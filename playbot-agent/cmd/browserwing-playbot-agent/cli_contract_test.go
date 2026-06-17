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

func TestP475CLIGeneratesFillFromRecordedInputAction(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "generate-input-job.json")
	job := `{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"p475-input-action-fixture",
		"page_context":{"url":"https://example.invalid/orders","description":"orders page"},
		"recording_source":{
			"source_page_script_id":"ps-input",
			"action_trace":[{
				"type":"input",
				"target":{"placeholder":"API token","recorded_selector":"input[name=token]"},
				"value":"sk-test-token-for-recording",
				"intent_reason":"Enter the sandbox API token"
			}],
			"dom_snapshot":{"elements":[{"placeholder":"API token","recorded_selector":"input[name=token]"}]},
			"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean","target_url":"https://example.invalid/orders"}
		},
		"llm_runtime_config":{
			"provider":"custom",
			"endpoint":"https://llm.invalid/v1",
			"model":"contract-model",
			"config_id":"p475-default",
			"redacted_summary":"custom contract-model",
			"secret_channel":{"kind":"env","name":"BROWSERWING_PLAYBOT_LLM_API_KEY"}
		}
	}`
	if err := os.WriteFile(fixture, []byte(job), 0o600); err != nil {
		t.Fatalf("write input action fixture: %v", err)
	}

	stdout, stderr, err := runP475AgentCLIWithFixture(t, fixture, "sk-cli-contract-secret")
	if err != nil {
		t.Fatalf("agent CLI returned process error %v; stdout: %s; stderr: %s", err, stdout, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not PlaybotResult JSON: %v; stdout: %s; stderr: %s", err, stdout, stderr)
	}
	if result["status"] != "success" {
		t.Fatalf("status = %v, want success; result: %v; stderr: %s", result["status"], result, stderr)
	}
	cases, _ := result["test_cases"].([]any)
	if len(cases) != 1 {
		t.Fatalf("test_cases = %#v, want one generated case", result["test_cases"])
	}
	blueprint, _ := cases[0].(map[string]any)
	steps, _ := blueprint["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("steps = %#v, want one generated step", blueprint["steps"])
	}
	step, _ := steps[0].(map[string]any)
	if step["action"] != "fill" || step["value"] != "sk-test-token-for-recording" {
		t.Fatalf("compiled input step = %#v, want fill with recorded test token", step)
	}
	if target, _ := step["target"].(map[string]any); target["recorded_selector"] != "input[name=token]" {
		t.Fatalf("compiled input target = %#v, want recorded selector preserved", step["target"])
	}
}

func TestP475CLIEventsMaskRecordedInputValues(t *testing.T) {
	tmpDir := t.TempDir()
	fixture := filepath.Join(tmpDir, "generate-input-job.json")
	eventsPath := filepath.Join(tmpDir, "events.jsonl")
	recordedValue := "sk-test-token-for-recording"
	job := `{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"p475-events-mask-fixture",
		"page_context":{"url":"https://example.invalid/orders","description":"orders page"},
		"recording_source":{
			"source_page_script_id":"ps-events-mask",
			"action_trace":[{"type":"input","target":{"placeholder":"API token","recorded_selector":"input[name=token]"},"value":"` + recordedValue + `","intent_reason":"Enter the sandbox API token"}],
			"dom_snapshot":{"elements":[{"placeholder":"API token","recorded_selector":"input[name=token]"}]},
			"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean","target_url":"https://example.invalid/orders"}
		},
		"llm_runtime_config":{"provider":"custom","endpoint":"https://llm.invalid/v1","model":"contract-model","secret_channel":{"kind":"env","name":"BROWSERWING_PLAYBOT_LLM_API_KEY"}}
	}`
	if err := os.WriteFile(fixture, []byte(job), 0o600); err != nil {
		t.Fatalf("write event fixture: %v", err)
	}
	cmd := exec.Command("go", "run", ".", "--mode", "generate", "--input", fixture, "--events", eventsPath)
	cmd.Env = append(os.Environ(), "BROWSERWING_PLAYBOT_LLM_API_KEY=sk-cli-event-secret")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("agent CLI returned process error %v; stdout: %s; stderr: %s", err, stdout.String(), stderr.String())
	}
	events, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events JSONL: %v", err)
	}
	if strings.Contains(string(events), recordedValue) {
		t.Fatalf("events leaked recorded input value: %s", events)
	}
	if !strings.Contains(string(events), "已录制输入值") || !strings.Contains(string(events), "llm_visible_output") {
		t.Fatalf("events should include masked candidate steps and visible model output: %s", events)
	}
}

