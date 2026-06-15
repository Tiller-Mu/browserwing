package playbotagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

/*
P4.7.5 contract basis:
- docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md sections 5.1, 5.2, and 7
  require the backend to call an independent Go Playbot agent, pass secrets only
  through a controlled channel, and keep Python playbot-engine out of the formal
  generation/optimization path.
- docs/P4_7_5_PLAYBOT_GO_AGENT_IMPLEMENTATION_PLAN.md sections 4.3 and 5 assign
  this adapter to backend/services/playbotagent.

Current expected red state:
- backend/services/playbotagent has no production adapter yet, so these tests fail
  with missing symbols.

Targeted verification:
- cd backend
- go test ./services/playbotagent -run TestP475 -count=1
*/

func TestP475ClientInvokesIndependentGoAgentWithSecretChannel(t *testing.T) {
	tmpDir := t.TempDir()
	callsFile := filepath.Join(tmpDir, "calls.json")
	agentPath := writeP475FakeAgentBinary(t, tmpDir, callsFile, `{"schema_version":"p4.7.5","status":"success","code":"","test_cases":[{"title":"generated","description":"ok","steps":[{"action":"navigate","url":"/orders"}]}],"context_trace":{"source_page_script_id":"ps_123","source_hash":"sha256:test","used_fields":["action_trace"]}}`)
	secret := "sk-backend-agent-secret"

	client := NewBinaryClient(BinaryClientOptions{
		CommandPath:     agentPath,
		SecretEnvName:   "BROWSERWING_PLAYBOT_LLM_API_KEY",
		WorkingDir:      tmpDir,
		RedactLocalPath: true,
	})
	result, err := client.Run(context.Background(), Job{
		SchemaVersion: "p4.7.5",
		Mode:          "generate",
		RequestID:     "backend-agent-contract",
		LLMRuntimeConfig: LLMRuntimeConfig{
			Provider:        "custom",
			Endpoint:        "https://llm.invalid/v1",
			Model:           "contract-model",
			ConfigID:        "default-test-llm",
			RedactedSummary: "custom contract-model",
		},
		SecretChannel: SecretChannel{EnvName: "BROWSERWING_PLAYBOT_LLM_API_KEY", Value: secret},
		RecordingSource: RecordingSource{
			SourcePageScriptID: "ps_123",
			ActionTrace:        []map[string]any{{"type": "click", "target": map[string]any{"text": "Save"}}},
			DOMSnapshot:        map[string]any{"elements": []map[string]any{{"text": "Save"}}},
			RecordingMeta:      map[string]any{"schema_version": 1, "recording_kind": "business_flow", "auth_context": "clean"},
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != "success" || len(result.TestCases) != 1 {
		t.Fatalf("result = %+v, want one success testcase", result)
	}

	call := readP475AgentCall(t, callsFile)
	if !strings.Contains(call.Args, "--mode generate") || !strings.Contains(call.Args, "--input") {
		t.Fatalf("agent args = %q, want --mode generate and --input", call.Args)
	}
	if !strings.Contains(call.Env, "BROWSERWING_PLAYBOT_LLM_API_KEY="+secret) {
		t.Fatalf("secret channel env was not passed to agent; env: %s", call.Env)
	}
	jobData := readP475AgentInput(t, call.InputPath)
	requireP475AgentPayloadOmits(t, jobData, secret, "api_key", "cookie", "localStorage", "sessionStorage", `C:\Users\`)
}

func TestP475ClientRejectsPythonPlaybotEngineCommand(t *testing.T) {
	_, err := NewBinaryClient(BinaryClientOptions{
		CommandPath: "python",
		EngineDir:   filepath.Join("..", "..", "playbot-engine"),
	})
	if err == nil || !strings.Contains(err.Error(), "playbot_agent_python_engine_forbidden") {
		t.Fatalf("NewBinaryClient python command error = %v, want playbot_agent_python_engine_forbidden", err)
	}
}

type p475AgentCall struct {
	Args      string `json:"args"`
	Env       string `json:"env"`
	InputPath string `json:"input_path"`
}

func writeP475FakeAgentBinary(t *testing.T, dir, callsFile, stdout string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-browserwing-playbot-agent")
	if runtime.GOOS == "windows" {
		path += ".cmd"
		script := strings.Join([]string{
			"@echo off",
			"setlocal enabledelayedexpansion",
			"set \"INPUT_FILE=\"",
			":parse_args",
			"if \"%~1\"==\"\" goto after_parse_args",
			"if \"%~1\"==\"--input\" (",
			"  shift",
			"  set \"INPUT_FILE=%~1\"",
			")",
			"shift",
			"goto parse_args",
			":after_parse_args",
			"echo {\"args\":\"%*\",\"env\":\"BROWSERWING_PLAYBOT_LLM_API_KEY=%BROWSERWING_PLAYBOT_LLM_API_KEY%\",\"input_path\":\"!INPUT_FILE:\\=\\\\!\"}>\"" + callsFile + "\"",
			"echo " + stdout,
			"exit /b 0",
			"",
		}, "\r\n")
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake agent cmd: %v", err)
		}
		return path
	}
	script := strings.Join([]string{
		"#!/bin/sh",
		"input_file=\"\"",
		"args=\"$*\"",
		"while [ $# -gt 0 ]; do",
		"  if [ \"$1\" = \"--input\" ]; then shift; input_file=\"$1\"; fi",
		"  shift",
		"done",
		"printf '{\"args\":\"%s\",\"env\":\"BROWSERWING_PLAYBOT_LLM_API_KEY=%s\",\"input_path\":\"%s\"}\\n' \"$args\" \"$BROWSERWING_PLAYBOT_LLM_API_KEY\" \"$input_file\" > \"" + callsFile + "\"",
		"cat <<'JSON'",
		stdout,
		"JSON",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake agent shell: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod fake agent shell: %v", err)
	}
	return path
}

func readP475AgentCall(t *testing.T, path string) p475AgentCall {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fake agent call: %v", err)
	}
	var call p475AgentCall
	if err := json.Unmarshal(data, &call); err != nil {
		t.Fatalf("decode fake agent call: %v; raw: %s", err, data)
	}
	return call
}

func readP475AgentInput(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fake agent input: %v", err)
	}
	return data
}

func requireP475AgentPayloadOmits(t *testing.T, data []byte, forbidden ...string) {
	t.Helper()
	text := string(data)
	for _, token := range forbidden {
		if strings.TrimSpace(token) != "" && strings.Contains(text, token) {
			t.Fatalf("agent payload leaked %q: %s", token, text)
		}
	}
}
