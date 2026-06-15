package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

/*
P4.7.5 contract basis:
- docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md section 7 requires stdout to
  contain only PlaybotResult JSON, stderr to contain redacted logs, and business
  failures to be expressed through PlaybotResult.status/code.
- docs/P4_7_5_PLAYBOT_GO_AGENT_IMPLEMENTATION_PLAN.md section 4.1 assigns the
  CLI stdout/stderr contract to cmd/browserwing-playbot-agent.

Current expected red state:
- the CLI main package does not exist yet, so go run . fails before the stdout
  contract can pass.

Targeted verification:
- cd ..\playbot-agent
- go test ./cmd/browserwing-playbot-agent -run TestP475 -count=1
*/

func TestP475CLIStdoutContainsOnlyPlaybotResultJSON(t *testing.T) {
	secret := "sk-cli-contract-secret"
	stdout, stderr, err := runP475AgentCLI(t, secret)
	if err != nil {
		t.Fatalf("agent CLI returned process error %v; stderr: %s", err, stderr)
	}
	if strings.Contains(stdout, "\n{") || strings.Contains(stdout, "log") {
		t.Fatalf("stdout contains non-result content: %q", stdout)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not PlaybotResult JSON: %v; stdout: %s; stderr: %s", err, stdout, stderr)
	}
	if result["schema_version"] != "p4.7.5" {
		t.Fatalf("schema_version = %v, want p4.7.5; result: %v", result["schema_version"], result)
	}
	if result["status"] != "failed" || result["code"] != "recording_action_missing_target" {
		t.Fatalf("business quality failure = status %v code %v, want failed/recording_action_missing_target; result: %v", result["status"], result["code"], result)
	}
}

func TestP475CLIRedactsSecretChannelFromStderr(t *testing.T) {
	secret := "sk-cli-redaction-secret"
	stdout, stderr, err := runP475AgentCLI(t, secret)
	if err != nil {
		t.Fatalf("agent CLI returned process error %v; stdout: %s; stderr: %s", err, stdout, stderr)
	}
	if strings.Contains(stdout, secret) {
		t.Fatalf("stdout leaked secret channel value: %s", stdout)
	}
	if strings.Contains(stderr, secret) {
		t.Fatalf("stderr leaked secret channel value: %s", stderr)
	}
	if strings.Contains(stderr, `C:\Users\`) {
		t.Fatalf("stderr leaked local absolute path: %s", stderr)
	}
}

func runP475AgentCLI(t *testing.T, secret string) (stdout string, stderr string, err error) {
	t.Helper()
	fixture := filepath.Join("..", "..", "testdata", "generate_missing_target_job.json")
	cmd := exec.Command("go", "run", ".", "--mode", "generate", "--input", fixture)
	cmd.Env = append(os.Environ(), "BROWSERWING_PLAYBOT_LLM_API_KEY="+secret)
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	runErr := cmd.Run()
	return out.String(), errOut.String(), runErr
}
