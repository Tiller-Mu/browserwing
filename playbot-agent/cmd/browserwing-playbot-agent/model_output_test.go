package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/browserwing/browserwing/playbot-agent/internal/llmclient"
	"github.com/browserwing/browserwing/playbot-agent/internal/protocol"
)

func TestP475ResponseToModelOutputDropsRawUnknownFieldsAndMasksValues(t *testing.T) {
	output := responseToModelOutput(llmclient.Response{
		VisibleMessage: "可展示说明",
		CandidateSteps: []llmclient.CandidateStep{{
			Action:        "fill",
			TargetSummary: "API token input",
			ValueSummary:  "sk-test-token-for-recording",
			Reason:        "Enter the recorded sandbox token",
		}},
		Assumptions: []string{"使用已录制流程生成"},
		RiskNotes:   []string{"保存前仍需校验 Blueprint"},
		Raw: map[string]any{
			"debug":       "full prompt",
			"raw_context": map[string]any{"value": "raw-secret-value"},
			"candidate_steps": []any{map[string]any{
				"action":        "fill",
				"value":         "raw-secret-value",
				"value_summary": "raw-secret-value",
			}},
		},
	})

	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal model output: %v", err)
	}
	text := string(raw)
	for _, forbidden := range []string{"debug", "raw_context", "raw-secret-value", "sk-test-token-for-recording"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("model_output leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "已录制输入值") {
		t.Fatalf("model_output should include masked input summary: %s", text)
	}
	if output["visible_message"] != "可展示说明" {
		t.Fatalf("visible_message = %v, want 可展示说明", output["visible_message"])
	}
}

func TestP475VisibleOutputPromptIncludesStableTargetHints(t *testing.T) {
	job := protocol.PlaybotJob{
		SchemaVersion:   protocol.SchemaVersion,
		Mode:            protocol.ModeGenerate,
		UserInstruction: "generate checkout",
		PageContext: protocol.PageContext{
			URL:         "https://example.invalid/orders",
			Description: "orders",
		},
		RecordingSource: protocol.RecordingSource{
			ActionTrace: []protocol.RecordedAction{{
				Type:  "input",
				RefID: "recorded_action_0",
				Target: protocol.RecordedTarget{
					Text:             "Submit",
					RecordedSelector: "button.primary",
					Selector:         "#submit-primary",
					Role:             "button",
				},
				Value:        "sk-test-token-for-recording",
				IntentReason: "recorded reason",
			}},
		},
	}

	prompt := playbotVisibleOutputUserPrompt(job)
	if strings.Contains(prompt, "sk-test-token-for-recording") {
		t.Fatalf("prompt leaked recorded input value: %s", prompt)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(prompt), &payload); err != nil {
		t.Fatalf("unmarshal prompt: %v", err)
	}
	actions, ok := payload["actions"].([]any)
	if !ok || len(actions) != 1 {
		t.Fatalf("actions = %#v, want one action", payload["actions"])
	}
	action, ok := actions[0].(map[string]any)
	if !ok {
		t.Fatalf("action = %#v, want object", actions[0])
	}
	if got := action["target_summary"]; got != "Submit" {
		t.Fatalf("target_summary = %#v, want Submit", got)
	}
	if got := action["value"]; got != "<recorded_input_value>" {
		t.Fatalf("value = %#v, want recorded input placeholder", got)
	}
	hint, ok := action["target_hint"].(map[string]any)
	if !ok {
		t.Fatalf("target_hint = %#v, want object", action["target_hint"])
	}
	for key, want := range map[string]string{
		"ref_id":            "recorded_action_0",
		"recorded_selector": "button.primary",
		"selector":          "#submit-primary",
		"text":              "Submit",
	} {
		if got := hint[key]; got != want {
			t.Fatalf("target_hint[%s] = %#v, want %q", key, got, want)
		}
	}
}

