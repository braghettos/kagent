"""GenAI + agent metrics plugin for kagent ADK agents.

A BasePlugin (same shape as LLMPassthroughPlugin) that records OTel GenAI-semconv metrics on the
global meter. The meter is a no-op until kagent.core.configure_metrics() enables a MeterProvider
(gated by OTEL_METRICS_ENABLED), so this plugin is safe to register unconditionally.

  - gen_ai.client.token.usage (input/output) from each LLM response's usage_metadata
  - krateo.agent.invocations per agent run
"""

import logging

from google.adk.agents.base_agent import BaseAgent
from google.adk.agents.callback_context import CallbackContext
from google.adk.models.llm_response import LlmResponse
from google.adk.plugins.base_plugin import BasePlugin
from opentelemetry import metrics

logger = logging.getLogger(__name__)


class KrateoTelemetryPlugin(BasePlugin):
    """Records GenAI token-usage + agent-invocation metrics (OTel GenAI semconv)."""

    def __init__(self) -> None:
        super().__init__(name="krateo_telemetry")
        meter = metrics.get_meter("kagent.adk.krateo_telemetry")
        self._token_usage = meter.create_histogram(
            "gen_ai.client.token.usage",
            unit="{token}",
            description="Number of input/output tokens used per LLM call.",
        )
        self._invocations = meter.create_counter(
            "krateo.agent.invocations",
            description="Number of agent invocations.",
        )

    async def before_agent_callback(self, *, agent: BaseAgent, callback_context: CallbackContext):
        self._invocations.add(1, {"krateo.agent.name": agent.name})
        return None

    async def after_model_callback(self, *, callback_context: CallbackContext, llm_response: LlmResponse):
        usage = getattr(llm_response, "usage_metadata", None)
        if usage is None:
            return None
        try:
            agent_name = callback_context._invocation_context.agent.name
        except Exception:
            agent_name = "unknown"
        prompt = getattr(usage, "prompt_token_count", None)
        if prompt:
            self._token_usage.record(prompt, {"gen_ai.agent.name": agent_name, "gen_ai.token.type": "input"})
        output = getattr(usage, "candidates_token_count", None)
        if output:
            self._token_usage.record(output, {"gen_ai.agent.name": agent_name, "gen_ai.token.type": "output"})
        return None
