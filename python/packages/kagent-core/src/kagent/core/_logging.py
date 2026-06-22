import json
import logging
import os
from datetime import datetime, timezone

try:
    from opentelemetry import trace as _otel_trace
except ImportError:  # opentelemetry-api is a hard dep; tolerate absence in minimal envs
    _otel_trace = None

_logging_configured = False

# Kept for backward-compatibility with any importer; OTelJSONFormatter below
# supersedes it for actual formatting.
LOG_FORMAT = "%(asctime)s - %(name)s - %(levelname)s - %(message)s"

# Standard LogRecord attributes that are NOT re-emitted as structured fields.
_RESERVED = {
    "name", "msg", "args", "levelname", "levelno", "pathname", "filename", "module",
    "exc_info", "exc_text", "stack_info", "lineno", "funcName", "created", "msecs",
    "relativeCreated", "thread", "threadName", "processName", "process", "taskName",
    "message", "asctime",
}


def _severity_number(levelno: int) -> int:
    """OpenTelemetry SeverityNumber for a Python logging level."""
    if levelno >= logging.CRITICAL:
        return 21  # FATAL
    if levelno >= logging.ERROR:
        return 17  # ERROR
    if levelno >= logging.WARNING:
        return 13  # WARN
    if levelno >= logging.INFO:
        return 9  # INFO
    return 5  # DEBUG


class OTelJSONFormatter(logging.Formatter):
    """One JSON object per line in the OpenTelemetry log data model: RFC3339Nano
    `timestamp`, level (SeverityText) + `SeverityNumber`, `msg` body only,
    `trace_id`/`span_id` when a span is active, and resource attributes from env.
    """

    def __init__(self) -> None:
        super().__init__()
        self._resource = {
            "service.name": os.getenv("OTEL_SERVICE_NAME") or "kagent",
            "service.namespace": os.getenv("SERVICE_NAMESPACE", "krateo"),
        }
        version = os.getenv("SERVICE_VERSION")
        if version:
            self._resource["service.version"] = version
        composition_id = os.getenv("KRATEO_COMPOSITION_ID")
        if composition_id:
            self._resource["krateo.io/composition-id"] = composition_id

    def format(self, record: logging.LogRecord) -> str:
        out = {
            "timestamp": datetime.fromtimestamp(record.created, tz=timezone.utc).isoformat(),
            "level": record.levelname,
            "SeverityNumber": _severity_number(record.levelno),
            "msg": record.getMessage(),
            "logger": record.name,
        }
        out.update(self._resource)

        # Trace correlation when a span is active.
        if _otel_trace is not None:
            ctx = _otel_trace.get_current_span().get_span_context()
            if ctx is not None and ctx.is_valid:
                out["trace_id"] = f"{ctx.trace_id:032x}"
                out["span_id"] = f"{ctx.span_id:016x}"

        # User-supplied structured fields (logger.info(..., extra={...})).
        for key, value in record.__dict__.items():
            if key not in _RESERVED and not key.startswith("_"):
                out[key] = value

        if record.exc_info:
            out["exception"] = self.formatException(record.exc_info)

        return json.dumps(out, default=str)


def configure_logging() -> None:
    """Configure structured JSON (OTel log model) logging from LOG_LEVEL."""
    global _logging_configured

    log_level = os.getenv("LOG_LEVEL", "INFO").upper()
    formatter = OTelJSONFormatter()

    if not logging.root.handlers:
        handler = logging.StreamHandler()
        handler.setFormatter(formatter)
        logging.basicConfig(level=log_level, handlers=[handler])
        _logging_configured = True
        logging.info("Logging configured", extra={"log_level": log_level})
    elif not _logging_configured:
        logging.root.setLevel(log_level)
        for handler in logging.root.handlers:
            handler.setFormatter(formatter)
        _logging_configured = True
        logging.info("Logging reconfigured to structured JSON", extra={"log_level": log_level})
    else:
        logging.root.setLevel(log_level)