func TestP475RunValidatesModePrerequisitesBeforeLLM(t *testing.T) {
	t.Setenv("BROWSERWING_PLAYBOT_LLM_API_KEY", "sk-test-visible-output")
	cases := []struct {
		name     string
		mode     string
		job      string
		wantCode string
	}{
		{
			name: "optimize missing blueprint",
			mode: protocol.ModeOptimize,
			job: `{
				"schema_version":"p4.7.5",
				"mode":"optimize",
				"request_id":"p475-optimize-preflight",
				"limits":{"enable_llm":true},
				"llm_runtime_config":{"endpoint":"http://127.0.0.1:1/v1","model":"contract-model","secret_channel":{"kind":"env","name":"BROWSERWING_PLAYBOT_LLM_API_KEY"}}
			}`,
			wantCode: "playbot_existing_blueprint_required",
		},
		{
			name: "repair proposal missing facts",
			mode: "repair-proposal",
			job: `{
				"schema_version":"p4.7.5",
				"mode":"repair_proposal",
				"request_id":"p475-repair-preflight",
				"existing_blueprint":{"title":"case","description":"old","steps":[{"action":"click","target":{"text":"Submit"}}]},
				"execution_report":{"status":"failed"},
				"limits":{"enable_llm":true},
				"llm_runtime_config":{"endpoint":"http://127.0.0.1:1/v1","model":"contract-model","secret_channel":{"kind":"env","name":"BROWSERWING_PLAYBOT_LLM_API_KEY"}}
			}`,
			wantCode: "recording_fact_required",
		},
		{
			name: "generate unsupported action",
			mode: protocol.ModeGenerate,
			job: `{
				"schema_version":"p4.7.5",
				"mode":"generate",
				"request_id":"p475-generate-preflight",
				"page_context":{"url":"https://example.invalid/orders","description":"orders page"},
				"recording_source":{
					"source_page_script_id":"ps-preflight",
					"action_trace":[{"type":"hover","target":{"text":"Menu"}}],
					"dom_snapshot":{"elements":[{"text":"Menu"}]},
					"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean","target_url":"https://example.invalid/orders"}
				},
				"limits":{"enable_llm":true},
				"llm_runtime_config":{"endpoint":"http://127.0.0.1:1/v1","model":"contract-model","secret_channel":{"kind":"env","name":"BROWSERWING_PLAYBOT_LLM_API_KEY"}}
			}`,
			wantCode: "blueprint_unsupported_action",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := filepath.Join(t.TempDir(), "job.json")
			if err := os.WriteFile(fixture, []byte(tc.job), 0o600); err != nil {
				t.Fatalf("write job fixture: %v", err)
			}
			result := run(context.Background(), tc.mode, fixture, newEventWriter(""))
			if result.Status != protocol.StatusFailed || result.Code != tc.wantCode {
				t.Fatalf("result = status %q code %q, want failed/%s", result.Status, result.Code, tc.wantCode)
			}
		})
	}
}

func TestP475OptimizeReturnsUnimplementedInsteadOfNoopProposal(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "optimize-job.json")
	job := `{
		"schema_version":"p4.7.5",
		"mode":"optimize",
		"request_id":"p475-optimize-unimplemented",
		"user_instruction":"add an assertion for the success toast",
		"existing_blueprint":{
			"title":"checkout",
			"description":"existing case",
			"steps":[{"action":"click","target":{"text":"Submit"}}]
		},
		"execution_report":{"status":"failed"}
	}`
	if err := os.WriteFile(fixture, []byte(job), 0o600); err != nil {
		t.Fatalf("write optimize fixture: %v", err)
	}

	result := run(context.Background(), protocol.ModeOptimize, fixture, newEventWriter(""))
	if result.Status != protocol.StatusFailed || result.Code != "playbot_optimize_not_implemented" {
		t.Fatalf("result = status %q code %q, want failed/playbot_optimize_not_implemented", result.Status, result.Code)
	}
	if len(result.RefinedBlueprint) != 0 {
		t.Fatalf("optimize should not return a no-op refined_blueprint: %+v", result.RefinedBlueprint)
	}
}

