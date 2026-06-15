package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/browserwing/browserwing/models"
)

/*
P4.7.5 contract basis:
- docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md sections 5, 7, 8, 9, 10,
  11, and 14 require backend generation to call the independent Go agent,
  select PageScript/protected RecordingSession as the fact source, reject
  RecordingArtifact-only generation, and run strict final Blueprint validation
  before preview/append/replace can affect assets.
- docs/P4_7_5_PLAYBOT_GO_AGENT_IMPLEMENTATION_PLAN.md sections 4.3 and 4.4
  assign these backend API red tests to backend/api.

Current expected red state:
- Handler has no SetPlaybotAgentClientForTest seam yet and production generation
  still calls services/playbot Python CLI with a looser save validator.

Targeted verification:
- cd backend
- go test ./api -run TestP475 -count=1
*/

func TestP475GenerateUsesGoPlaybotAgentAdapter(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	cookieSecret := "cookie-value-p475-generate"
	storageSecret := "storage-value-p475-generate"
	env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
		ActionTrace: fmt.Sprintf(`{"actions":[{"type":"click","target":{"text":"Save"},"cookies":[{"name":"sid","value":%q}]}]}`, cookieSecret),
		DOMSnapshot: fmt.Sprintf(`{"elements":[{"role":"button","text":"Save"}],"localStorage":{"token":%q}}`, storageSecret),
		Meta:        p475RecordingMeta("business_flow", "clean"),
	})
	agent := newP475FakePlaybotAgent(t, p475AgentGenerateSuccess(p475ValidGeneratedBlueprint("generated through go agent")))
	env.installP475FakePlaybotAgent(t, agent)

	res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
		"mode": "append",
	})

	env.requireStatus(t, res, http.StatusOK)
	if agent.CallCount() != 1 {
		t.Fatalf("Go Playbot agent calls = %d, want 1", agent.CallCount())
	}
	env.requirePlaybotCalls(t, 0)
	jobJSON := agent.LastJobJSON(t)
	requireP475JSONContains(t, jobJSON, `"mode":"generate"`)
	requireP475JSONOmits(t, jobJSON, "test-api-key", cookieSecret, storageSecret, "api_key", "cookies", "localStorage", "sessionStorage")
	env.requireTestCaseCount(t, page.ID, 1)
}

func TestP475GenerationSourcePrefersPageScriptAndRequiresProtectedStoppedSession(t *testing.T) {
	t.Run("PageScript wins over stopped session", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
			ActionTrace: `{"actions":[{"type":"click","target":{"text":"page-script-save"}}]}`,
			DOMSnapshot: `{"elements":[{"role":"button","text":"page-script-save"}]}`,
			Meta:        p475RecordingMeta("business_flow", "clean"),
		})
		env.seedP475RecordingSession(t, project.ID, version.ID, page.ID, "stopped", `[{ "type":"click", "target":{"text":"stopped-session-delete"} }]`)
		agent := newP475FakePlaybotAgent(t, p475AgentGenerateSuccess(p475ValidGeneratedBlueprint("page script source wins")))
		env.installP475FakePlaybotAgent(t, agent)

		res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "preview"})

		env.requireStatus(t, res, http.StatusOK)
		jobJSON := agent.LastJobJSON(t)
		requireP475JSONContains(t, jobJSON, "page-script-save")
		requireP475JSONOmits(t, jobJSON, "stopped-session-delete")
	})

	t.Run("stopped session cannot be used without PageScript or protected transaction", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		env.seedP475RecordingSession(t, project.ID, version.ID, page.ID, "stopped", `[{ "type":"click", "target":{"text":"Save"} }]`)
		agent := newP475FakePlaybotAgent(t, p475AgentGenerateSuccess(p475ValidGeneratedBlueprint("must not save from bare stopped session")))
		env.installP475FakePlaybotAgent(t, agent)

		res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "append"})

		env.requireStatus(t, res, http.StatusBadRequest)
		if agent.CallCount() != 0 {
			t.Fatalf("Go Playbot agent calls = %d, want 0 until stopped session is saved/protected", agent.CallCount())
		}
		env.requireTestCaseCount(t, page.ID, 0)
	})
}

