package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/browserwing/browserwing/models"
)

func TestPageRecordingDetailReturnsSanitizedLatestRecording(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	oldScript := env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
		ActionTrace: `{"actions":[{"type":"click","target":{"text":"Old"}}]}`,
		DOMSnapshot: `{"elements":[{"role":"button","text":"Old"}]}`,
		Meta:        p475RecordingMeta("business_flow", "clean"),
	})
	newScript := env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
		ActionTrace: `{"actions":[{"type":"input","target":{"text":"sk-payment-token-field"},"value":"sk-test-token-for-recording","cookies":[{"name":"sid","value":"cookie-value-recording-detail"}],"debug_path":"C:\\Users\\Administrator\\secret-profile"},{"type":"click","target":{"text":"Save","recorded_selector":"button.save"},"api_key":"test-api-key"}]}`,
		DOMSnapshot: `{"elements":[{"role":"textbox","text":"sk-payment-token-field"},{"role":"button","text":"Save"}],"localStorage":{"token":"storage-value-recording-detail"},"sessionStorage":{"trace":"session-value-recording-detail"},"profile_path":"C:\\Users\\Administrator\\profile"}`,
		Meta:        p475RecordingMeta("business_flow", "clean"),
	})
	env.db.Model(&oldScript).Updates(map[string]any{"created_at": time.Now().Add(-time.Hour), "updated_at": time.Now().Add(-time.Hour)})
	env.db.Model(&newScript).Updates(map[string]any{"created_at": time.Now(), "updated_at": time.Now()})

	res := env.getLatestPageRecording(t, project.ID, version.ID, page.ID)

	env.requireStatus(t, res, http.StatusOK)
	body := env.decodeObject(t, res)
	recording := body["recording"].(map[string]any)
	if uint(recording["id"].(float64)) != newScript.ID {
		t.Fatalf("recording id = %v, want latest script %d", recording["id"], newScript.ID)
	}
	responseJSON := res.Body.String()
	requireP475JSONContains(t, responseJSON, "sk-test-token-for-recording")
	requireP475JSONOmits(t, responseJSON, "cookie-value-recording-detail", "storage-value-recording-detail", "session-value-recording-detail", "test-api-key", "C:\\Users\\Administrator")
	diagnostics := recording["diagnostics"].(map[string]any)
	if diagnostics["action_count"] != float64(2) {
		t.Fatalf("action_count = %v, want 2; body: %s", diagnostics["action_count"], responseJSON)
	}
	if diagnostics["snapshot_element_count"] != float64(2) {
		t.Fatalf("snapshot_element_count = %v, want 2; body: %s", diagnostics["snapshot_element_count"], responseJSON)
	}
}

func TestPageRecordingDetailReportsQualityDiagnostics(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
		ActionTrace: `{"actions":[{"type":"click"}]}`,
		DOMSnapshot: `{}`,
		Meta:        p475RecordingMeta("business_flow", "clean"),
	})

	res := env.getLatestPageRecording(t, project.ID, version.ID, page.ID)

	env.requireStatus(t, res, http.StatusOK)
	body := env.decodeObject(t, res)
	recording := body["recording"].(map[string]any)
	diagnostics := recording["diagnostics"].(map[string]any)
	codes := fmt.Sprint(diagnostics["quality_codes"])
	if !strings.Contains(codes, "recording_snapshot_unusable") || !strings.Contains(codes, "recording_action_missing_target") {
		t.Fatalf("quality_codes = %v, want snapshot and target diagnostics; body: %s", diagnostics["quality_codes"], res.Body.String())
	}
}

func TestPageRecordingDetailAcceptsTopLevelSelectorActions(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
		ActionTrace: `{"actions":[{"type":"click","selector":"#create-order"}]}`,
		DOMSnapshot: `{"elements":[{"role":"button","text":"Create order"}]}`,
		Meta:        p475RecordingMeta("business_flow", "clean"),
	})

	res := env.getLatestPageRecording(t, project.ID, version.ID, page.ID)

	env.requireStatus(t, res, http.StatusOK)
	body := env.decodeObject(t, res)
	recording := body["recording"].(map[string]any)
	diagnostics := recording["diagnostics"].(map[string]any)
	codes := fmt.Sprint(diagnostics["quality_codes"])
	if strings.Contains(codes, "recording_action_missing_target") {
		t.Fatalf("quality_codes = %v, top-level selector action should not be reported as missing target; body: %s", diagnostics["quality_codes"], res.Body.String())
	}
}