func TestP475GenerateUsesLLMSemanticPlanForBlueprint(t *testing.T) {
	t.Setenv("BROWSERWING_PLAYBOT_LLM_API_KEY", "sk-test-visible-output")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("LLM request path = %s, want /chat/completions", r.URL.Path)
		}
		content, err := json.Marshal(map[string]any{
			"visible_message": "模型生成了可执行计划",
			"candidate_steps": []map[string]any{{
				"action":         "click",
				"target_summary": "Submit",
				"reason":         "Use the model semantic plan",
			}},
			"semantic_plan": map[string]any{
				"title":       "Model generated title",
				"description": "Model generated description",
				"steps": []map[string]any{{
					"action":        "click",
					"target_hint":   map[string]string{"text": "Submit"},
					"intent_reason": "Model semantic step",
				}},
			},
		})
		if err != nil {
			t.Fatalf("marshal LLM content: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": string(content)},
			}},
		})
	}))
	defer server.Close()

	fixture := filepath.Join(t.TempDir(), "job.json")
	job := `{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"p475-llm-semantic-plan",
		"user_instruction":"Recorded fallback title",
		"page_context":{"url":"https://example.invalid/orders","description":"orders page"},
		"recording_source":{
			"source_page_script_id":"ps-llm-plan",
			"action_trace":[{"type":"click","target":{"text":"Submit"},"intent_reason":"Recorded click"}],
			"dom_snapshot":{"elements":[{"text":"Submit"}]},
			"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean","target_url":"https://example.invalid/orders"}
		},
		"limits":{"enable_llm":true},
		"llm_runtime_config":{"endpoint":"` + server.URL + `","model":"contract-model","secret_channel":{"kind":"env","name":"BROWSERWING_PLAYBOT_LLM_API_KEY"}}
	}`
	if err := os.WriteFile(fixture, []byte(job), 0o600); err != nil {
		t.Fatalf("write job fixture: %v", err)
	}

	result := run(context.Background(), protocol.ModeGenerate, fixture, newEventWriter(""))
	if result.Status != protocol.StatusSuccess {
		t.Fatalf("result = status %q code %q, want success", result.Status, result.Code)
	}
	if len(result.TestCases) != 1 {
		t.Fatalf("test cases = %#v, want one", result.TestCases)
	}
	blueprint := result.TestCases[0]
	if blueprint["title"] != "Model generated title" || blueprint["description"] != "Model generated description" {
		t.Fatalf("blueprint title/description = %#v, want model semantic plan fields", blueprint)
	}
	steps, _ := blueprint["steps"].([]any)
	if len(steps) != 1 {
		t.Fatalf("blueprint steps = %#v, want one model step", blueprint["steps"])
	}
	step, _ := steps[0].(map[string]any)
	if step["description"] != "Model semantic step" {
		t.Fatalf("step description = %v, want model semantic step", step["description"])
	}
}

func TestP475GenerateRejectsAmbiguousLLMSemanticPlanTarget(t *testing.T) {
	t.Setenv("BROWSERWING_PLAYBOT_LLM_API_KEY", "sk-test-visible-output")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, err := json.Marshal(map[string]any{
			"visible_message": "模型生成了存在歧义的计划",
			"semantic_plan": map[string]any{
				"title":       "Ambiguous target plan",
				"description": "Ambiguous target description",
				"steps": []map[string]any{{
					"action":        "click",
					"target_hint":   map[string]string{"text": "Submit"},
					"intent_reason": "The text appears more than once",
				}},
			},
		})
		if err != nil {
			t.Fatalf("marshal LLM content: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": string(content)},
			}},
		})
	}))
	defer server.Close()

	fixture := filepath.Join(t.TempDir(), "job.json")
	job := `{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"p475-llm-ambiguous-target",
		"page_context":{"url":"https://example.invalid/orders","description":"orders page"},
		"recording_source":{
			"source_page_script_id":"ps-llm-ambiguous",
			"action_trace":[
				{"type":"click","target":{"text":"Submit","recorded_selector":"button.primary"}},
				{"type":"click","target":{"text":"Submit","recorded_selector":"button.secondary"}}
			],
			"dom_snapshot":{"elements":[{"text":"Submit","recorded_selector":"button.primary"},{"text":"Submit","recorded_selector":"button.secondary"}]},
			"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean","target_url":"https://example.invalid/orders"}
		},
		"limits":{"enable_llm":true},
		"llm_runtime_config":{"endpoint":"` + server.URL + `","model":"contract-model","secret_channel":{"kind":"env","name":"BROWSERWING_PLAYBOT_LLM_API_KEY"}}
	}`
	if err := os.WriteFile(fixture, []byte(job), 0o600); err != nil {
		t.Fatalf("write job fixture: %v", err)
	}

	result := run(context.Background(), protocol.ModeGenerate, fixture, newEventWriter(""))
	if result.Status != protocol.StatusFailed || result.Code != "playbot_llm_semantic_plan_unapproved_target" {
		t.Fatalf("result = status %q code %q, want failed/playbot_llm_semantic_plan_unapproved_target", result.Status, result.Code)
	}
}

