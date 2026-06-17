package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

/*
P4.7.5 contract basis:
- docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md sections 7, 10, and 13 define
  PlaybotJob/PlaybotResult schema_version, status enum, context_trace, and secret
  redaction boundaries.
- docs/P4_7_5_PLAYBOT_GO_AGENT_IMPLEMENTATION_PLAN.md section 4.1 assigns these
  tests to the Go agent protocol layer.

Current expected red state:
- playbot-agent currently has no production protocol package, so these tests fail
  with missing symbols before any implementation is allowed.

Targeted verification:
- cd ..\playbot-agent
- go test ./internal/protocol -run TestP475 -count=1
*/

func TestP475ResultStatusEnumAndSchemaVersion(t *testing.T) {
	valid := []string{StatusSuccess, StatusFailed, StatusContextRequired}
	for _, status := range valid {
		raw, err := json.Marshal(map[string]any{
			"schema_version": "p4.7.5",
			"status":         status,
			"code":           "",
			"context_trace": map[string]any{
				"source_page_script_id": "ps_123",
				"source_hash":           "sha256:contract",
				"used_fields":           []string{"action_trace", "dom_snapshot", "recording_meta"},
			},
		})
		if err != nil {
			t.Fatalf("marshal result fixture: %v", err)
		}
		result, err := DecodeAndValidatePlaybotResult(raw)
		if err != nil {
			t.Fatalf("DecodeAndValidatePlaybotResult(%s) returned error: %v", status, err)
		}
		if result.SchemaVersion != "p4.7.5" {
			t.Fatalf("schema_version = %q, want p4.7.5", result.SchemaVersion)
		}
		if result.Status != status {
			t.Fatalf("status = %q, want %q", result.Status, status)
		}
	}

	invalid := []byte(`{"schema_version":"p4.7.5","status":"running","code":"not_allowed","context_trace":{}}`)
	if _, err := DecodeAndValidatePlaybotResult(invalid); err == nil || !strings.Contains(err.Error(), "playbot_result_invalid_status") {
		t.Fatalf("invalid status error = %v, want playbot_result_invalid_status", err)
	}
}

func TestP475ProtocolRejectsSecretsInPlaybotJobJSON(t *testing.T) {
	secret := "sk-p475-contract-secret"
	cookieValue := "cookie-value-must-not-enter-job"
	storageValue := "local-storage-value-must-not-enter-job"
	localPath := `C:\Users\someone\AppData\Local\browserwing\profile`

	cleanJob := PlaybotJob{
		SchemaVersion: "p4.7.5",
		Mode:          ModeGenerate,
		RequestID:     "req-p475-clean",
		ProjectScope: ProjectScope{
			ProjectID: 10,
			VersionID: 20,
			PageID:    30,
		},
		PageContext: PageContext{
			URL:         "https://example.invalid/orders",
			Description: "orders page",
		},
		RecordingSource: RecordingSource{
			SourcePageScriptID: "ps_123",
			ActionTrace: []RecordedAction{{
				Type: "click",
				Target: RecordedTarget{
					Role:             "button",
					Text:             "Cookies",
					RecordedSelector: "button.save",
				},
			}},
			DOMSnapshot: map[string]any{"elements": []map[string]any{{"role": "button", "text": "Cookies"}}},
			RecordingMeta: RecordingMeta{
				SchemaVersion: 1,
				RecordingKind: "business_flow",
				AuthContext:   "clean",
				TargetURL:     "https://example.invalid/orders",
			},
		},
		LLMRuntimeConfig: LLMRuntimeConfig{
			Provider:        "custom",
			Endpoint:        "https://llm.invalid/v1",
			Model:           "contract-model",
			ConfigID:        "default-contract",
			TimeoutMs:       30000,
			RetryCount:      1,
			RedactedSummary: "custom contract-model",
			SecretChannel: SecretChannelRef{
				Kind: "env",
				Name: "BROWSERWING_PLAYBOT_LLM_API_KEY",
			},
		},
	}
	raw, err := MarshalPlaybotJob(cleanJob)
	if err != nil {
		t.Fatalf("MarshalPlaybotJob returned error: %v", err)
	}
	requireP475ProtocolOmits(t, raw, "backend_approved_context")
	requireP475ProtocolOmits(t, raw, secret, cookieValue, storageValue, localPath, "api_key", "cookies", "localStorage", "sessionStorage")
	if _, err := DecodeAndValidatePlaybotJob(raw); err != nil {
		t.Fatalf("clean job should validate: %v; json: %s", err, raw)
	}

	contaminated := []byte(`{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"req-p475-contaminated",
		"llm_runtime_config":{"provider":"custom","model":"x","api_key":"sk-p475-contract-secret"},
		"recording_source":{
			"action_trace":[{"type":"click","cookies":[{"name":"sid","value":"cookie-value-must-not-enter-job"}]}],
			"dom_snapshot":{"localStorage":{"token":"local-storage-value-must-not-enter-job"}},
			"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean"}
		}
	}`)
	if _, err := DecodeAndValidatePlaybotJob(contaminated); err == nil || !strings.Contains(err.Error(), "playbot_job_secret_leak") {
		t.Fatalf("contaminated job error = %v, want playbot_job_secret_leak", err)
	}

	localPathJob := []byte(`{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"req-p475-local-path",
		"page_context":{"url":"https://example.invalid/orders","description":"uses D:\\dpProject\\browserwing\\profiles\\state.json"},
		"recording_source":{
			"action_trace":[{"type":"click","target":{"text":"Save"}}],
			"dom_snapshot":{"elements":[{"text":"Save"}]},
			"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean"}
		}
	}`)
	if _, err := DecodeAndValidatePlaybotJob(localPathJob); err == nil || !strings.Contains(err.Error(), "playbot_job_secret_leak") {
		t.Fatalf("local path job error = %v, want playbot_job_secret_leak", err)
	}

	tokenLikeBusinessText := []byte(`{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"req-p475-token-like-business-text",
		"page_context":{"url":"https://example.invalid/orders","description":"recorded test token sk-test-token-for-recording"},
		"recording_source":{
			"action_trace":[{"type":"input","target":{"text":"sk-payment-token-field"},"value":"sk-test-token-for-recording"}],
			"dom_snapshot":{"elements":[{"text":"sk-payment-token-field"}]},
			"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean"}
		}
	}`)
	if _, err := DecodeAndValidatePlaybotJob(tokenLikeBusinessText); err != nil {
		t.Fatalf("token-like business text should validate: %v", err)
	}
}

