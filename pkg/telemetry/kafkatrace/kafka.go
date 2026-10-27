package kafkatrace

import (
	"context"
	"sort"
	"strings"

	"github.com/segmentio/kafka-go"

	"mcp-runtime/pkg/telemetry"
)

// InjectKafkaHeaders appends the active trace context to Kafka headers.
func InjectKafkaHeaders(ctx context.Context, headers []kafka.Header) []kafka.Header {
	traceHeaders := telemetry.CaptureTraceContext(ctx)
	if len(traceHeaders) == 0 {
		return headers
	}
	keys := make([]string, 0, len(traceHeaders))
	for key := range traceHeaders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		headers = append(headers, kafka.Header{Key: key, Value: []byte(traceHeaders[key])})
	}
	return headers
}

// ExtractKafkaHeaders extracts trace context from Kafka headers into ctx.
func ExtractKafkaHeaders(ctx context.Context, headers []kafka.Header) context.Context {
	if len(headers) == 0 {
		return telemetry.ContextWithTraceContext(ctx, nil)
	}
	carrier := make(map[string]string, len(headers))
	for _, header := range headers {
		key := strings.ToLower(strings.TrimSpace(header.Key))
		if key == "" || len(header.Value) == 0 {
			continue
		}
		carrier[key] = string(header.Value)
	}
	return telemetry.ContextWithTraceContext(ctx, carrier)
}
