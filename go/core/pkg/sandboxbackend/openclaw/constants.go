package openclaw

const (
	// NemoclawSandboxBaseImage is the default OpenShell VM image for OpenClaw/NemoClaw harnesses.
	// Substrate requires workload images to use @sha256:... refs (see pinImageRef). (OpenShell doesn't care)
	// Tag: 2026.5.4
	NemoclawSandboxBaseImage = "ghcr.io/kagent-dev/nemoclaw/sandbox-base@sha256:d52bee415dc4c0dba7164f9eabe727574c056d4f211781f20af249707883a3b4"

	// AcpSandboxOpenClawImage is the default Substrate workload image for
	// OpenClaw/NemoClaw harnesses: the kagent acp-sandbox openclaw target
	// (docker/acp-sandbox/Dockerfile), which layers the acp-shim and the
	// restore-proof gateway-ensure scripts onto an OpenClaw install.
	// Substrate admission requires a digest-pinned ref.
	AcpSandboxOpenClawImage = "ttl.sh/kagent-acp-openclaw@sha256:f8c7b73253dd00098d3f2cb2c3a3d7585fa549daadeefdacd563362e4d40c7e6"

	// SubstrateActorHome is the home directory of the unprivileged user in
	// AcpSandboxOpenClawImage (USER agent); openclaw.json is written under it.
	SubstrateActorHome = "/home/agent"

	// openshellSecretProviderID is the secrets.providers key written into openclaw.json for OpenShell sandboxes.
	openshellSecretProviderID = "kagent"

	// substrateSecretProviderID is the env SecretRef provider id for native OpenClaw on Substrate.
	substrateSecretProviderID = "default"

	// DefaultInferenceBaseURL is the Model provider baseUrl when ModelConfig does not set an explicit upstream (OpenShell).
	DefaultInferenceBaseURL = "https://inference.local/v1"

	// SubstrateBootstrapDefaultBaseURL is passed when building openclaw.json for Substrate harnesses.
	// When ModelConfig has no explicit provider URL, the models section is omitted entirely so
	// OpenClaw is not given a partial providers.* block (baseUrl is required when present).
	SubstrateBootstrapDefaultBaseURL = ""
)