func TestP475RecordingArtifactCannotSatisfyGenerationSource(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := env.seedP475RecordingSession(t, project.ID, version.ID, page.ID, "stopped", "")
	if err := env.db.Create(&models.RecordingArtifact{
		ProjectID:          project.ID,
		VersionID:          version.ID,
		PageID:             page.ID,
		RecordingSessionID: session.ID,
		ArtifactType:       "screenshot",
		StorageBackend:     "local",
		StoragePath:        "recordings/p475/screenshot.png",
		FileName:           "screenshot.png",
		MimeType:           "image/png",
		SizeBytes:          1234,
		CreatedAt:          time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed RecordingArtifact: %v", err)
	}
	agent := newP475FakePlaybotAgent(t, p475AgentGenerateSuccess(p475ValidGeneratedBlueprint("artifact must not be enough")))
	env.installP475FakePlaybotAgent(t, agent)

	res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "append"})

	env.requireStatus(t, res, http.StatusBadRequest)
	if agent.CallCount() != 0 {
		t.Fatalf("Go Playbot agent calls = %d, want 0 for RecordingArtifact-only source", agent.CallCount())
	}
	env.requireTestCaseCount(t, page.ID, 0)
}

func TestP475RecordingQualityErrorProtectsExistingAssets(t *testing.T) {
	for _, code := range []string{
		"recording_action_missing_target",
		"recording_action_missing_value",
		"recording_navigation_missing_url",
		"recording_snapshot_unusable",
		"recording_meta_invalid",
		"recording_auth_context_conflict",
	} {
		t.Run(code, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
				ActionTrace: `{"actions":[{"type":"click","target":{"text":"Save"}}]}`,
				DOMSnapshot: `{"elements":[{"role":"button","text":"Save"}]}`,
				Meta:        p475RecordingMeta("business_flow", "clean"),
			})
			oldCase := env.seedTestCase(t, page.ID, "old case before recording quality failure")
			before := env.snapshotTestCase(t, oldCase.ID)
			agent := newP475FakePlaybotAgent(t, p475AgentQualityFailure(code))
			env.installP475FakePlaybotAgent(t, agent)

			res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "replace"})

			env.requireStatus(t, res, http.StatusBadRequest)
			if agent.CallCount() != 1 {
				t.Fatalf("Go Playbot agent calls = %d, want 1", agent.CallCount())
			}
			env.requireTestCaseCount(t, page.ID, 1)
			env.requireTestCaseUnchanged(t, before, code+" must not replace or mutate old TestCase")
			requireP475JSONContains(t, res.Body.String(), code)
			requireP475JSONOmits(t, res.Body.String(), "test-api-key", `C:\Users\`)
		})
	}
}

func TestP475GeneratedBlueprintMustPassStrictFinalFieldValidation(t *testing.T) {
	cases := []struct {
		name      string
		blueprint map[string]any
	}{
		{
			name: "navigate value without url",
			blueprint: p475GeneratedBlueprint("navigate value only", []map[string]any{{
				"action": "navigate",
				"value":  "/orders",
			}}),
		},
		{
			name: "target hint without final target",
			blueprint: p475GeneratedBlueprint("target hint only", []map[string]any{{
				"action":      "click",
				"target_hint": map[string]any{"text": "Save"},
			}}),
		},
		{
			name: "unsupported action",
			blueprint: p475GeneratedBlueprint("unsupported action", []map[string]any{{
				"action": "hover",
				"target": map[string]any{"text": "Menu"},
			}}),
		},
		{
			name: "target exists but runner cannot normalize it",
			blueprint: p475GeneratedBlueprint("target role only", []map[string]any{{
				"action": "click",
				"target": map[string]any{"role": "button"},
			}}),
		},
		{
			name:      "empty steps",
			blueprint: p475GeneratedBlueprint("empty steps", []map[string]any{}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
				ActionTrace: `{"actions":[{"type":"click","target":{"text":"Save"}}]}`,
				DOMSnapshot: `{"elements":[{"role":"button","text":"Save"}]}`,
				Meta:        p475RecordingMeta("business_flow", "clean"),
			})
			agent := newP475FakePlaybotAgent(t, p475AgentGenerateSuccess(tc.blueprint))
			env.installP475FakePlaybotAgent(t, agent)

			res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "preview"})

			env.requireStatus(t, res, http.StatusBadRequest)
			env.requireTestCaseCount(t, page.ID, 0)
		})
	}
}

func TestP475PreviewAppendAndReplaceProtectAssetsOnValidationFailure(t *testing.T) {
	invalidCases := []struct {
		name      string
		blueprint map[string]any
	}{
		{
			name: "target hint only",
			blueprint: p475GeneratedBlueprint("invalid target hint only", []map[string]any{{
				"action":      "click",
				"target_hint": map[string]any{"text": "Save"},
			}}),
		},
		{
			name: "runner normalization cannot consume final target",
			blueprint: p475GeneratedBlueprint("invalid target role only", []map[string]any{{
				"action": "click",
				"target": map[string]any{"role": "button"},
			}}),
		},
	}
	for _, invalid := range invalidCases {
		for _, mode := range []string{"preview", "append", "replace"} {
			t.Run(invalid.name+"/"+mode, func(t *testing.T) {
				env := newGenerateContractEnv(t)
				project, version, page := env.seedProjectVersionPage(t)
				env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
					ActionTrace: `{"actions":[{"type":"click","target":{"text":"Save"}}]}`,
					DOMSnapshot: `{"elements":[{"role":"button","text":"Save"}]}`,
					Meta:        p475RecordingMeta("business_flow", "clean"),
				})
				oldCase := env.seedTestCase(t, page.ID, "old case before invalid generated blueprint")
				before := env.snapshotTestCase(t, oldCase.ID)
				agent := newP475FakePlaybotAgent(t, p475AgentGenerateSuccess(invalid.blueprint))
				env.installP475FakePlaybotAgent(t, agent)

				res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": mode})

				env.requireStatus(t, res, http.StatusBadRequest)
				env.requireTestCaseCount(t, page.ID, 1)
				env.requireTestCaseUnchanged(t, before, mode+" must not pollute assets when strict validation or runner normalization fails")
			})
		}
	}
}

func TestP475ContextRequiredRetriesOnlyWithBackendApprovedContext(t *testing.T) {
	t.Run("approved context is provided once then rerun succeeds", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		approvedContextCookieSecret := "cookie-value-p475-approved-context"
		approvedContextStorageSecret := "storage-value-p475-approved-context"
		approvedContextAPIKey := "sk-p475-approved-context"
		approvedContextLocalPath := `C:\Users\someone\AppData\Local\browserwing\approved-context`
		env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
			ActionTrace: fmt.Sprintf(
				`{"actions":[{"type":"click","target":{"text":"Save"},"ref_id":"recorded_action_0","cookies":[{"name":"sid","value":%q}],"api_key":%q}]}`,
				approvedContextCookieSecret,
				approvedContextAPIKey,
			),
			DOMSnapshot: fmt.Sprintf(
				`{"elements":[{"ref_id":"recorded_action_0","role":"button","text":"Save","recorded_selector":"button.save","dataset":{"apiKey":%q}}],"localStorage":{"token":%q},"sessionStorage":{"trace":%q},"profile_path":%q}`,
				approvedContextAPIKey,
				approvedContextStorageSecret,
				approvedContextStorageSecret,
				approvedContextLocalPath,
			),
			Meta: p475RecordingMeta("business_flow", "clean"),
		})
		oldCase := env.seedTestCase(t, page.ID, "old case before context retry")
		before := env.snapshotTestCase(t, oldCase.ID)
		agent := newP475FakePlaybotAgent(t,
			p475AgentContextRequired("dom_snapshot_chunk_required", true, map[string]any{
				"kind":   "dom_snapshot",
				"scope":  "recorded_action_0",
				"reason": "need locator candidates near recorded action",
			}),
			p475AgentGenerateSuccess(p475ValidGeneratedBlueprint("generated after approved context")),
		)
		env.installP475FakePlaybotAgent(t, agent)

		res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "append"})

		env.requireStatus(t, res, http.StatusOK)
		if agent.CallCount() != 2 {
			t.Fatalf("Go Playbot agent calls = %d, want exactly 2 for one deterministic context retry", agent.CallCount())
		}
		firstJob := agent.JobJSONAt(t, 0)
		secondJob := agent.JobJSONAt(t, 1)
		if secondJob == firstJob {
			t.Fatalf("second Playbot job must include backend-approved context changes instead of resending the same job: %s", secondJob)
		}
		requireP475JSONOmits(t, firstJob, "backend_approved_context")
		requireP475ApprovedContext(t, secondJob, "dom_snapshot", "recorded_action_0", "page_script.dom_snapshot",
			approvedContextCookieSecret,
			approvedContextStorageSecret,
			approvedContextAPIKey,
			approvedContextLocalPath,
		)
		requireP475JSONOmits(t, secondJob,
			"test-api-key",
			"cookie",
			"localStorage",
			"sessionStorage",
			"api_key",
			approvedContextCookieSecret,
			approvedContextStorageSecret,
			approvedContextAPIKey,
			approvedContextLocalPath,
		)
		env.requireTestCaseCount(t, page.ID, 2)
		env.requireTestCaseUnchanged(t, before, "append after approved context retry must not mutate old TestCase")
	})

	t.Run("unapproved context request is rejected without asset pollution", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		env.seedP475PageScript(t, page.ID, p475PageScriptSeed{})
		oldCase := env.seedTestCase(t, page.ID, "old case before unapproved context request")
		before := env.snapshotTestCase(t, oldCase.ID)
		agent := newP475FakePlaybotAgent(t, p475AgentContextRequired("raw_auth_storage_required", true, map[string]any{
			"kind":   "auth_storage",
			"scope":  "cookie_value",
			"reason": "agent must not receive raw cookie or storage values",
		}))
		env.installP475FakePlaybotAgent(t, agent)

		res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "replace"})

		env.requireStatus(t, res, http.StatusBadRequest)
		if agent.CallCount() != 1 {
			t.Fatalf("Go Playbot agent calls = %d, want 1 for rejected unapproved context", agent.CallCount())
		}
		env.requireTestCaseCount(t, page.ID, 1)
		env.requireTestCaseUnchanged(t, before, "unapproved context request must not replace or mutate old TestCase")
		requireP475JSONContains(t, res.Body.String(), "raw_auth_storage_required")
		requireP475JSONOmits(t, res.Body.String(), "test-api-key", "cookie_value")
	})

	t.Run("retryable context cannot loop indefinitely", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
			ActionTrace: `{"actions":[{"type":"click","target":{"text":"Save"},"ref_id":"recorded_action_0"}]}`,
			DOMSnapshot: `{"elements":[{"ref_id":"recorded_action_0","role":"button","text":"Save"}]}`,
			Meta:        p475RecordingMeta("business_flow", "clean"),
		})
		oldCase := env.seedTestCase(t, page.ID, "old case before repeated context request")
		before := env.snapshotTestCase(t, oldCase.ID)
		request := map[string]any{
			"kind":   "dom_snapshot",
			"scope":  "recorded_action_0",
			"reason": "same context requested again",
		}
		agent := newP475FakePlaybotAgent(t,
			p475AgentContextRequired("dom_snapshot_chunk_required", true, request),
			p475AgentContextRequired("dom_snapshot_chunk_required", true, request),
			p475AgentGenerateSuccess(p475ValidGeneratedBlueprint("must not be reached after repeated context request")),
		)
		env.installP475FakePlaybotAgent(t, agent)

		res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "replace"})

		env.requireStatus(t, res, http.StatusBadRequest)
		if agent.CallCount() != 2 {
			t.Fatalf("Go Playbot agent calls = %d, want exactly 2 to prove finite retry", agent.CallCount())
		}
		env.requireTestCaseCount(t, page.ID, 1)
		env.requireTestCaseUnchanged(t, before, "repeated context_required must not replace or mutate old TestCase")
	})
}

type p475FakePlaybotAgent struct {
	t       *testing.T
	results []map[string]any
	jobs    []map[string]any
}

func newP475FakePlaybotAgent(t *testing.T, results ...map[string]any) *p475FakePlaybotAgent {
	t.Helper()
	return &p475FakePlaybotAgent{t: t, results: results}
}

func (a *p475FakePlaybotAgent) Run(_ context.Context, job map[string]any) (map[string]any, error) {
	a.jobs = append(a.jobs, p475CloneMap(a.t, job))
	if len(a.results) == 0 {
		a.t.Fatalf("fake Go Playbot agent was called without a queued result; job: %v", job)
	}
	result := a.results[0]
	a.results = a.results[1:]
	return p475CloneMap(a.t, result), nil
}

func (a *p475FakePlaybotAgent) CallCount() int {
	return len(a.jobs)
}

func (a *p475FakePlaybotAgent) LastJobJSON(t *testing.T) string {
	t.Helper()
	if len(a.jobs) == 0 {
		t.Fatal("fake Go Playbot agent was not called")
	}
	return a.JobJSONAt(t, len(a.jobs)-1)
}

func (a *p475FakePlaybotAgent) JobJSONAt(t *testing.T, index int) string {
	t.Helper()
	if index < 0 || index >= len(a.jobs) {
		t.Fatalf("fake Go Playbot agent job index %d out of range; call count = %d", index, len(a.jobs))
	}
	data, err := json.Marshal(a.jobs[index])
	if err != nil {
		t.Fatalf("marshal fake agent job: %v", err)
	}
	return string(data)
}

func (e *generateContractEnv) installP475FakePlaybotAgent(t *testing.T, agent *p475FakePlaybotAgent) {
	t.Helper()
	method := reflect.ValueOf(e.handler).MethodByName("SetPlaybotAgentClientForTest")
	if !method.IsValid() {
		t.Fatalf("SetPlaybotAgentClientForTest is not available on production Handler; P4.7.5 requires a Go playbot agent adapter seam instead of Python playbot-engine")
	}
	if method.Type().NumIn() != 1 {
		t.Fatalf("SetPlaybotAgentClientForTest input count = %d, want 1", method.Type().NumIn())
	}
	arg := reflect.ValueOf(agent)
	want := method.Type().In(0)
	if !arg.Type().AssignableTo(want) {
		if arg.Type().ConvertibleTo(want) {
			arg = arg.Convert(want)
		} else {
			t.Fatalf("fake Go Playbot agent type %s is not assignable to SetPlaybotAgentClientForTest(%s)", arg.Type(), want)
		}
	}
	method.Call([]reflect.Value{arg})
}

type p475PageScriptSeed struct {
	ActionTrace string
	DOMSnapshot string
	Meta        string
}

func (e *generateContractEnv) seedP475PageScript(t *testing.T, pageID uint, seed p475PageScriptSeed) models.PageScript {
	t.Helper()
	script := models.PageScript{
		PageID:            pageID,
		Name:              "p4.7.5 recorded main flow",
		ActionTrace:       firstP475NonEmpty(seed.ActionTrace, `{"actions":[{"type":"click","target":{"text":"Save"}}]}`),
		DOMSnapshot:       firstP475NonEmpty(seed.DOMSnapshot, `{"elements":[{"role":"button","text":"Save"}]}`),
		RecordingMetaJSON: firstP475NonEmpty(seed.Meta, p475RecordingMeta("business_flow", "clean")),
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
	if err := e.db.Create(&script).Error; err != nil {
		t.Fatalf("seed P4.7.5 PageScript: %v", err)
	}
	return script
}

func (e *generateContractEnv) seedP475RecordingSession(t *testing.T, projectID, versionID, pageID uint, status string, actionsJSON string) models.RecordingSession {
	t.Helper()
	now := time.Now().UTC()
	session := models.RecordingSession{
		ProjectID:         projectID,
		VersionID:         versionID,
		PageID:            pageID,
		RecordingKind:     "business_flow",
		AuthContext:       "clean",
		TargetURL:         "https://example.invalid/orders",
		Status:            status,
		ActionsJSON:       firstP475NonEmpty(actionsJSON, `[]`),
		ActionCount:       1,
		DOMSnapshot:       `{"elements":[{"role":"button","text":"Save"}]}`,
		RecordingMetaJSON: p475RecordingMeta("business_flow", "clean"),
		StartedAt:         now.Add(-time.Minute),
		LastSyncedAt:      now.Add(-30 * time.Second),
		StoppedAt:         now,
		CreatedAt:         now.Add(-time.Minute),
		UpdatedAt:         now,
	}
	if status == "saved" {
		session.SavedAt = now
	}
	if err := e.db.Create(&session).Error; err != nil {
		t.Fatalf("seed P4.7.5 RecordingSession: %v", err)
	}
	return session
}

func p475AgentGenerateSuccess(blueprints ...map[string]any) map[string]any {
	cases := make([]any, 0, len(blueprints))
	for _, blueprint := range blueprints {
		cases = append(cases, blueprint)
	}
	return map[string]any{
		"schema_version": "p4.7.5",
		"status":         "success",
		"code":           "",
		"test_cases":     cases,
		"context_trace":  p475ContextTrace(),
	}
}

func p475AgentQualityFailure(code string) map[string]any {
	return map[string]any{
		"schema_version": "p4.7.5",
		"status":         "failed",
		"code":           code,
		"quality_errors": []map[string]any{{
			"code":      code,
			"message":   "recording quality is insufficient",
			"retryable": false,
		}},
		"context_trace": p475ContextTrace(),
	}
}

func p475AgentContextRequired(code string, retryable bool, requested ...map[string]any) map[string]any {
	items := make([]any, 0, len(requested))
	for _, item := range requested {
		items = append(items, item)
	}
	return map[string]any{
		"schema_version":    "p4.7.5",
		"status":            "context_required",
		"code":              code,
		"retryable":         retryable,
		"requested_context": items,
		"context_trace":     p475ContextTrace(),
	}
}

func p475ContextTrace() map[string]any {
	return map[string]any{
		"source_page_script_id": "ps_123",
		"source_hash":           "sha256:p475-contract",
		"used_fields":           []string{"action_trace", "dom_snapshot", "recording_meta"},
		"truncated":             []string{},
	}
}

func p475ValidGeneratedBlueprint(title string) map[string]any {
	return p475GeneratedBlueprint(title, []map[string]any{
		{"action": "navigate", "url": "/orders"},
		{"action": "click", "target": map[string]any{"role": "button", "text": "Save", "recorded_selector": "button.save"}},
	})
}

func p475GeneratedBlueprint(title string, steps []map[string]any) map[string]any {
	typedSteps := make([]any, 0, len(steps))
	for _, step := range steps {
		typedSteps = append(typedSteps, step)
	}
	return map[string]any{
		"title":        title,
		"description":  "P4.7.5 generated blueprint contract",
		"steps":        typedSteps,
		"auth_context": "clean",
	}
}

func p475RecordingMeta(kind, authContext string) string {
	data, err := json.Marshal(map[string]any{
		"schema_version":  1,
		"recording_kind":  kind,
		"auth_context":    authContext,
		"target_url":      "https://example.invalid/orders",
		"captured_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"session_version": "p4.7.5-contract",
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func p475CloneMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal map clone: %v", err)
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		t.Fatalf("decode map clone: %v", err)
	}
	return cloned
}

func requireP475JSONContains(t *testing.T, text string, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("JSON does not contain %q: %s", want, text)
	}
}

func requireP475ApprovedContext(t *testing.T, jobJSON string, wantKind string, wantScope string, wantSource string, forbiddenPayloadTokens ...string) {
	t.Helper()
	var job map[string]any
	if err := json.Unmarshal([]byte(jobJSON), &job); err != nil {
		t.Fatalf("decode Playbot job JSON: %v; job: %s", err, jobJSON)
	}
	raw, ok := job["backend_approved_context"]
	if !ok {
		t.Fatalf("Playbot retry job missing backend_approved_context: %s", jobJSON)
	}
	contexts, ok := raw.([]any)
	if !ok {
		t.Fatalf("backend_approved_context has type %T, want JSON array; job: %s", raw, jobJSON)
	}
	for _, item := range contexts {
		ctx, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("backend_approved_context item has type %T, want object; job: %s", item, jobJSON)
		}
		if ctx["kind"] == wantKind && ctx["scope"] == wantScope {
			if ctx["source"] != wantSource {
				t.Fatalf("backend_approved_context source = %#v, want %q; job: %s", ctx["source"], wantSource, jobJSON)
			}
			payload, ok := ctx["payload"]
			if !ok || payload == nil {
				t.Fatalf("backend_approved_context missing payload for kind=%q scope=%q; job: %s", wantKind, wantScope, jobJSON)
			}
			payloadJSON, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal backend_approved_context payload: %v; job: %s", err, jobJSON)
			}
			requireP475JSONOmits(t, string(payloadJSON), forbiddenPayloadTokens...)
			switch typed := payload.(type) {
			case map[string]any:
				if len(typed) == 0 {
					t.Fatalf("backend_approved_context payload is empty for kind=%q scope=%q; job: %s", wantKind, wantScope, jobJSON)
				}
			case []any:
				if len(typed) == 0 {
					t.Fatalf("backend_approved_context payload is empty for kind=%q scope=%q; job: %s", wantKind, wantScope, jobJSON)
				}
			case string:
				if strings.TrimSpace(typed) == "" {
					t.Fatalf("backend_approved_context payload is empty for kind=%q scope=%q; job: %s", wantKind, wantScope, jobJSON)
				}
			}
			return
		}
	}
	t.Fatalf("backend_approved_context missing kind=%q scope=%q; job: %s", wantKind, wantScope, jobJSON)
}

func requireP475JSONOmits(t *testing.T, text string, forbidden ...string) {
	t.Helper()
	for _, token := range forbidden {
		if strings.TrimSpace(token) == "" {
			continue
		}
		if strings.Contains(text, token) {
			t.Fatalf("JSON leaked forbidden token %q: %s", token, text)
		}
	}
}

func firstP475NonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