func TestP475CLIAcceptsDocumentedRepairProposalMode(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "repair-proposal-job.json")
	job := `{
		"schema_version":"p4.7.5",
		"mode":"repair_proposal",
		"request_id":"p475-repair-proposal-fixture",
		"recording_source":{
			"source_page_script_id":"ps-repair",
			"action_trace":[{"type":"click","target":{"text":"Submit"}}]
		},
		"existing_blueprint":{
			"title":"failing case",
			"description":"old description",
			"steps":[{"action":"click","target":{"text":"Submit"}}]
		},
		"execution_report":{"status":"failed"}
	}`
	if err := os.WriteFile(fixture, []byte(job), 0o600); err != nil {
		t.Fatalf("write repair proposal fixture: %v", err)
	}

	stdout, stderr, err := runP475AgentCLIWithModeAndFixture(t, "repair-proposal", fixture, "sk-cli-contract-secret")
	if err != nil {
		t.Fatalf("agent CLI returned process error %v; stdout: %s; stderr: %s", err, stdout, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not PlaybotResult JSON: %v; stdout: %s; stderr: %s", err, stdout, stderr)
	}
	if result["status"] != "success" {
		t.Fatalf("status = %v, want success for documented repair-proposal mode; result: %v; stderr: %s", result["status"], result, stderr)
	}
	if proposal, _ := result["repair_proposal"].(map[string]any); len(proposal) == 0 {
		t.Fatalf("repair_proposal = %#v, want draft proposal", result["repair_proposal"])
	}
}

func TestP475CLIRejectsRepairProposalWithoutRecordedFacts(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "repair-proposal-no-facts-job.json")
	job := `{
		"schema_version":"p4.7.5",
		"mode":"repair_proposal",
		"request_id":"p475-repair-proposal-no-facts-fixture",
		"existing_blueprint":{
			"title":"failing case",
			"description":"old description",
			"steps":[{"action":"click","target":{"text":"Submit"}}]
		},
		"execution_report":{"status":"failed"}
	}`
	if err := os.WriteFile(fixture, []byte(job), 0o600); err != nil {
		t.Fatalf("write repair proposal no-facts fixture: %v", err)
	}

	stdout, stderr, err := runP475AgentCLIWithModeAndFixture(t, "repair-proposal", fixture, "sk-cli-contract-secret")
	if err != nil {
		t.Fatalf("agent CLI returned process error %v; stdout: %s; stderr: %s", err, stdout, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not PlaybotResult JSON: %v; stdout: %s; stderr: %s", err, stdout, stderr)
	}
	if result["status"] != "failed" || result["code"] != "recording_fact_required" {
		t.Fatalf("repair proposal no-facts result = status %v code %v, want failed/recording_fact_required; result: %v", result["status"], result["code"], result)
	}
	if proposal, _ := result["repair_proposal"].(map[string]any); len(proposal) != 0 {
		t.Fatalf("repair proposal should not be returned without recorded facts: %v", result)
	}
}

func runP475AgentCLI(t *testing.T, secret string) (stdout string, stderr string, err error) {
	t.Helper()
	fixture := filepath.Join("..", "..", "testdata", "generate_missing_target_job.json")
	return runP475AgentCLIWithFixture(t, fixture, secret)
}

func runP475AgentCLIWithFixture(t *testing.T, fixture string, secret string) (stdout string, stderr string, err error) {
	t.Helper()
	return runP475AgentCLIWithModeAndFixture(t, "generate", fixture, secret)
}

func runP475AgentCLIWithModeAndFixture(t *testing.T, mode string, fixture string, secret string) (stdout string, stderr string, err error) {
	t.Helper()
	cmd := exec.Command("go", "run", ".", "--mode", mode, "--input", fixture)
	cmd.Env = append(os.Environ(), "BROWSERWING_PLAYBOT_LLM_API_KEY="+secret)
	var out bytes.Buffer
	var errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	runErr := cmd.Run()
	return out.String(), errOut.String(), runErr
}
