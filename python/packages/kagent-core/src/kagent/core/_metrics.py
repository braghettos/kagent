"""OpenTelemetry GenAI metrics pipeline for kagent ADK agents.

Mirrors kagent.core.tracing.configure(): a gated MeterProvider exporting OTLP metrics over
http/protobuf, sharing the same resource (service.name/namespace/version + krateo.io/composition-id)
as the traces and logs. Gated on OTEL_METRICS_ENABLED (default off); idempotent / no-op otherwise.
"""

import logging
import os

from opentelemetry import metrics
from opentelemetry.exporter.otlp.proto.http.metric_exporter import OTLPMetricExporter
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource

_configured = False


def configure_metrics(name: str = "kagent", namespace: str = "kagent") -> None:
    """Configure the OTLP MeterProvider when OTEL_METRICS_ENABLED=true; idempotent / no-op otherwise."""
    global _configured
    if _configured:
        return
    if os.getenv("OTEL_METRICS_ENABLED", "false").lower() != "true":
        return

    attrs = {"service.name": name, "service.namespace": namespace}
    if sv := os.getenv("SERVICE_VERSION"):
        attrs["service.version"] = sv
    if cid := os.getenv("KRATEO_COMPOSITION_ID"):
        attrs["krateo.io/composition-id"] = cid

    reader = PeriodicExportingMetricReader(OTLPMetricExporter())
    metrics.set_meter_provider(MeterProvider(resource=Resource(attrs), metric_readers=[reader]))
    _configured = True
    logging.info("OpenTelemetry metrics enabled")
