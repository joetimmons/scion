package runtimebroker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/config"
)

// grokLiteralConfigYAML is the auth contract of the custom grok-literal
// bundle: api-key only, XAI_API_KEY required, no no_auth shell.
const grokLiteralConfigYAML = `name: grok-literal
harness: grok-build
image: scion-grok-build:latest
user: scion
provisioner:
  type: container-script
  interface_version: 1
  command: ["python3", "/home/scion/.scion/harness/provision.py"]
  timeout: 30s
  lifecycle_events: ["pre-start"]
command:
  base: ["grok", "--permission-mode", "dontAsk", "--deny", "*", "--disable-web-search", "--no-subagents", "--verbatim"]
auth:
  default_type: api-key
  types:
    api-key:
      required_env:
        - any_of: ["XAI_API_KEY"]
  autodetect:
    env:
      XAI_API_KEY: api-key
`

// TestEnvGather_GrokLiteralGlobalOnHubManagedWorkspace: a workstation global
// bundle is the one a hub sync publishes. A hub-managed workspace has no
// project harness-config tier. Env-gather must still ask for XAI_API_KEY.
func TestEnvGather_GrokLiteralGlobalOnHubManagedWorkspace(t *testing.T) {
	if errs, err := config.ValidateHarnessConfig([]byte(grokLiteralConfigYAML)); err != nil || len(errs) > 0 {
		t.Fatalf("schema: err=%v errs=%v", err, errs)
	}
	srv := newTestServer(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(home, ".scion", "projects", "box")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	hcDir := filepath.Join(home, ".scion", "harness-configs", "grok-literal")
	if err := os.MkdirAll(hcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hcDir, "config.yaml"), []byte(grokLiteralConfigYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	req := CreateAgentRequest{
		Name:        "grok-auth",
		ProjectPath: workspace,
		GatherEnv:   true,
		Config: &CreateAgentConfig{
			HarnessConfig: "grok-literal",
			HarnessAuth:   "api-key",
		},
	}
	required, _, _, _ := srv.extractRequiredEnvKeys(req, "")
	if !slices.Contains(required, "XAI_API_KEY") {
		t.Fatalf("required = %v, want XAI_API_KEY", required)
	}

	req.ResolvedEnv = map[string]string{"XAI_API_KEY": "dummy-not-a-real-key"}
	required, _, _, _ = srv.extractRequiredEnvKeys(req, "")
	if slices.Contains(required, "XAI_API_KEY") {
		t.Fatalf("dummy key still required: %v", required)
	}
}

// TestEnvGather_GrokLiteralHTTPNeedsXAIKey is the create response the CLI
// turns into "Sending 1 environment value(s) to the Hub: XAI_API_KEY".
func TestEnvGather_GrokLiteralHTTPNeedsXAIKey(t *testing.T) {
	srv := newTestServer(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(home, ".scion", "projects", "box")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	hcDir := filepath.Join(home, ".scion", "harness-configs", "grok-literal")
	if err := os.MkdirAll(hcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hcDir, "config.yaml"), []byte(grokLiteralConfigYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{
		"name": "grok-auth-connected",
		"id": "agent-grok-literal",
		"gatherEnv": true,
		"projectPath": "` + workspace + `",
		"config": {"harnessConfig": "grok-literal", "harnessAuth": "api-key", "profile": "default"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var envReqs EnvRequirementsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &envReqs); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(envReqs.Needs, "XAI_API_KEY") {
		t.Fatalf("needs = %v, required = %v", envReqs.Needs, envReqs.Required)
	}
}
