package substrate

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"fmt"
	"strings"
	"text/template"

	atev1alpha1 "github.com/agent-substrate/substrate/pkg/api/v1alpha1"
	"github.com/kagent-dev/kagent/go/api/v1alpha2"
	"github.com/kagent-dev/kagent/go/core/internal/utils"
	"github.com/kagent-dev/kagent/go/core/pkg/sandboxbackend/openclaw"
	corev1 "k8s.io/api/core/v1"
)

// OpenClawGatewayPort is the loopback port the OpenClaw gateway listens on
// inside a substrate actor. The acp-shim owns the atenet ingress port
// (acpListenPort) and passes non-ACP traffic (Control UI) through to it.
const OpenClawGatewayPort = 18789

// acpListenPort is the actor port atenet-router routes Host-based traffic to.
const acpListenPort = 80

//go:embed templates/openclaw_startup.sh.tmpl
var openClawStartupScriptTmplContent string

var openClawStartupScriptTmpl = template.Must(template.New("openclaw_startup").Parse(openClawStartupScriptTmplContent))

type openClawStartupScriptData struct {
	OpenClawJSONBase64 string
	GatewayPort        int
	ACPPort            int
}

// buildOpenClawActorStartup returns the ateom workload startup script and container env for OpenClaw on Substrate.
// When spec.modelConfigRef is set, openclaw.json includes models/agents/channels like the OpenShell bootstrap path.
func (p *Lifecycle) buildOpenClawActorStartup(ctx context.Context, ah *v1alpha2.AgentHarness) (script string, env []atev1alpha1.EnvVar, err error) {
	if ah == nil {
		return "", nil, fmt.Errorf("AgentHarness is required")
	}
	if p.Client == nil {
		return "", nil, fmt.Errorf("substrate lifecycle kubernetes client is required")
	}

	token, err := ResolveGatewayToken(ctx, p.Client, ah)
	if err != nil {
		return "", nil, fmt.Errorf("resolve gateway token: %w", err)
	}
	gw := openclaw.SubstrateGatewayBootstrap(token, OpenClawGatewayPort, openClawControlUIBasePath(ah))

	var jsonBytes []byte
	var containerEnv []corev1.EnvVar

	ref := strings.TrimSpace(ah.Spec.ModelConfigRef)
	if ref != "" {
		mcRef, parseErr := utils.ParseRefString(ref, ah.Namespace)
		if parseErr != nil {
			return "", nil, fmt.Errorf("parse modelConfigRef %q: %w", ref, parseErr)
		}
		mc := &v1alpha2.ModelConfig{}
		if getErr := p.Client.Get(ctx, mcRef, mc); getErr != nil {
			return "", nil, fmt.Errorf("get ModelConfig %s: %w", mcRef, getErr)
		}
		jsonBytes, containerEnv, err = openclaw.BuildSubstrateBootstrapJSON(ctx, p.Client, ah.Namespace, ah, mc, gw)
		if err != nil {
			return "", nil, fmt.Errorf("build openclaw bootstrap json: %w", err)
		}
	} else {
		jsonBytes, err = openclaw.BuildGatewayOnlyBootstrapJSON(gw)
		if err != nil {
			return "", nil, fmt.Errorf("build gateway-only openclaw json: %w", err)
		}
		containerEnv = []corev1.EnvVar{{Name: "HOME", Value: openclaw.SubstrateActorHome}}
	}
	containerEnv = append(containerEnv, acpShimEnv(ah, gw.Port)...)
	script, err = openClawStartupScript(jsonBytes, gw.Port)
	if err != nil {
		return "", nil, err
	}
	return script, actorTemplateEnvFromPodEnv(containerEnv), nil
}

// acpShimEnv returns the env vars the acp-shim and the image's
// openclaw-gateway-ensure/openclaw-acp-child scripts read. The shim reuses
// the harness gateway token as its bearer token; when the token comes from a
// Secret it stays a secretKeyRef (resolved by ate-api), never inlined.
func acpShimEnv(ah *v1alpha2.AgentHarness, gatewayPort int) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "OPENCLAW_GATEWAY_PORT", Value: fmt.Sprintf("%d", gatewayPort)},
	}
	return append(env, acpShimTokenEnv(ah)...)
}

// acpShimTokenEnv returns the ACP_SHIM_TOKEN env var derived from the
// harness gateway token (secretKeyRef stays a ref, resolved by ate-api).
func acpShimTokenEnv(ah *v1alpha2.AgentHarness) []corev1.EnvVar {
	var env []corev1.EnvVar
	sub := ah.Spec.Substrate
	if sub != nil && sub.GatewayTokenSecretRef != nil && strings.TrimSpace(sub.GatewayTokenSecretRef.Name) != "" {
		env = append(env, corev1.EnvVar{
			Name: "ACP_SHIM_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: sub.GatewayTokenSecretRef.Name},
					Key:                  GatewayTokenSecretKey,
				},
			},
		})
	} else if sub != nil && strings.TrimSpace(sub.GatewayToken) != "" {
		env = append(env, corev1.EnvVar{Name: "ACP_SHIM_TOKEN", Value: strings.TrimSpace(sub.GatewayToken)})
	}
	return env
}

func openClawControlUIBasePath(ah *v1alpha2.AgentHarness) string {
	if ah == nil {
		return ""
	}
	return "/api/agentharnesses/" + ah.Namespace + "/" + ah.Name + "/gateway"
}

func openClawStartupScript(jsonBytes []byte, gwPort int) (string, error) {
	var buf bytes.Buffer
	if err := openClawStartupScriptTmpl.Execute(&buf, openClawStartupScriptData{
		OpenClawJSONBase64: base64.StdEncoding.EncodeToString(jsonBytes),
		GatewayPort:        gwPort,
		ACPPort:            acpListenPort,
	}); err != nil {
		return "", fmt.Errorf("render openclaw startup script: %w", err)
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}