func TestP475GenerateRejectsConflictingStableAndWeakTargetHints(t *testing.T) {
	t.Setenv("BROWSERWING_PLAYBOT_LLM_API_KEY", "sk-test-visible-output")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, err := json.Marshal(map[string]any{
			"visible_message": "模型生成了冲突定位的计划",
			"semantic_plan": map[string]any{
				"title":       "Conflicting target plan",
				"description": "Conflicting target description",
				"steps": []map[string]any{{
					"action":        "click",
					"target_hint":   map[string]string{"ref_id": "recorded_action_999", "text": "Submit"},
					"intent_reason": "The weak text matches but the stable ref does not",
				}},
			},
		})
		if err != nil {
			t.Fatalf("marshal LLM content: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": string(content)},
			}},
		})
	}))
	defer server.Close()

	fixture := filepath.Join(t.TempDir(), "job.json")
	job := `{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"p475-llm-conflicting-target",
		"page_context":{"url":"https://example.invalid/orders","description":"orders page"},
		"recording_source":{
			"source_page_script_id":"ps-llm-conflicting-target",
			"action_trace":[{"type":"click","ref_id":"recorded_action_0","target":{"text":"Submit","recorded_selector":"button.primary"}}],
			"dom_snapshot":{"elements":[{"text":"Submit","recorded_selector":"button.primary"}]},
			"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean","target_url":"https://example.invalid/orders"}
		},
		"limits":{"enable_llm":true},
		"llm_runtime_config":{"endpoint":"` + server.URL + `","model":"contract-model","secret_channel":{"kind":"env","name":"BROWSERWING_PLAYBOT_LLM_API_KEY"}}
	}`
	if err := os.WriteFile(fixture, []byte(job), 0o600); err != nil {
		t.Fatalf("write job fixture: %v", err)
	}

	result := run(context.Background(), protocol.ModeGenerate, fixture, newEventWriter(""))
	if result.Status != protocol.StatusFailed || result.Code != "playbot_llm_semantic_plan_unapproved_target" {
		t.Fatalf("result = status %q code %q, want failed/playbot_llm_semantic_plan_unapproved_target", result.Status, result.Code)
	}
}

func TestP475GenerateRequiresLLMSemanticPlanWhenLLMEnabled(t *testing.T) {
	t.Setenv("BROWSERWING_PLAYBOT_LLM_API_KEY", "sk-test-visible-output")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, err := json.Marshal(map[string]any{
			"visible_message": "模型只返回展示说明",
		})
		if err != nil {
			t.Fatalf("marshal LLM content: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": string(content)},
			}},
		})
	}))
	defer server.Close()

	fixture := filepath.Join(t.TempDir(), "job.json")
	job := `{
		"schema_version":"p4.7.5",
		"mode":"generate",
		"request_id":"p475-llm-semantic-plan-required",
		"page_context":{"url":"https://example.invalid/orders","description":"orders page"},
		"recording_source":{
			"source_page_script_id":"ps-llm-plan-required",
			"action_trace":[{"type":"click","target":{"text":"Submit"}}],
			"dom_snapshot":{"elements":[{"text":"Submit"}]},
			"recording_meta":{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean","target_url":"https://example.invalid/orders"}
		},
		"limits":{"enable_llm":true},
		"llm_runtime_config":{"endpoint":"` + server.URL + `","model":"contract-model","secret_channel":{"kind":"env","name":"BROWSERWING_PLAYBOT_LLM_API_KEY"}}
	}`
	if err := os.WriteFile(fixture, []byte(job), 0o600); err != nil {
		t.Fatalf("write job fixture: %v", err)
	}

	result := run(context.Background(), protocol.ModeGenerate, fixture, newEventWriter(""))
	if result.Status != protocol.StatusFailed || result.Code != "playbot_llm_semantic_plan_required" {
		t.Fatalf("result = status %q code %q, want failed/playbot_llm_semantic_plan_required", result.Status, result.Code)
	}
}
