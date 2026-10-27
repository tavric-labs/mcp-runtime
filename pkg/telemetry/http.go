package telemetry

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"mcp-runtime/pkg/envcfg"
	"mcp-runtime/pkg/httpx"
)

// StartMetricsServer starts a Prometheus metrics server with /metrics and /health.
func StartMetricsServer(port string) (func(context.Context) error, <-chan error) {
	metricsServer := &http.Server{
		Addr:              listenAddr(port),
		Handler:           metricsHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errs := make(chan error, 1)
	go func() {
		defer close(errs)
		if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	return metricsServer.Shutdown, errs
}

func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.InstrumentMetricHandler(prometheus.DefaultRegisterer, promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{EnableOpenMetrics: true})))
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func listenAddr(port string) string {
	port = strings.TrimSpace(port)
	if port == "" {
		return ":0"
	}
	if strings.HasPrefix(port, ":") || strings.Contains(port, ":") {
		return port
	}
	return ":" + port
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
	r.wroteHeader = true
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(body)
}

// Unwrap exposes the underlying writer so http.ResponseController can reach
// the connection, e.g. for handlers that extend their read/write deadlines.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *statusRecorder) Flush() {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// LogRequests emits correlated logfmt completion records using route templates.
// Raw URLs, headers and bodies are deliberately excluded; healthy probes are quiet.
func LogRequests(next http.Handler) http.Handler {
	return LogRequestsForService(envcfg.EnvOr("OTEL_SERVICE_NAME", "mcp-service"), next)
}

// requestIDForLog returns the id the request middleware stored, or a
// well-formed header when the middleware is not in front of this logger.
// Anything else is dropped so a caller cannot write into the log line.
func requestIDForLog(r *http.Request) string {
	if r == nil {
		return ""
	}
	if id := httpx.RequestID(r.Context()); id != "" {
		return id
	}
	id := r.Header.Get(httpx.RequestIDHeader)
	if httpx.ValidRequestID(id) {
		return id
	}
	return ""
}

// LogRequestsForService keeps bootstrap service identity aligned with metrics.
func LogRequestsForService(service string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: CorrelateResponse(w, r.Context()), status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		probe := IsProbePath(r)
		if probe && recorder.status < http.StatusBadRequest {
			return
		}
		operation := r.Pattern
		if operation == "" {
			operation = "unmatched"
		}
		if probe {
			operation = "probe"
		}
		if failureStatus(recorder.status) {
			RecordSpanFailure(r.Context(), operation, http.StatusText(recorder.status), recorder.status, nil)
		}
		// #nosec G706 -- quoted fields escape control characters; no raw URL or body.
		log.Printf("event=http.request service=%q operation=%q method=%q status=%d duration_ms=%.3f request_id=%q%s",
			service, operation, r.Method, recorder.status,
			float64(time.Since(start))/float64(time.Millisecond), requestIDForLog(r), TraceLogSuffix(r.Context()))
	})
}