func TestP475BackendApprovedContextRetryJobShape(t *testing.T) {
	retryJob := []byte(`{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"req-p475-context-retry",
		"project_scope":{"project_id":10,"version_id":20,"page_id":30},
		"page_context":{"url":"https://example.invalid/orders","description":"orders page"},
		"recording_source":{
			"source_page_script_id":"ps_123",
			"action_trace":[{"type":"click","target":{"role":"button","text":"Save","recorded_selector":"button.save"},"ref_id":"recorded_action_0"}],
			"dom_snapshot":{"elements":[{"ref_id":"recorded_action_0","role":"button","text":"Save"}]},
			"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean","target_url":"https://example.invalid/orders"}
		},
		"backend_approved_context":[
			{
				"kind":"dom_snapshot",
				"scope":"recorded_action_0",
				"source":"page_script.dom_snapshot",
				"payload":{"elements":[{"ref_id":"recorded_action_0","role":"button","text":"Save","recorded_selector":"button.save"}]}
			}
		],
		"llm_runtime_config":{
			"provider":"custom",
			"endpoint":"https://llm.invalid/v1",
			"model":"contract-model",
			"config_id":"default-contract",
			"timeout_ms":30000,
			"retry_count":1,
			"redacted_summary":"custom contract-model",
			"secret_channel":{"kind":"env","name":"BROWSERWING_PLAYBOT_LLM_API_KEY"}
		}
	}`)
	if _, err := DecodeAndValidatePlaybotJob(retryJob); err != nil {
		t.Fatalf("retry job with backend-approved context should validate: %v; json: %s", err, retryJob)
	}

	missingSource := []byte(`{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"req-p475-context-missing-source",
		"recording_source":{"source_page_script_id":"ps_123"},
		"backend_approved_context":[{"kind":"dom_snapshot","scope":"recorded_action_0","payload":{"elements":[]}}]
	}`)
	if _, err := DecodeAndValidatePlaybotJob(missingSource); err == nil || !strings.Contains(err.Error(), "playbot_job_backend_approved_context_invalid") {
		t.Fatalf("missing source error = %v, want playbot_job_backend_approved_context_invalid", err)
	}

	missingPayload := []byte(`{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"req-p475-context-missing-payload",
		"recording_source":{"source_page_script_id":"ps_123"},
		"backend_approved_context":[{"kind":"dom_snapshot","scope":"recorded_action_0","source":"page_script.dom_snapshot"}]
	}`)
	if _, err := DecodeAndValidatePlaybotJob(missingPayload); err == nil || !strings.Contains(err.Error(), "playbot_job_backend_approved_context_invalid") {
		t.Fatalf("missing payload error = %v, want playbot_job_backend_approved_context_invalid", err)
	}
}

func TestP475LLMRuntimeConfigSerializesOnlyRedactedSummary(t *testing.T) {
	secret := "sk-p475-runtime-secret"
	cfg, channel, err := BuildLLMRuntimeConfig(LLMRuntimeConfigInput{
		Provider:   "custom",
		Endpoint:   "https://llm.invalid/v1",
		Model:      "contract-model",
		ConfigID:   "runtime-config-id",
		APIKey:     secret,
		TimeoutMs:  30000,
		RetryCount: 1,
	})
	if err != nil {
		t.Fatalf("BuildLLMRuntimeConfig returned error: %v", err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal llm runtime config: %v", err)
	}
	requireP475ProtocolOmits(t, raw, secret, "api_key")
	if strings.TrimSpace(cfg.RedactedSummary) == "" {
		t.Fatalf("RedactedSummary is empty: %+v", cfg)
	}
	resolved, err := channel.ResolveForProcess()
	if err != nil {
		t.Fatalf("secret channel should resolve for process: %v", err)
	}
	if resolved != secret {
		t.Fatalf("resolved secret = %q, want original secret from controlled channel", resolved)
	}
}

func TestP475ProtocolFixturesDoNotContainSecrets(t *testing.T) {
	forbidden := []string{
		"api_key",
		"cookies",
		"localStorage",
		"sessionStorage",
		`C:\Users\`,
	}
	root := filepath.Join("..", "..", "testdata")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, token := range forbidden {
			if strings.Contains(string(data), token) {
				t.Fatalf("fixture %s contains forbidden token %q", path, token)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan protocol fixtures: %v", err)
	}
}

func requireP475ProtocolOmits(t *testing.T, raw []byte, forbidden ...string) {
	t.Helper()
	text := string(raw)
	for _, token := range forbidden {
		if strings.TrimSpace(token) == "" {
			continue
		}
		if strings.Contains(text, token) {
			t.Fatalf("serialized protocol leaked %q: %s", token, text)
		}
	}
}
