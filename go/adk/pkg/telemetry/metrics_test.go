package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestRecordTokenUsage_RecordsGenAIHistogram verifies the GenAI token-usage
// histogram is recorded with input/output token-type attributes and the
// model/provider labels.
func TestRecordTokenUsage_RecordsGenAIHistogram(t *testing.T) {
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		resetGenAIMetrics()
	})

	if err := initGenAIMetrics(); err != nil {
		t.Fatalf("initGenAIMetrics: %v", err)
	}

	RecordTokenUsage(context.Background(), "gpt-4o", "openai", 100, 42)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	var found bool
	var dataPoints int
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != metricGenAIClientTokenUsage {
				continue
			}
			found = true
			hist, ok := m.Data.(metricdata.Histogram[int64])
			if !ok {
				t.Fatalf("expected Histogram[int64], got %T", m.Data)
			}
			dataPoints = len(hist.DataPoints)
			for _, dp := range hist.DataPoints {
				tt, ok := dp.Attributes.Value(attribute.Key(attrGenAITokenType))
				if !ok {
					t.Errorf("data point missing %s attribute", attrGenAITokenType)
				}
				if tt.AsString() != tokenTypeInput && tt.AsString() != tokenTypeOutput {
					t.Errorf("unexpected token type %q", tt.AsString())
				}
				if model, ok := dp.Attributes.Value(attribute.Key(attrGenAIRequestModel)); !ok || model.AsString() != "gpt-4o" {
					t.Errorf("expected model gpt-4o, got %q (present=%v)", model.AsString(), ok)
				}
				if prov, ok := dp.Attributes.Value(attribute.Key(attrGenAIProviderName)); !ok || prov.AsString() != "openai" {
					t.Errorf("expected provider openai, got %q (present=%v)", prov.AsString(), ok)
				}
			}
		}
	}
	if !found {
		t.Fatalf("metric %s not recorded", metricGenAIClientTokenUsage)
	}
	if dataPoints != 2 {
		t.Errorf("expected 2 data points (input+output), got %d", dataPoints)
	}
}

// TestRecordTokenUsage_NoopWhenDisabled verifies recording is a safe no-op when
// the instruments have not been initialised (metrics disabled).
func TestRecordTokenUsage_NoopWhenDisabled(t *testing.T) {
	resetGenAIMetrics()
	// Must not panic.
	RecordTokenUsage(context.Background(), "gpt-4o", "openai", 100, 42)
}
