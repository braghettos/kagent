package substrate

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	atev1alpha1 "github.com/agent-substrate/substrate/pkg/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/api/v1alpha2"
	"github.com/kagent-dev/kagent/go/core/internal/utils"
	"github.com/kagent-dev/kagent/go/core/pkg/sandboxbackend/openclaw"
	corev1 "k8s.io/api/core/v1"
)

// Default Substrate workload images for the generic acp-shim agent targets
// (docker/acp-sandbox/Dockerfile). Substrate admission requires digest-pinned
// refs.
const (
	// AcpSandboxHermesImage is the acp-sandbox "hermes" target.
	AcpSandboxHermesImage = "ttl.sh/kagent-acp-hermes@sha256:38e8f2d34ea753070ca47094cc231295d1a07b245821d40853726bd0065d8c57"
	// AcpSandboxCodexImage is the acp-sandbox "codex" target.
	AcpSandboxCodexImage = "ttl.sh/kagent-acp-codex@sha256:d5029db3ffbf2b30924001f7eb76943d38048d8cb836b4e20413ee8585a7a182"
	// AcpSandboxClaudeImage is the acp-sandbox "claude" target.
	AcpSandboxClaudeImage = "ttl.sh/kagent-acp-claude@sha256:79917a45e947c9edc737ee63fa48998c02ee0e7c167a8212cace76167bca1de1"
	// AcpSandboxCopilotImage is the acp-sandbox "copilot" target.
	AcpSandboxCopilotImage = "ttl.sh/kagent-acp-copilot@sha256:f2e198e9e0c386d2f31cbd214205f6fed58a91284c59bbd1cc21520ae97d0692"
	// AcpSandboxGeminiImage is the acp-sandbox "gemini" target.
	AcpSandboxGeminiImage = "ttl.sh/kagent-acp-gemini@sha256:26db24eef270eceb02a5352e72ea0127b90a0daefb14f103802e13a87ae9ff11"
	// AcpSandboxGooseImage is the acp-sandbox "goose" target.
	AcpSandboxGooseImage = "ttl.sh/kagent-acp-goose@sha256:36d6e4f449cd4a54ecdb4925dab96d1e775c344622855f13111e687012453dca"
)

// acpAgentSpec describes how to run one stdio ACP agent behind the acp-shim
// inside a Substrate actor.
type acpAgentSpec struct {
	// DefaultImage is the digest-pinned acp-sandbox target image used when
	// neither the harness nor cluster defaults specify a workload image.
	DefaultImage string
	// ChildCommand is the stdio ACP agent command the shim spawns per
	// connection (shell-safe words, joined with spaces).
	ChildCommand []string
}

// acpAgentSpecs maps non-OpenClaw substrate backends to their agent commands.
// Per-connection child policy is used for all of them: Substrate actors are
// checkpointed/restored, and a child spawned per connection is always a
// fresh post-restore process (the same reason the OpenClaw path re-ensures
// its gateway per connection).
var acpAgentSpecs = map[v1alpha2.AgentHarnessBackendType]acpAgentSpec{
	v1alpha2.AgentHarnessBackendHermes: {
		DefaultImage: AcpSandboxHermesImage,
		ChildCommand: []string{"hermes", "acp"},
	},
	v1alpha2.AgentHarnessBackendCodex: {
		DefaultImage: AcpSandboxCodexImage,
		ChildCommand: []string{"codex-acp"},
	},
	v1alpha2.AgentHarnessBackendClaude: {
		DefaultImage: AcpSandboxClaudeImage,
		ChildCommand: []string{"claude-code-acp"},
	},
	v1alpha2.AgentHarnessBackendCopilot: {
		DefaultImage: AcpSandboxCopilotImage,
		ChildCommand: []string{"copilot", "--acp", "--stdio"},
	},
	v1alpha2.AgentHarnessBackendGemini: {
		DefaultImage: AcpSandboxGeminiImage,
		ChildCommand: []string{"gemini", "--experimental-acp"},
	},
	v1alpha2.AgentHarnessBackendGoose: {
		DefaultImage: AcpSandboxGooseImage,
		ChildCommand: []string{"goose", "acp"},
	},
}

