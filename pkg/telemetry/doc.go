// Package telemetry owns service tracing, correlated logging, HTTP request
// metrics and response correlation. Service entry points initialize exporters
// and middleware; domain packages do not depend on this package. Kafka header
// adaptation lives separately in telemetry/kafkatrace.
package telemetry
