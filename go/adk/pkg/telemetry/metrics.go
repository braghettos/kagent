package telemetry

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

// GenAI semantic-convention attribute keys and metric names.
// See https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-metrics/
const (
	metricGenAIClientTokenUsage = "gen_ai.client.token.usage"

	attrGenAITokenType    = "gen_ai.token.type"
	attrGenAIRequestModel = "gen_ai.request.model"
	attrGenAIProviderName = "gen_ai.provider.name"

	tokenTypeInput  = "input"
	tokenTypeOutput = "output"

	meterName = "github.com/kagent-dev/kagent/go/adk"
)

// genAIMetrics holds the GenAI metric instruments. It is recorded by the A2A
// executor when the agent runtime reports token usage. When metrics are
// disabled (the default), instrumentInstance is nil and the public Record*
// helpers are cheap no-ops, keeping the runtime byte-identical when off.
type genAIMetrics struct {
	tokenUsage metric.Int64Histogram
}

var (
	instrumentMu       sync.RWMutex
	instrumentInstance *genAIMetrics
)

// metricsEnabled reports whether the OTLP metrics pipeline is opted in.
func metricsEnabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_METRICS_ENABLED")), "true")
}

// newMeterProvider builds a MeterProvider with an OTLP metric exporter, mirroring
// newTracerProvider's protocol/endpoint resolution (grpc default, http/protobuf
// opt-in via OTEL_EXPORTER_OTLP[_METRICS]_PROTOCOL).
func newMeterProvider(ctx context.Context, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	protocol := resolveOTLPProtocol("METRICS")
	metricEndpoint := resolveEndpoint("METRICS")

	var exporter sdkmetric.Exporter
	var err error

	switch protocol {
	case "http/protobuf":
		var opts []otlpmetrichttp.Option
		if metricEndpoint != "" {
			opts = append(opts, otlpmetrichttp.WithEndpointURL(metricEndpoint))
		}
		exporter, err = otlpmetrichttp.New(ctx, opts...)
	default:
		var opts []otlpmetricgrpc.Option
		if metricEndpoint != "" {
			if u, parseErr := url.Parse(metricEndpoint); parseErr == nil && u.Scheme != "" && u.Host != "" {
				opts = append(opts, otlpmetricgrpc.WithEndpointURL(u.String()))
			} else {
				opts = append(opts, otlpmetricgrpc.WithEndpoint(metricEndpoint))
			}
		}
		exporter, err = otlpmetricgrpc.New(ctx, opts...)
	}
	if err != nil {
		return nil, err
	}

	reader := sdkmetric.NewPeriodicReader(exporter,
		sdkmetric.WithInterval(30*time.Second),
	)
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	), nil
}

// initGenAIMetrics creates the GenAI metric instruments from the global meter
// provider and installs them as the package-level recorder. It is called from
// Init after otel.SetMeterProvider so the instruments bind to the OTLP pipeline.
func initGenAIMetrics() error {
	meter := otel.GetMeterProvider().Meter(meterName)

	tokenUsage, err := meter.Int64Histogram(
		metricGenAIClientTokenUsage,
		metric.WithUnit("{token}"),
		metric.WithDescription("Measures the number of input and output tokens used by GenAI requests."),
	)
	if err != nil {
		return err
	}

	instrumentMu.Lock()
	instrumentInstance = &genAIMetrics{tokenUsage: tokenUsage}
	instrumentMu.Unlock()
	return nil
}

// resetGenAIMetrics clears the package-level recorder. Used on shutdown so a
// subsequent run without metrics enabled stays a no-op.
func resetGenAIMetrics() {
	instrumentMu.Lock()
	instrumentInstance = nil
	instrumentMu.Unlock()
}

func currentGenAIMetrics() *genAIMetrics {
	instrumentMu.RLock()
	defer instrumentMu.RUnlock()
	return instrumentInstance
}

// RecordTokenUsage records input/output token counts on the
// gen_ai.client.token.usage histogram, tagged with gen_ai.token.type,
// gen_ai.request.model, and gen_ai.provider.name. It is a no-op when metrics
// are disabled (instrument not initialised).
func RecordTokenUsage(ctx context.Context, model, provider string, inputTokens, outputTokens int64) {
	m := currentGenAIMetrics()
	if m == nil || m.tokenUsage == nil {
		return
	}

	baseAttrs := make([]attribute.KeyValue, 0, 3)
	if model != "" {
		baseAttrs = append(baseAttrs, attribute.String(attrGenAIRequestModel, model))
	}
	if provider != "" {
		baseAttrs = append(baseAttrs, attribute.String(attrGenAIProviderName, provider))
	}

	if inputTokens > 0 {
		attrs := append([]attribute.KeyValue{attribute.String(attrGenAITokenType, tokenTypeInput)}, baseAttrs...)
		m.tokenUsage.Record(ctx, inputTokens, metric.WithAttributes(attrs...))
	}
	if outputTokens > 0 {
		attrs := append([]attribute.KeyValue{attribute.String(attrGenAITokenType, tokenTypeOutput)}, baseAttrs...)
		m.tokenUsage.Record(ctx, outputTokens, metric.WithAttributes(attrs...))
	}
}