// buildAcpAgentActorStartup returns the ateom workload startup script and
// container env for a generic stdio ACP agent (hermes/codex/claude) on
// Substrate. Unlike OpenClaw there is no in-sandbox gateway: the shim owns
// the atenet ingress port and bridges WebSocket frames to a per-connection
// agent child. Model credentials come from the harness ModelConfig as a
// provider-conventional env var (e.g. OPENAI_API_KEY, ANTHROPIC_API_KEY)
// resolved by ate-api from the referenced Secret.
func (p *Lifecycle) buildAcpAgentActorStartup(ctx context.Context, ah *v1alpha2.AgentHarness, spec acpAgentSpec) (script string, env []atev1alpha1.EnvVar, err error) {
	if ah == nil {
		return "", nil, fmt.Errorf("AgentHarness is required")
	}

	containerEnv := []corev1.EnvVar{
		{Name: "HOME", Value: openclaw.SubstrateActorHome},
		// Substrate actors do not inherit the image's ENV (unlike docker run),
		// so the shim's exec.LookPath and any subprocesses the agent spawns
		// need an explicit PATH. Includes the hermes image's venv bin dir.
		{Name: "PATH", Value: openclaw.SubstrateActorHome + "/.venv/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
	}
	if ah.Spec.Backend == v1alpha2.AgentHarnessBackendGoose {
		// No system keyring in the sandbox: goose must read provider API keys
		// from env vars. (Image ENV is not inherited under Substrate.)
		containerEnv = append(containerEnv, corev1.EnvVar{Name: "GOOSE_DISABLE_KEYRING", Value: "1"})
	}

	prelude := ""
	if ref := strings.TrimSpace(ah.Spec.ModelConfigRef); ref != "" {
		mcRef, parseErr := utils.ParseRefString(ref, ah.Namespace)
		if parseErr != nil {
			return "", nil, fmt.Errorf("parse modelConfigRef %q: %w", ref, parseErr)
		}
		mc := &v1alpha2.ModelConfig{}
		if getErr := p.Client.Get(ctx, mcRef, mc); getErr != nil {
			return "", nil, fmt.Errorf("get ModelConfig %s: %w", mcRef, getErr)
		}
		apiKeyEnv, keyErr := openclaw.ModelConfigAPIKeyEnvVar(mc)
		if keyErr != nil {
			return "", nil, keyErr
		}
		containerEnv = append(containerEnv, apiKeyEnv)

		switch ah.Spec.Backend {
		case v1alpha2.AgentHarnessBackendHermes:
			prelude = hermesConfigPrelude(mc)
		case v1alpha2.AgentHarnessBackendGemini:
			if model := strings.TrimSpace(mc.Spec.Model); model != "" {
				containerEnv = append(containerEnv, corev1.EnvVar{Name: "GEMINI_MODEL", Value: model})
			}
		case v1alpha2.AgentHarnessBackendGoose:
			containerEnv = append(containerEnv, gooseModelEnv(mc, apiKeyEnv)...)
		}
	}

	// Backend-agnostic env passthrough (e.g. GH_TOKEN for copilot, whose
	// credential is a GitHub token rather than a ModelConfig API key).
	// Appended last so it cannot shadow HOME/PATH or the shim token.
	containerEnv = append(containerEnv, ah.Spec.Env...)

	containerEnv = append(containerEnv, acpShimTokenEnv(ah)...)

	script = fmt.Sprintf(
		"set -e\n%sexec /usr/local/bin/acp-shim \\\n  --listen :%d \\\n  --child-policy per-connection \\\n  -- %s",
		prelude, acpListenPort, strings.Join(spec.ChildCommand, " "))
	return script, actorTemplateEnvFromPodEnv(containerEnv), nil
}

// hermesProviderSlugs maps kagent ModelConfig providers to hermes provider
// slugs (hermes_cli CANONICAL_PROVIDERS). Hermes authenticates these via the
// provider-conventional env var already injected from the ModelConfig secret.
var hermesProviderSlugs = map[v1alpha2.ModelProvider]string{
	v1alpha2.ModelProviderOpenAI:    "openai-api",
	v1alpha2.ModelProviderAnthropic: "anthropic",
	v1alpha2.ModelProviderGemini:    "gemini",
}

// hermesConfigPrelude returns shell lines that write ~/.hermes/config.yaml
// selecting the ModelConfig's model and provider. Without it hermes defaults
// to an unauthenticated provider and prompts silently produce no output.
// Returns "" when the ModelConfig provider has no hermes equivalent.
func hermesConfigPrelude(mc *v1alpha2.ModelConfig) string {
	slug, ok := hermesProviderSlugs[mc.Spec.Provider]
	if !ok || strings.TrimSpace(mc.Spec.Model) == "" {
		return ""
	}
	cfg := fmt.Sprintf("model:\n  default: %q\n  provider: %q\n", mc.Spec.Model, slug)
	if mc.Spec.Provider == v1alpha2.ModelProviderOpenAI {
		// Hermes auto-upgrades direct api.openai.com to its codex_responses
		// transport, which requests reasoning.encrypted_content — rejected
		// with HTTP 400 by non-reasoning models (e.g. gpt-4.1-mini), and the
		// turn ends silently. Pin the plain chat-completions transport.
		cfg += "  api_mode: chat_completions\n"
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(cfg))
	return fmt.Sprintf("mkdir -p \"$HOME/.hermes\"\necho %s | base64 -d > \"$HOME/.hermes/config.yaml\"\n", encoded)
}

// gooseProviderSlugs maps kagent ModelConfig providers to goose provider
// names (GOOSE_PROVIDER). Goose reads the provider API key from the
// provider-conventional env var when the keyring is disabled.
var gooseProviderSlugs = map[v1alpha2.ModelProvider]string{
	v1alpha2.ModelProviderOpenAI:    "openai",
	v1alpha2.ModelProviderAnthropic: "anthropic",
	v1alpha2.ModelProviderGemini:    "google",
	v1alpha2.ModelProviderOllama:    "ollama",
}

// gooseModelEnv returns GOOSE_PROVIDER/GOOSE_MODEL env vars selecting the
// ModelConfig's model. Without them goose has no configured provider and
// `goose acp` fails at session start. Goose's google provider reads
// GOOGLE_API_KEY, so the ModelConfig secret is aliased under that name too.
func gooseModelEnv(mc *v1alpha2.ModelConfig, apiKeyEnv corev1.EnvVar) []corev1.EnvVar {
	slug, ok := gooseProviderSlugs[mc.Spec.Provider]
	if !ok || strings.TrimSpace(mc.Spec.Model) == "" {
		return nil
	}
	env := []corev1.EnvVar{
		{Name: "GOOSE_PROVIDER", Value: slug},
		{Name: "GOOSE_MODEL", Value: strings.TrimSpace(mc.Spec.Model)},
	}
	if mc.Spec.Provider == v1alpha2.ModelProviderGemini {
		alias := apiKeyEnv.DeepCopy()
		alias.Name = "GOOGLE_API_KEY"
		env = append(env, *alias)
	}
	return env
}