func TestPageRecordingDetailAcceptsNestedCSSAndXPathTargets(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedP475PageScript(t, page.ID, p475PageScriptSeed{
		ActionTrace: `{"actions":[{"type":"click","target":{"xpath":"//button[@id='create-order']"}},{"type":"fill","target":{"css":"input[name='orderName']"},"value":"demo"}]}`,
		DOMSnapshot: `{"elements":[{"role":"button","text":"Create order"},{"role":"textbox","text":"Order name"}]}`,
		Meta:        p475RecordingMeta("business_flow", "clean"),
	})

	res := env.getLatestPageRecording(t, project.ID, version.ID, page.ID)

	env.requireStatus(t, res, http.StatusOK)
	body := env.decodeObject(t, res)
	recording := body["recording"].(map[string]any)
	diagnostics := recording["diagnostics"].(map[string]any)
	codes := fmt.Sprint(diagnostics["quality_codes"])
	if strings.Contains(codes, "recording_action_missing_target") {
		t.Fatalf("quality_codes = %v, nested css/xpath targets should not be reported as missing target; body: %s", diagnostics["quality_codes"], res.Body.String())
	}
}

func TestPageRecordingDetailReturnsNotFoundWithoutRecording(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)

	res := env.getLatestPageRecording(t, project.ID, version.ID, page.ID)

	env.requireStatus(t, res, http.StatusNotFound)
	body := env.decodeObject(t, res)
	if body["code"] != "page_recording_not_found" {
		t.Fatalf("code = %v, want page_recording_not_found; body: %s", body["code"], res.Body.String())
	}
}

func TestPageRecordingDetailRejectsHierarchyMismatch(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, _, page := env.seedProjectVersionPage(t)
	_, otherVersion, otherPage := env.seedProjectVersionPage(t)
	env.seedP475PageScript(t, page.ID, p475PageScriptSeed{})
	env.seedP475PageScript(t, otherPage.ID, p475PageScriptSeed{})

	res := env.getLatestPageRecording(t, project.ID, otherVersion.ID, otherPage.ID)

	env.requireStatus(t, res, http.StatusNotFound)
	body := env.decodeObject(t, res)
	if body["code"] == "page_recording_not_found" {
		t.Fatalf("hierarchy mismatch returned recording-missing code instead of page scope failure; body: %s", res.Body.String())
	}
}

func (e *generateContractEnv) getLatestPageRecording(t *testing.T, projectID, versionID, pageID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/recordings/latest", projectID, versionID, pageID)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	res := httptest.NewRecorder()
	e.router.ServeHTTP(res, req)
	return res
}

func TestPageRecordingDetailHandlesInvalidStoredJSON(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	script := models.PageScript{
		PageID:            page.ID,
		Name:              "invalid recording",
		ActionTrace:       "{not-json",
		DOMSnapshot:       "{not-json",
		RecordingMetaJSON: "{not-json",
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}
	if err := env.db.Create(&script).Error; err != nil {
		t.Fatalf("seed invalid PageScript: %v", err)
	}

	res := env.getLatestPageRecording(t, project.ID, version.ID, page.ID)

	env.requireStatus(t, res, http.StatusOK)
	body := env.decodeObject(t, res)
	recording := body["recording"].(map[string]any)
	diagnostics := recording["diagnostics"].(map[string]any)
	parseErrors := fmt.Sprint(diagnostics["parse_errors"])
	if !strings.Contains(parseErrors, "recording_action_trace_invalid") ||
		!strings.Contains(parseErrors, "recording_dom_snapshot_invalid") ||
		!strings.Contains(parseErrors, "recording_meta_invalid") {
		t.Fatalf("parse_errors = %v, want all stored JSON parse failures; body: %s", diagnostics["parse_errors"], res.Body.String())
	}
}
