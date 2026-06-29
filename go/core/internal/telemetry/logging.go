package telemetry

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"
	otelzap "go.opentelemetry.io/contrib/bridges/otelzap"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel/attribute"
	logglobal "go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	crzap "sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/kagent-dev/kagent/go/core/pkg/env"
)

const loggerBridgeName = "github.com/kagent-dev/kagent/go/core"

// InitLoggerProvider configures an OTLP LoggerProvider and registers it as the
// global OTel logger provider. The exporter type and endpoint are read from the
// standard OTEL environment variables via autoexport, mirroring
// InitTracerProvider. The returned shutdown function must be called on process
// exit to flush in-flight log records. When OTEL_LOGGING_ENABLED is unset the
// pipeline is not created and a no-op shutdown is returned (default-OFF).
func InitLoggerProvider(ctx context.Context, serviceVersion string) (func(context.Context) error, error) {
	if !env.OtelLoggingEnabled.Get() {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("create log exporter: %w", err)
	}

	res, err := newTelemetryResource(ctx, serviceVersion)
	if err != nil {
		return nil, err
	}

	lp := log.NewLoggerProvider(
		log.WithProcessor(log.NewBatchProcessor(exporter)),
		log.WithResource(res),
	)

	logglobal.SetLoggerProvider(lp)

	return lp.Shutdown, nil
}

// ControllerZapOpts returns controller-runtime zap options. When
// OTEL_LOGGING_ENABLED is set it additively tees the controller's stdout zap
// core with an otelzap bridge core, routing the controller's own logs through
// the global OTLP LoggerProvider while preserving stdout logging. When disabled
// it returns no options, leaving the logger byte-identical to upstream.
func ControllerZapOpts() []crzap.Opts {
	if !env.OtelLoggingEnabled.Get() {
		return nil
	}
	bridgeCore := otelzap.NewCore(loggerBridgeName,
		otelzap.WithLoggerProvider(logglobal.GetLoggerProvider()),
	)
	return []crzap.Opts{
		crzap.RawZapOpts(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
			return zapcore.NewTee(core, bridgeCore)
		})),
	}
}

// newTelemetryResource builds the OTel resource shared by the controller's
// signal pipelines. Kept consistent with InitTracerProvider's attributes.
func newTelemetryResource(ctx context.Context, serviceVersion string) (*resource.Resource, error) {
	instanceID, err := os.Hostname()
	if err != nil || instanceID == "" {
		instanceID = uuid.New().String()
	}

	attrs := []attribute.KeyValue{
		semconv.ServiceName(ServiceName),
		semconv.ServiceVersion(serviceVersion),
		semconv.ServiceNamespace(ServiceNamespace),
		semconv.ServiceInstanceID(instanceID),
	}
	if ns := os.Getenv("KAGENT_NAMESPACE"); ns != "" {
		attrs = append(attrs, semconv.K8SNamespaceName(ns))
	}
	if pod := os.Getenv("K8S_POD_NAME"); pod != "" {
		attrs = append(attrs, semconv.K8SPodName(pod))
	}
	if node := os.Getenv("K8S_NODE_NAME"); node != "" {
		attrs = append(attrs, semconv.K8SNodeName(node))
	}

	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(attrs...),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("create OTEL resource: %w", err)
	}
	return res, nil
}
