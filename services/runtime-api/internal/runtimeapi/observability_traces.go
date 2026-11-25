package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mcp-runtime/pkg/transport/httptrace"
)

const (
	envTempoAPIURL     = "TEMPO_API_URL"
	defaultTempoAPIURL = "http://tempo.mcp-observability.svc:3200"

	// serverUIDAttribute is the resource attribute the operator stamps on every
	// exporting container of a server. It is the immutable instance identity, so a
	// reused name never exposes an earlier server's spans.
	serverUIDAttribute = "mcp.server.uid"

	traceSearchLimit    = 20
	traceMaxBodyBytes   = 8 << 20
	traceMaxSpans       = 2000
	traceQueryTimeout   = 10 * time.Second
	traceSearchLookback = time.Hour
)

var traceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{16}$|^[0-9a-fA-F]{32}$`)

// HandleRuntimeObservabilityTraces returns one server's traces without handing
// the caller a query language. Tempo is single-tenant and a trace can contain
// spans from other servers (a caller that talks to several of them shares one
// trace), so isolation cannot rely on the search alone:
//
//   - the search expression is built here from the authorized server's namespace
//     and UID; no caller text reaches Tempo;
//   - the response is rebuilt from spans that matched, so root names and
//     services of other workloads in the same trace are never returned;
//   - a trace is returned only after its spans are filtered to resources that
//     carry this server's UID, and a trace with none of them is "not found".
func (s *RuntimeServer) HandleRuntimeObservabilityTraces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	_, target, err := s.authorizedObservabilityTarget(r)
	if err != nil {
		writeObservabilityError(w, err)
		return
	}
	uid := string(target.UID)
	traceID := strings.TrimSpace(r.URL.Query().Get("trace_id"))
	if r.URL.Query().Has("trace_id") && !traceIDPattern.MatchString(traceID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_trace_id"})
		return
	}
	base, err := tempoBaseURL()
	if err != nil {
		log.Printf("observability tempo proxy configuration error: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "traces not configured"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), traceQueryTimeout)
	defer cancel()

	if traceID == "" {
		query := fmt.Sprintf(`{resource.service.namespace=%s && resource.%s=%s}`, promQLString(target.Namespace), serverUIDAttribute, promQLString(uid))
		now := time.Now()
		values := url.Values{
			"q":     {query},
			"limit": {strconv.Itoa(traceSearchLimit)},
			"start": {strconv.FormatInt(now.Add(-traceSearchLookback).Unix(), 10)},
			"end":   {strconv.FormatInt(now.Unix(), 10)},
		}
		body, status := tempoGet(ctx, base+"/api/search?"+values.Encode())
		if status != http.StatusOK {
			writeTempoFailure(w, target.Namespace, target.Name, status)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"namespace": target.Namespace, "server": target.Name, "traces": ownTraceSummaries(body)})
		return
	}

	body, status := tempoGet(ctx, base+"/api/traces/"+traceID)
	if status == http.StatusNotFound {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "trace not found"})
		return
	}
	if status != http.StatusOK {
		writeTempoFailure(w, target.Namespace, target.Name, status)
		return
	}
	spans := ownSpans(body, uid)
	if len(spans) == 0 {
		// Indistinguishable from a missing trace, so a trace ID cannot be probed
		// to learn whether another server exists.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "trace not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespace": target.Namespace, "server": target.Name, "traceID": strings.ToLower(traceID), "spans": spans})
}

func tempoBaseURL() (string, error) {
	raw := strings.TrimSpace(envOr(envTempoAPIURL, defaultTempoAPIURL))
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("Tempo API URL must be absolute http(s) without credentials")
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

func tempoGet(ctx context.Context, target string) ([]byte, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	req.Header.Set("Accept", "application/json")
	resp, err := httptrace.DoHTTP(nil, req)
	if err != nil {
		return nil, http.StatusBadGateway
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, traceMaxBodyBytes+1))
	if err != nil || len(body) > traceMaxBodyBytes {
		return nil, http.StatusBadGateway
	}
	return body, http.StatusOK
}

func writeTempoFailure(w http.ResponseWriter, namespace, name string, status int) {
	log.Printf("observability tempo query failed namespace=%q server=%q status=%d", namespace, name, status)
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "tempo_unavailable"})
}

// ownTraceSummaries keeps only what the matched spans of this server say.
// Tempo's rootServiceName and rootTraceName describe the whole trace and may
// belong to another workload, so they are never copied.
func ownTraceSummaries(body []byte) []map[string]any {
	var search struct {
		Traces []struct {
			TraceID  string `json:"traceID"`
			SpanSets []struct {
				Spans []struct {
					Name              string `json:"name"`
					StartTimeUnixNano string `json:"startTimeUnixNano"`
					DurationNanos     string `json:"durationNanos"`
				} `json:"spans"`
			} `json:"spanSets"`
		} `json:"traces"`
	}
	out := []map[string]any{}
	if json.Unmarshal(body, &search) != nil {
		return out
	}
	for _, t := range search.Traces {
		if !traceIDPattern.MatchString(t.TraceID) || len(t.SpanSets) == 0 || len(t.SpanSets[0].Spans) == 0 {
			continue
		}
		span := t.SpanSets[0].Spans[0]
		durationMs := int64(0)
		if nanos, err := strconv.ParseInt(span.DurationNanos, 10, 64); err == nil && nanos > 0 {
			durationMs = nanos / int64(time.Millisecond)
		}
		out = append(out, map[string]any{"traceID": strings.ToLower(t.TraceID), "name": span.Name, "startTimeUnixNano": span.StartTimeUnixNano, "durationMs": durationMs})
		if len(out) == traceSearchLimit {
			break
		}
	}
	return out
}

type otlpAttribute struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

type otlpResourceSpans struct {
	Resource struct {
		Attributes []otlpAttribute `json:"attributes"`
	} `json:"resource"`
	ScopeSpans []struct {
		Spans []otlpSpan `json:"spans"`
	} `json:"scopeSpans"`
	InstrumentationLibrarySpans []struct {
		Spans []otlpSpan `json:"spans"`
	} `json:"instrumentationLibrarySpans"`
}

type otlpSpan struct {
	SpanID            string          `json:"spanId"`
	ParentSpanID      string          `json:"parentSpanId"`
	Name              string          `json:"name"`
	Kind              json.RawMessage `json:"kind"`
	StartTimeUnixNano json.RawMessage `json:"startTimeUnixNano"`
	EndTimeUnixNano   json.RawMessage `json:"endTimeUnixNano"`
	Attributes        []otlpAttribute `json:"attributes"`
	Status            json.RawMessage `json:"status"`
}

// ownSpans returns the spans whose resource carries exactly this server's UID.
// It understands Tempo's batches and resourceSpans shapes. Spans with no UID, a
// different UID, or an earlier instance's UID are dropped, with the resource
// that held them.
func ownSpans(body []byte, uid string) []map[string]any {
	var doc struct {
		Batches       []otlpResourceSpans `json:"batches"`
		ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
		Trace         struct {
			ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
		} `json:"trace"`
	}
	out := []map[string]any{}
	if uid == "" || json.Unmarshal(body, &doc) != nil {
		return out
	}
	blocks := append(append(append([]otlpResourceSpans{}, doc.Batches...), doc.ResourceSpans...), doc.Trace.ResourceSpans...)
	for _, block := range blocks {
		service, resourceUID := "", ""
		for _, attr := range block.Resource.Attributes {
			var value struct {
				StringValue string `json:"stringValue"`
			}
			_ = json.Unmarshal(attr.Value, &value)
			switch attr.Key {
			case "service.name":
				service = value.StringValue
			case serverUIDAttribute:
				resourceUID = value.StringValue
			}
		}
		if resourceUID != uid {
			continue
		}
		var groups [][]otlpSpan
		for _, scope := range block.ScopeSpans {
			groups = append(groups, scope.Spans)
		}
		for _, scope := range block.InstrumentationLibrarySpans {
			groups = append(groups, scope.Spans)
		}
		for _, spans := range groups {
			for _, span := range spans {
				out = append(out, map[string]any{
					"spanID": span.SpanID, "parentSpanID": span.ParentSpanID, "name": span.Name, "service": service,
					"kind": span.Kind, "startTimeUnixNano": span.StartTimeUnixNano, "endTimeUnixNano": span.EndTimeUnixNano,
					"attributes": span.Attributes, "status": span.Status,
				})
				if len(out) >= traceMaxSpans {
					return out
				}
			}
		}
	}
	return out
}
