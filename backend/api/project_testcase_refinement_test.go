package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/services/playbotagent"
)

func TestRefineTestCaseRequiresHierarchyAndPrompt(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	siblingPage := env.seedPageInVersion(t, version.ID, "refine sibling page")
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "refine target", Status: "active"})
	env.setPlaybotStdout(t, validPlaybotRefineOutput("refined hierarchy control"))

	control := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
		"prompt": "add password empty validation",
	})
	env.requireStatus(t, control, http.StatusOK)
	env.requirePlaybotCalls(t, 1)
	env.requireRefinementCount(t, testCase.ID, 1)

	cases := []struct {
		name       string
		projectID  uint
		versionID  uint
		pageID     uint
		testCaseID uint
	}{
		{"wrong project", otherProject.ID, version.ID, page.ID, testCase.ID},
		{"wrong version", project.ID, otherVersion.ID, page.ID, testCase.ID},
		{"page belongs to another version", project.ID, version.ID, foreignPage.ID, testCase.ID},
		{"testcase belongs to sibling page", project.ID, version.ID, siblingPage.ID, testCase.ID},
		{"missing testcase", project.ID, version.ID, page.ID, testCase.ID + 9999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beforeCalls := env.playbotCalls(t)
			res := env.postRefineTestCase(t, tc.projectID, tc.versionID, tc.pageID, tc.testCaseID, map[string]any{
				"prompt": "must not call playbot on hierarchy mismatch",
			})
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
			if got := env.playbotCalls(t); got != beforeCalls {
				t.Fatalf("Playbot calls = %d, want unchanged at %d", got, beforeCalls)
			}
			env.requireRefinementCount(t, testCase.ID, 1)
		})
	}

	t.Run("empty prompt", func(t *testing.T) {
		beforeCalls := env.playbotCalls(t)
		res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
			"prompt": "   ",
		})
		env.requireStatus(t, res, http.StatusBadRequest)
		env.requireJSONError(t, res)
		if got := env.playbotCalls(t); got != beforeCalls {
			t.Fatalf("Playbot calls = %d, want unchanged at %d", got, beforeCalls)
		}
		env.requireRefinementCount(t, testCase.ID, 1)
	})
}

func TestRefineTestCaseCreatesProposedSuggestionWithoutMutatingTestCase(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:         "saved login case",
		Description:   "original description",
		Status:        "active",
		ScriptContent: "script must not change",
	})
	before := env.snapshotTestCase(t, testCase.ID)
	env.setPlaybotStdout(t, validPlaybotRefineOutput("password empty validation"))

	res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
		"prompt": "add password empty validation",
	})

	env.requireStatus(t, res, http.StatusOK)
	detail := env.decodeRefinementDetail(t, res)
	if detail["status"] != "proposed" {
		t.Fatalf("status = %v, want proposed", detail["status"])
	}
	if strings.TrimSpace(fmt.Sprint(detail["summary"])) == "" {
		t.Fatalf("summary is empty: %v", detail)
	}
	if _, ok := detail["original_blueprint"].(map[string]any); !ok {
		t.Fatalf("original_blueprint = %T, want object", detail["original_blueprint"])
	}
	refined, ok := detail["refined_blueprint"].(map[string]any)
	if !ok {
		t.Fatalf("refined_blueprint = %T, want object", detail["refined_blueprint"])
	}
	if refined["title"] != "password empty validation" {
		t.Fatalf("refined title = %v, want password empty validation", refined["title"])
	}
	env.requireTestCaseUnchanged(t, before, "refine must save suggestion without mutating TestCase")
	env.requireRefinementCount(t, testCase.ID, 1)
	env.requirePlaybotCalls(t, 1)
}

func TestRefineTestCaseAllowsMissingMainFlowAndRequiresOwnedExecutionContext(t *testing.T) {
	env := newGenerateContractEnv(t)
	env.installRecordingFakePlaybotCommand(t)
	project, version, page := env.seedProjectVersionPage(t)
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "manual case without recording", Status: "active"})
	otherCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "same page other case", Status: "active"})
	execution := env.seedTestExecution(t, testCase.ID, testExecutionSeed{Status: "failed"})
	otherExecution := env.seedTestExecution(t, otherCase.ID, testExecutionSeed{Status: "failed"})
	env.setPlaybotStdout(t, validPlaybotRefineOutput("refined with execution report"))

	res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
		"prompt":       "fix based on failed report",
		"execution_id": execution.ID,
	})

	env.requireStatus(t, res, http.StatusOK)
	env.requireRefinementCount(t, testCase.ID, 1)
	env.requirePlaybotCalls(t, 1)
	input := env.readRecordedPlaybotInput(t)
	warnings := requireObjectArrayField(t, input, "context_warnings")
	if len(warnings) == 0 {
		t.Fatalf("context_warnings = %v, want warning for missing PageScript", input["context_warnings"])
	}
	firstWarning, ok := warnings[0].(map[string]any)
	if !ok || firstWarning["code"] != "missing_page_script" {
		t.Fatalf("context_warnings[0] = %v, want missing_page_script", warnings[0])
	}
	source := requireMapField(t, input, "recording_source")
	if len(source) != 0 {
		t.Fatalf("recording_source = %v, want empty object when PageScript is missing", source)
	}
	report, ok := input["execution_report"].(map[string]any)
	if !ok {
		t.Fatalf("execution_report = %T, want object; input: %v", input["execution_report"], input)
	}
	if report["source"] != "blueprint" {
		t.Fatalf("execution_report.source = %v, want blueprint; report: %v", report["source"], report)
	}

	cases := []struct {
		name        string
		projectID   uint
		versionID   uint
		pageID      uint
		executionID uint
	}{
		{"execution belongs to another testcase", project.ID, version.ID, page.ID, otherExecution.ID},
		{"page belongs to another version", project.ID, version.ID, foreignPage.ID, execution.ID},
		{"wrong project", otherProject.ID, version.ID, page.ID, execution.ID},
		{"wrong version", project.ID, otherVersion.ID, page.ID, execution.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beforeCalls := env.playbotCalls(t)
			res := env.postRefineTestCase(t, tc.projectID, tc.versionID, tc.pageID, testCase.ID, map[string]any{
				"prompt":       "must not use mismatched execution",
				"execution_id": tc.executionID,
			})
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
			if got := env.playbotCalls(t); got != beforeCalls {
				t.Fatalf("Playbot calls = %d, want unchanged at %d", got, beforeCalls)
			}
			env.requireRefinementCount(t, testCase.ID, 1)
		})
	}

	t.Run("corrupt execution report", func(t *testing.T) {
		corrupt := env.seedTestExecution(t, testCase.ID, testExecutionSeed{Status: "error"})
		if err := env.db.Model(&models.TestExecution{}).Where("id = ?", corrupt.ID).Update("report_data", "{not-json").Error; err != nil {
			t.Fatalf("corrupt execution report: %v", err)
		}
		beforeCalls := env.playbotCalls(t)
		res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
			"prompt":       "must reject corrupt report",
			"execution_id": corrupt.ID,
		})
		env.requireStatus(t, res, http.StatusInternalServerError)
		env.requireJSONError(t, res)
		if got := env.playbotCalls(t); got != beforeCalls {
			t.Fatalf("Playbot calls = %d, want unchanged at %d", got, beforeCalls)
		}
		env.requireRefinementCount(t, testCase.ID, 1)
	})
}

func TestRefineTestCaseRejectsInvalidPlaybotOutputWithoutSaving(t *testing.T) {
	cases := []struct {
		name       string
		stdout     string
		wantStatus int
	}{
		{"non json stdout", "{not-json", http.StatusBadRequest},
		{"playbot error", `{"error":"refine failed"}`, http.StatusBadRequest},
		{"missing refined blueprint", `{"summary":"missing blueprint","risk_notes":""}`, http.StatusBadRequest},
		{"empty summary", `{"refined_blueprint":{"title":"x","description":"","steps":[{"action":"expect_text","target":{"text":"x","recorded_selector":".x"},"value":"x"}]},"summary":"   ","risk_notes":""}`, http.StatusBadRequest},
		{"refined blueprint missing active steps", `{"refined_blueprint":{"title":"x","description":""},"summary":"invalid active blueprint","risk_notes":""}`, http.StatusBadRequest},
		{"refined title empty", `{"refined_blueprint":{"title":" ","description":"","steps":[{"action":"expect_text","text":"x"}]},"summary":"invalid title","risk_notes":""}`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "unchanged case", Status: "active"})
			before := env.snapshotTestCase(t, testCase.ID)
			env.setPlaybotStdout(t, tc.stdout)

			res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
				"prompt": "invalid output must not save",
			})

			env.requireStatus(t, res, tc.wantStatus)
			env.requireJSONError(t, res)
			env.requireRefinementCount(t, testCase.ID, 0)
			env.requireTestCaseUnchanged(t, before, "invalid Playbot refine output must not mutate TestCase")
		})
	}
}

func TestListTestCaseRefinementsScopesSortsAndOmitsBlueprints(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	otherPage := env.seedPageInVersion(t, version.ID, "refinement list sibling page")
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "refinement owner", Status: "active"})
	otherCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "same page other case", Status: "active"})
	otherPageCase := env.seedCustomTestCase(t, otherPage.ID, testCaseSeed{Title: "other page case", Status: "active"})
	foreignCase := env.seedCustomTestCase(t, foreignPage.ID, testCaseSeed{Title: "foreign case", Status: "active"})

	older := env.seedRefinement(t, testCase.ID, refinementSeed{Prompt: "older", Status: "proposed", CreatedAt: time.Now().Add(-2 * time.Hour)})
	sameTime := time.Now().Add(-time.Hour)
	tieOlder := env.seedRefinement(t, testCase.ID, refinementSeed{Prompt: "tie older id", Status: "applied", CreatedAt: sameTime})
	tieNewer := env.seedRefinement(t, testCase.ID, refinementSeed{Prompt: "tie newer id", Status: "discarded", CreatedAt: sameTime})
	env.seedRefinement(t, otherCase.ID, refinementSeed{Prompt: "same page other case", Status: "proposed", CreatedAt: time.Now()})
	env.seedRefinement(t, otherPageCase.ID, refinementSeed{Prompt: "other page case", Status: "proposed", CreatedAt: time.Now()})
	env.seedRefinement(t, foreignCase.ID, refinementSeed{Prompt: "foreign case", Status: "proposed", CreatedAt: time.Now()})

	res := env.listTestCaseRefinements(t, project.ID, version.ID, page.ID, testCase.ID)

	env.requireStatus(t, res, http.StatusOK)
	body := env.decodeObject(t, res)
	if body["count"] != float64(3) {
		t.Fatalf("count = %v, want 3", body["count"])
	}
	items, ok := body["refinements"].([]any)
	if !ok {
		t.Fatalf("refinements = %T, want array", body["refinements"])
	}
	if len(items) != 3 {
		t.Fatalf("len(refinements) = %d, want 3", len(items))
	}
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	third := items[2].(map[string]any)
	if uint(first["id"].(float64)) != tieNewer.ID || uint(second["id"].(float64)) != tieOlder.ID || uint(third["id"].(float64)) != older.ID {
		t.Fatalf("refinement order ids = [%v, %v, %v], want [%d, %d, %d]", first["id"], second["id"], third["id"], tieNewer.ID, tieOlder.ID, older.ID)
	}
	for _, item := range items {
		obj := item.(map[string]any)
		if uint(obj["test_case_id"].(float64)) != testCase.ID {
			t.Fatalf("test_case_id = %v, want %d", obj["test_case_id"], testCase.ID)
		}
		if _, exists := obj["original_blueprint"]; exists {
			t.Fatalf("list item leaked original_blueprint: %v", obj)
		}
		if _, exists := obj["refined_blueprint"]; exists {
			t.Fatalf("list item leaked refined_blueprint: %v", obj)
		}
	}

	cases := []struct {
		name       string
		projectID  uint
		versionID  uint
		pageID     uint
		testCaseID uint
	}{
		{"wrong project", otherProject.ID, version.ID, page.ID, testCase.ID},
		{"wrong version", project.ID, otherVersion.ID, page.ID, testCase.ID},
		{"page belongs to another version", project.ID, version.ID, foreignPage.ID, testCase.ID},
		{"testcase belongs to sibling page", project.ID, version.ID, otherPage.ID, testCase.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.listTestCaseRefinements(t, tc.projectID, tc.versionID, tc.pageID, tc.testCaseID)
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
		})
	}
}

func TestGetTestCaseRefinementRequiresHierarchyAndParsesBlueprints(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	siblingPage := env.seedPageInVersion(t, version.ID, "refinement detail sibling page")
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "detail owner", Status: "active"})
	otherCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "other case", Status: "active"})
	refinement := env.seedRefinement(t, testCase.ID, refinementSeed{Prompt: "detail prompt", Status: "proposed"})
	otherRefinement := env.seedRefinement(t, otherCase.ID, refinementSeed{Prompt: "other prompt", Status: "proposed"})

	res := env.getTestCaseRefinement(t, project.ID, version.ID, page.ID, testCase.ID, refinement.ID)

	env.requireStatus(t, res, http.StatusOK)
	detail := env.decodeRefinementDetail(t, res)
	if detail["id"] != float64(refinement.ID) || detail["test_case_id"] != float64(testCase.ID) {
		t.Fatalf("refinement ids = %v, want id=%d test_case_id=%d", detail, refinement.ID, testCase.ID)
	}
	if _, ok := detail["original_blueprint"].(map[string]any); !ok {
		t.Fatalf("original_blueprint = %T, want object", detail["original_blueprint"])
	}
	if _, ok := detail["refined_blueprint"].(map[string]any); !ok {
		t.Fatalf("refined_blueprint = %T, want object", detail["refined_blueprint"])
	}

	cases := []struct {
		name         string
		projectID    uint
		versionID    uint
		pageID       uint
		testCaseID   uint
		refinementID uint
	}{
		{"wrong project", otherProject.ID, version.ID, page.ID, testCase.ID, refinement.ID},
		{"wrong version", project.ID, otherVersion.ID, page.ID, testCase.ID, refinement.ID},
		{"page belongs to another version", project.ID, version.ID, foreignPage.ID, testCase.ID, refinement.ID},
		{"testcase belongs to sibling page", project.ID, version.ID, siblingPage.ID, testCase.ID, refinement.ID},
		{"refinement belongs to another testcase", project.ID, version.ID, page.ID, testCase.ID, otherRefinement.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.getTestCaseRefinement(t, tc.projectID, tc.versionID, tc.pageID, tc.testCaseID, tc.refinementID)
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
		})
	}

	t.Run("corrupt stored blueprint", func(t *testing.T) {
		corrupt := env.seedRefinement(t, testCase.ID, refinementSeed{Prompt: "corrupt prompt", Status: "proposed"})
		env.updateRefinementStringField(t, corrupt.ID, "OriginalBlueprint", "{not-json")
		res := env.getTestCaseRefinement(t, project.ID, version.ID, page.ID, testCase.ID, corrupt.ID)
		env.requireStatus(t, res, http.StatusInternalServerError)
		env.requireJSONError(t, res)
	})
}

func TestApplyProposedRefinementUpdatesCaseAndMarksApplied(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:         "old title",
		Description:   "old description",
		Status:        "active",
		ScriptContent: "script stays unchanged",
	})
	oldUpdatedAt := time.Now().Add(-time.Hour)
	if err := env.db.Model(&models.TestCase{}).Where("id = ?", testCase.ID).Update("updated_at", oldUpdatedAt).Error; err != nil {
		t.Fatalf("force old updated_at: %v", err)
	}
	refined := map[string]any{
		"title":       "new refined title",
		"description": "new refined description",
		"steps":       []map[string]any{{"action": "expect_text", "text": "done"}},
	}
	refinement := env.seedRefinement(t, testCase.ID, refinementSeed{
		Prompt:            "apply this",
		Status:            "proposed",
		OriginalBlueprint: env.blueprintObject(t, testCase.ID),
		RefinedBlueprint:  refined,
	})

	res := env.applyTestCaseRefinement(t, project.ID, version.ID, page.ID, testCase.ID, refinement.ID)

	env.requireStatus(t, res, http.StatusOK)
	detail := env.decodeTestCaseDetail(t, res)
	if detail["title"] != "new refined title" || detail["description"] != "new refined description" {
		t.Fatalf("test_case title/description = %v/%v", detail["title"], detail["description"])
	}
	if detail["status"] != "active" || detail["script_content"] != "script stays unchanged" {
		t.Fatalf("apply changed status or script_content: %v", detail)
	}
	var stored models.TestCase
	if err := env.db.First(&stored, testCase.ID).Error; err != nil {
		t.Fatalf("load applied TestCase: %v", err)
	}
	if stored.Title != "new refined title" || stored.Description != "new refined description" || stored.Status != "active" || stored.ScriptContent != "script stays unchanged" {
		t.Fatalf("stored TestCase after apply = %+v", stored)
	}
	if !stored.UpdatedAt.After(oldUpdatedAt) {
		t.Fatalf("updated_at = %s, want after %s", stored.UpdatedAt, oldUpdatedAt)
	}
	env.requireBlueprintTitle(t, stored.Blueprint, "new refined title")
	row := env.loadRefinementRow(t, refinement.ID)
	if row.Status != "applied" || row.AppliedAt == nil {
		t.Fatalf("refinement status/applied_at = %s/%v, want applied/non-nil", row.Status, row.AppliedAt)
	}
}

func TestApplyRefinementAllowsEmptyDescription(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "description can clear", Description: "will clear", Status: "active"})
	refinement := env.seedRefinement(t, testCase.ID, refinementSeed{
		Prompt:            "clear description",
		Status:            "proposed",
		OriginalBlueprint: env.blueprintObject(t, testCase.ID),
		RefinedBlueprint: map[string]any{
			"title":       "description can clear",
			"description": "",
			"steps":       []map[string]any{{"action": "expect_text", "text": "done"}},
		},
	})

	res := env.applyTestCaseRefinement(t, project.ID, version.ID, page.ID, testCase.ID, refinement.ID)

	env.requireStatus(t, res, http.StatusOK)
	detail := env.decodeTestCaseDetail(t, res)
	if detail["description"] != "" {
		t.Fatalf("description = %v, want empty string", detail["description"])
	}
	blueprint := detail["blueprint"].(map[string]any)
	if blueprint["description"] != "" {
		t.Fatalf("blueprint description = %v, want empty string", blueprint["description"])
	}
}

func TestApplyRefinementRejectsStaleOrNonProposedWithoutMutating(t *testing.T) {
	t.Run("stale refinement", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "stale owner", Status: "active"})
		refinement := env.seedRefinement(t, testCase.ID, refinementSeed{
			Prompt:            "stale apply",
			Status:            "proposed",
			OriginalBlueprint: env.blueprintObject(t, testCase.ID),
			RefinedBlueprint: map[string]any{
				"title":       "must not apply stale",
				"description": "",
				"steps":       []map[string]any{{"action": "expect_text", "text": "done"}},
			},
		})
		if err := applyTestCaseUpdateDirect(env, testCase.ID, map[string]any{
			"title":       "edited after proposal",
			"description": "",
			"steps":       []map[string]any{{"action": "expect_text", "text": "edited"}},
		}); err != nil {
			t.Fatalf("edit TestCase after refinement: %v", err)
		}
		before := env.snapshotTestCase(t, testCase.ID)

		res := env.applyTestCaseRefinement(t, project.ID, version.ID, page.ID, testCase.ID, refinement.ID)

		env.requireStatus(t, res, http.StatusConflict)
		env.requireJSONError(t, res)
		env.requireTestCaseUnchanged(t, before, "stale apply must not overwrite current TestCase")
		row := env.loadRefinementRow(t, refinement.ID)
		if row.Status != "proposed" || row.AppliedAt != nil {
			t.Fatalf("stale refinement row = %+v, want proposed without applied_at", row)
		}
	})

	for _, status := range []string{"applied", "discarded"} {
		t.Run("non proposed "+status, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "non proposed owner", Status: "active"})
			refinement := env.seedRefinement(t, testCase.ID, refinementSeed{
				Prompt:            "non proposed",
				Status:            status,
				OriginalBlueprint: env.blueprintObject(t, testCase.ID),
				RefinedBlueprint:  validBlueprint("should not apply", ""),
			})
			before := env.snapshotTestCase(t, testCase.ID)

			res := env.applyTestCaseRefinement(t, project.ID, version.ID, page.ID, testCase.ID, refinement.ID)

			env.requireStatus(t, res, http.StatusBadRequest)
			env.requireJSONError(t, res)
			env.requireTestCaseUnchanged(t, before, "non-proposed refinement must not apply")
		})
	}
}

func TestDiscardRefinementDoesNotMutateCaseAndBlocksApply(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "discard owner", Status: "active"})
	refinement := env.seedRefinement(t, testCase.ID, refinementSeed{
		Prompt:            "discard me",
		Status:            "proposed",
		OriginalBlueprint: env.blueprintObject(t, testCase.ID),
		RefinedBlueprint:  validBlueprint("discarded title", ""),
	})
	before := env.snapshotTestCase(t, testCase.ID)

	res := env.discardTestCaseRefinement(t, project.ID, version.ID, page.ID, testCase.ID, refinement.ID)

	env.requireStatus(t, res, http.StatusOK)
	env.requireTestCaseUnchanged(t, before, "discard must not mutate TestCase")
	row := env.loadRefinementRow(t, refinement.ID)
	if row.Status != "discarded" || row.AppliedAt != nil {
		t.Fatalf("discarded refinement row = %+v", row)
	}

	apply := env.applyTestCaseRefinement(t, project.ID, version.ID, page.ID, testCase.ID, refinement.ID)
	env.requireStatus(t, apply, http.StatusBadRequest)
	env.requireJSONError(t, apply)
	env.requireTestCaseUnchanged(t, before, "discarded refinement must not apply")
}

func TestApplyRefinementValidationFailureKeepsTransactionAtomic(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "atomic owner", Status: "active"})
	refinement := env.seedRefinement(t, testCase.ID, refinementSeed{
		Prompt:            "invalid refined blueprint",
		Status:            "proposed",
		OriginalBlueprint: env.blueprintObject(t, testCase.ID),
		RefinedBlueprint: map[string]any{
			"title":       "active missing steps",
			"description": "",
		},
	})
	before := env.snapshotTestCase(t, testCase.ID)
	beforeRow := env.loadRefinementRow(t, refinement.ID)

	res := env.applyTestCaseRefinement(t, project.ID, version.ID, page.ID, testCase.ID, refinement.ID)

	env.requireStatus(t, res, http.StatusBadRequest)
	env.requireJSONError(t, res)
	env.requireTestCaseUnchanged(t, before, "failed apply must not mutate TestCase")
	afterRow := env.loadRefinementRow(t, refinement.ID)
	if afterRow != beforeRow {
		t.Fatalf("failed apply mutated refinement\nwant: %+v\n got: %+v", beforeRow, afterRow)
	}
}

func TestRefineTestCaseReusesExistingLLMConfigSelection(t *testing.T) {
	t.Run("default config", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		env.installRecordingFakePlaybotCommand(t)
		project, version, page := env.seedProjectVersionPage(t)
		testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "default llm config", Status: "active"})
		env.setPlaybotStdout(t, validPlaybotRefineOutput("default config refinement"))

		res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
			"prompt": "use default configured model",
		})

		env.requireStatus(t, res, http.StatusOK)
		job := env.playbotAgent.lastJob(t)
		if job.LLMRuntimeConfig.Endpoint != "http://llm.invalid/v1" || job.LLMRuntimeConfig.Model != "test-model" || job.LLMRuntimeConfig.ConfigID != "default-test-llm" {
			t.Fatalf("Playbot job LLM runtime config = %+v, want default config", job.LLMRuntimeConfig)
		}
		requireAgentJobOmitsSecret(t, job, "test-api-key")
		if secret := env.playbotAgent.lastSecret(t); secret.Value != "test-api-key" {
			t.Fatalf("Playbot secret channel value = %q, want default API key", secret.Value)
		}
		env.requirePlaybotCalls(t, 1)
	})

	t.Run("specified config", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		env.installRecordingFakePlaybotCommand(t)
		project, version, page := env.seedProjectVersionPage(t)
		testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "specified llm config", Status: "active"})
		env.saveRefinementLLMConfig(t, models.LLMConfigModel{
			ID:        "explicit-refine-llm",
			Name:      "Explicit refine LLM",
			Provider:  "custom",
			APIKey:    "explicit-api-key",
			Model:     "explicit-model",
			BaseURL:   "http://explicit-llm.invalid/v1",
			IsDefault: false,
			IsActive:  true,
		})
		env.setPlaybotStdout(t, validPlaybotRefineOutput("specified config refinement"))

		res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
			"prompt":        "use selected configured model",
			"llm_config_id": "explicit-refine-llm",
		})

		env.requireStatus(t, res, http.StatusOK)
		job := env.playbotAgent.lastJob(t)
		if job.LLMRuntimeConfig.Endpoint != "http://explicit-llm.invalid/v1" || job.LLMRuntimeConfig.Model != "explicit-model" || job.LLMRuntimeConfig.ConfigID != "explicit-refine-llm" {
			t.Fatalf("Playbot job LLM runtime config = %+v, want explicit config", job.LLMRuntimeConfig)
		}
		requireAgentJobOmitsSecret(t, job, "explicit-api-key")
		if secret := env.playbotAgent.lastSecret(t); secret.Value != "explicit-api-key" {
			t.Fatalf("Playbot secret channel value = %q, want explicit API key", secret.Value)
		}
		env.requirePlaybotCalls(t, 1)
	})

	cases := []struct {
		name        string
		llmConfigID string
		config      *models.LLMConfigModel
		wantCode    string
	}{
		{name: "missing config", llmConfigID: "missing-refine-llm", wantCode: "llm_config_not_found"},
		{name: "disabled config", llmConfigID: "disabled-refine-llm", config: &models.LLMConfigModel{
			ID:        "disabled-refine-llm",
			Name:      "Disabled refine LLM",
			Provider:  "custom",
			APIKey:    "disabled-api-key",
			Model:     "disabled-model",
			BaseURL:   "http://disabled-llm.invalid/v1",
			IsActive:  false,
			IsDefault: false,
		}, wantCode: "llm_config_disabled"},
		{name: "missing api key", llmConfigID: "missing-key-refine-llm", config: &models.LLMConfigModel{
			ID:       "missing-key-refine-llm",
			Name:     "Missing key refine LLM",
			Provider: "custom",
			Model:    "missing-key-model",
			BaseURL:  "http://missing-key-llm.invalid/v1",
			IsActive: true,
		}, wantCode: "llm_config_incomplete"},
		{name: "missing model", llmConfigID: "missing-model-refine-llm", config: &models.LLMConfigModel{
			ID:       "missing-model-refine-llm",
			Name:     "Missing model refine LLM",
			Provider: "custom",
			APIKey:   "missing-model-api-key",
			BaseURL:  "http://missing-model-llm.invalid/v1",
			IsActive: true,
		}, wantCode: "llm_config_incomplete"},
		{name: "missing endpoint", llmConfigID: "missing-endpoint-refine-llm", config: &models.LLMConfigModel{
			ID:       "missing-endpoint-refine-llm",
			Name:     "Missing endpoint refine LLM",
			Provider: "custom",
			APIKey:   "missing-endpoint-api-key",
			Model:    "missing-endpoint-model",
			IsActive: true,
		}, wantCode: "llm_config_incomplete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			env.installRecordingFakePlaybotCommand(t)
			project, version, page := env.seedProjectVersionPage(t)
			testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "reject bad llm config", Status: "active"})
			if tc.config != nil {
				env.saveRefinementLLMConfig(t, *tc.config)
			}
			beforeCalls := env.playbotCalls(t)

			res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
				"prompt":        "bad config must not call playbot",
				"llm_config_id": tc.llmConfigID,
			})

			env.requireStatus(t, res, http.StatusBadRequest)
			env.requireJSONError(t, res)
			p47RequireLLMErrorCode(t, res, tc.wantCode)
			if tc.config != nil && strings.TrimSpace(tc.config.APIKey) != "" {
				requireResponseBodyOmits(t, res, tc.config.APIKey)
			}
			if got := env.playbotCalls(t); got != beforeCalls {
				t.Fatalf("Playbot calls = %d, want unchanged at %d", got, beforeCalls)
			}
			env.requireRefinementCount(t, testCase.ID, 0)
		})
	}

	t.Run("default config not active", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		env.installRecordingFakePlaybotCommand(t)
		env.saveRefinementLLMConfig(t, models.LLMConfigModel{
			ID:        "default-test-llm",
			Name:      "Inactive default LLM",
			Provider:  "custom",
			APIKey:    "inactive-default-api-key",
			Model:     "inactive-default-model",
			BaseURL:   "http://inactive-default.invalid/v1",
			IsDefault: true,
			IsActive:  false,
		})
		project, version, page := env.seedProjectVersionPage(t)
		testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "inactive default config", Status: "active"})
		beforeCalls := env.playbotCalls(t)

		res := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
			"prompt": "inactive default must not call playbot",
		})

		env.requireStatus(t, res, http.StatusBadRequest)
		env.requireJSONError(t, res)
		p47RequireLLMErrorCode(t, res, "llm_config_missing_default")
		requireResponseBodyOmits(t, res, "inactive-default-api-key")
		if got := env.playbotCalls(t); got != beforeCalls {
			t.Fatalf("Playbot calls = %d, want unchanged at %d", got, beforeCalls)
		}
		env.requireRefinementCount(t, testCase.ID, 0)
	})
}

type refinementSeed struct {
	Prompt            string
	Status            string
	OriginalBlueprint map[string]any
	RefinedBlueprint  map[string]any
	Summary           string
	RiskNotes         string
	CreatedAt         time.Time
}

type refinementRow struct {
	ID                uint
	TestCaseID        uint
	UserPrompt        string
	OriginalBlueprint string
	RefinedBlueprint  string
	Summary           string
	RiskNotes         string
	Status            string
	AppliedAt         *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func validPlaybotRefineOutput(title string) string {
	data, err := json.Marshal(map[string]any{
		"schema_version": "p4.7.5",
		"status":         "success",
		"refined_blueprint": map[string]any{
			"title":       title,
			"description": "refined description",
			"steps": []map[string]any{
				{"action": "fill", "target": map[string]any{"placeholder": "Password"}, "value": "secret"},
				{"action": "expect_text", "target": map[string]any{"text": "Password is required", "recorded_selector": ".password-error"}, "value": "Password is required"},
			},
		},
		"summary":    "add password empty validation",
		"risk_notes": "confirm the validation text is stable",
		"error":      nil,
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func (e *generateContractEnv) postRefineTestCase(t *testing.T, projectID, versionID, pageID, testCaseID uint, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d/refine", projectID, versionID, pageID, testCaseID)
	return e.performRefinementRequest(t, http.MethodPost, path, payload)
}

func (e *generateContractEnv) listTestCaseRefinements(t *testing.T, projectID, versionID, pageID, testCaseID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d/refinements", projectID, versionID, pageID, testCaseID)
	return e.performRefinementRequest(t, http.MethodGet, path, nil)
}

func (e *generateContractEnv) getTestCaseRefinement(t *testing.T, projectID, versionID, pageID, testCaseID, refinementID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d/refinements/%d", projectID, versionID, pageID, testCaseID, refinementID)
	return e.performRefinementRequest(t, http.MethodGet, path, nil)
}

func (e *generateContractEnv) applyTestCaseRefinement(t *testing.T, projectID, versionID, pageID, testCaseID, refinementID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d/refinements/%d/apply", projectID, versionID, pageID, testCaseID, refinementID)
	return e.performRefinementRequest(t, http.MethodPost, path, map[string]any{})
}

func (e *generateContractEnv) discardTestCaseRefinement(t *testing.T, projectID, versionID, pageID, testCaseID, refinementID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d/refinements/%d/discard", projectID, versionID, pageID, testCaseID, refinementID)
	return e.performRefinementRequest(t, http.MethodPost, path, map[string]any{})
}

func (e *generateContractEnv) performRefinementRequest(t *testing.T, method, path string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal refinement request payload: %v", err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	e.router.ServeHTTP(res, req)
	return res
}

func (e *generateContractEnv) decodeRefinementDetail(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := e.decodeObject(t, res)
	refinement, ok := body["refinement"].(map[string]any)
	if !ok {
		t.Fatalf("refinement = %T, want object; body: %v", body["refinement"], body)
	}
	return refinement
}

func (e *generateContractEnv) seedRefinement(t *testing.T, testCaseID uint, seed refinementSeed) refinementRow {
	t.Helper()
	prompt := strings.TrimSpace(seed.Prompt)
	if prompt == "" {
		prompt = "contract prompt"
	}
	status := seed.Status
	if status == "" {
		status = "proposed"
	}
	original := seed.OriginalBlueprint
	if original == nil {
		original = e.blueprintObject(t, testCaseID)
	}
	refined := seed.RefinedBlueprint
	if refined == nil {
		refined = validBlueprint("refined title", "refined description")
	}
	summary := seed.Summary
	if summary == "" {
		summary = "contract summary"
	}
	createdAt := seed.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	originalJSON := marshalJSONMap(t, original)
	refinedJSON := marshalJSONMap(t, refined)
	refinement := models.LLMRefinement{
		TestCaseID:       testCaseID,
		UserPrompt:       prompt,
		RefinedBlueprint: refinedJSON,
		CreatedAt:        createdAt,
	}
	setRefinementStringField(t, &refinement, "OriginalBlueprint", originalJSON)
	setRefinementStringField(t, &refinement, "Summary", summary)
	setRefinementStringField(t, &refinement, "RiskNotes", seed.RiskNotes)
	setRefinementStringField(t, &refinement, "Status", status)
	setRefinementTimeField(t, &refinement, "UpdatedAt", createdAt)
	if err := e.db.Create(&refinement).Error; err != nil {
		t.Fatalf("seed LLMRefinement: %v", err)
	}
	if refinement.ID == 0 {
		t.Fatalf("seeded LLMRefinement id is zero")
	}
	return e.loadRefinementRow(t, refinement.ID)
}

func (e *generateContractEnv) loadRefinementRow(t *testing.T, id uint) refinementRow {
	t.Helper()
	var refinement models.LLMRefinement
	if err := e.db.First(&refinement, id).Error; err != nil {
		t.Fatalf("load refinement row %d: %v", id, err)
	}
	return refinementRow{
		ID:                refinement.ID,
		TestCaseID:        refinement.TestCaseID,
		UserPrompt:        refinement.UserPrompt,
		OriginalBlueprint: refinementStringField(t, &refinement, "OriginalBlueprint"),
		RefinedBlueprint:  refinement.RefinedBlueprint,
		Summary:           refinementStringField(t, &refinement, "Summary"),
		RiskNotes:         refinementStringField(t, &refinement, "RiskNotes"),
		Status:            refinementStringField(t, &refinement, "Status"),
		AppliedAt:         refinementTimePtrField(t, &refinement, "AppliedAt"),
		CreatedAt:         refinement.CreatedAt,
		UpdatedAt:         refinementTimeField(t, &refinement, "UpdatedAt"),
	}
}

func (e *generateContractEnv) updateRefinementStringField(t *testing.T, id uint, fieldName string, value string) {
	t.Helper()
	var refinement models.LLMRefinement
	if err := e.db.First(&refinement, id).Error; err != nil {
		t.Fatalf("load refinement %d before update: %v", id, err)
	}
	setRefinementStringField(t, &refinement, fieldName, value)
	if err := e.db.Save(&refinement).Error; err != nil {
		t.Fatalf("update refinement %d field %s: %v", id, fieldName, err)
	}
}

func (e *generateContractEnv) installRecordingFakePlaybotCommand(t *testing.T) {
	t.Helper()
}

func (e *generateContractEnv) readRecordedPlaybotInput(t *testing.T) map[string]any {
	t.Helper()
	return e.playbotAgent.lastJobMap(t)
}

func (e *generateContractEnv) readRecordedPlaybotArgs(t *testing.T) string {
	t.Helper()
	job := e.playbotAgent.lastJob(t)
	secret := e.playbotAgent.lastSecret(t)
	return fmt.Sprintf("--mode %s --llm-endpoint %s --llm-model %s --llm-config-id %s --secret-env %s --secret-value %s",
		job.Mode,
		job.LLMRuntimeConfig.Endpoint,
		job.LLMRuntimeConfig.Model,
		job.LLMRuntimeConfig.ConfigID,
		secret.EnvName,
		secret.Value,
	)
}

func (e *generateContractEnv) saveRefinementLLMConfig(t *testing.T, cfg models.LLMConfigModel) {
	t.Helper()
	if cfg.CreatedAt.IsZero() {
		cfg.CreatedAt = time.Now()
	}
	if cfg.UpdatedAt.IsZero() {
		cfg.UpdatedAt = cfg.CreatedAt
	}
	if err := e.handler.db.SaveLLMConfig(&cfg); err != nil {
		t.Fatalf("save LLM config %s: %v", cfg.ID, err)
	}
}

func requireObjectArrayField(t *testing.T, obj map[string]any, name string) []any {
	t.Helper()
	value, ok := obj[name].([]any)
	if !ok {
		t.Fatalf("%s = %T, want array; object: %v", name, obj[name], obj)
	}
	return value
}

func requireMapField(t *testing.T, obj map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := obj[name].(map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want object; object: %v", name, obj[name], obj)
	}
	return value
}

func requireNilObjectField(t *testing.T, obj map[string]any, name string) {
	t.Helper()
	value, exists := obj[name]
	if !exists {
		t.Fatalf("%s missing, want explicit null; object: %v", name, obj)
	}
	if value != nil {
		t.Fatalf("%s = %v, want explicit null; object: %v", name, value, obj)
	}
}

func requireStringContains(t *testing.T, value, want string) {
	t.Helper()
	if !strings.Contains(value, want) {
		t.Fatalf("value does not contain %q: %s", want, value)
	}
}

func requireAgentJobOmitsSecret(t *testing.T, job playbotagent.Job, secret string) {
	t.Helper()
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal Playbot agent job: %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("Playbot agent job leaked secret %q: %s", secret, data)
	}
}

func requireResponseBodyOmits(t *testing.T, res *httptest.ResponseRecorder, secret string) {
	t.Helper()
	if strings.TrimSpace(secret) == "" {
		t.Fatalf("secret must be non-empty")
	}
	if strings.Contains(res.Body.String(), secret) {
		t.Fatalf("response body leaked secret %q: %s", secret, res.Body.String())
	}
}

func setRefinementStringField(t *testing.T, refinement *models.LLMRefinement, fieldName string, value string) {
	t.Helper()
	field := refinementModelField(t, refinement, fieldName)
	if field.Kind() != reflect.String {
		t.Fatalf("models.LLMRefinement.%s = %s, want string", fieldName, field.Type())
	}
	if !field.CanSet() {
		t.Fatalf("models.LLMRefinement.%s is not settable", fieldName)
	}
	field.SetString(value)
}

func setRefinementTimeField(t *testing.T, refinement *models.LLMRefinement, fieldName string, value time.Time) {
	t.Helper()
	field := refinementModelField(t, refinement, fieldName)
	if field.Type() != reflect.TypeOf(time.Time{}) {
		t.Fatalf("models.LLMRefinement.%s = %s, want time.Time", fieldName, field.Type())
	}
	if !field.CanSet() {
		t.Fatalf("models.LLMRefinement.%s is not settable", fieldName)
	}
	field.Set(reflect.ValueOf(value))
}

func refinementStringField(t *testing.T, refinement *models.LLMRefinement, fieldName string) string {
	t.Helper()
	field := refinementModelField(t, refinement, fieldName)
	if field.Kind() != reflect.String {
		t.Fatalf("models.LLMRefinement.%s = %s, want string", fieldName, field.Type())
	}
	return field.String()
}

func refinementTimeField(t *testing.T, refinement *models.LLMRefinement, fieldName string) time.Time {
	t.Helper()
	field := refinementModelField(t, refinement, fieldName)
	if field.Type() != reflect.TypeOf(time.Time{}) {
		t.Fatalf("models.LLMRefinement.%s = %s, want time.Time", fieldName, field.Type())
	}
	return field.Interface().(time.Time)
}

func refinementTimePtrField(t *testing.T, refinement *models.LLMRefinement, fieldName string) *time.Time {
	t.Helper()
	field := refinementModelField(t, refinement, fieldName)
	if field.Type() != reflect.TypeOf((*time.Time)(nil)) {
		t.Fatalf("models.LLMRefinement.%s = %s, want *time.Time", fieldName, field.Type())
	}
	if field.IsNil() {
		return nil
	}
	return field.Interface().(*time.Time)
}

func refinementModelField(t *testing.T, refinement *models.LLMRefinement, fieldName string) reflect.Value {
	t.Helper()
	value := reflect.ValueOf(refinement)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		t.Fatalf("refinement must be non-nil pointer")
	}
	field := value.Elem().FieldByName(fieldName)
	if !field.IsValid() {
		t.Fatalf("models.LLMRefinement missing P4 field %s", fieldName)
	}
	return field
}

func (e *generateContractEnv) requireRefinementCount(t *testing.T, testCaseID uint, want int64) {
	t.Helper()
	var count int64
	if err := e.db.Table("llm_refinements").Where("test_case_id = ?", testCaseID).Count(&count).Error; err != nil {
		t.Fatalf("count LLMRefinement rows: %v", err)
	}
	if count != want {
		t.Fatalf("LLMRefinement count for testcase %d = %d, want %d", testCaseID, count, want)
	}
}

func (e *generateContractEnv) blueprintObject(t *testing.T, testCaseID uint) map[string]any {
	t.Helper()
	var testCase models.TestCase
	if err := e.db.First(&testCase, testCaseID).Error; err != nil {
		t.Fatalf("load TestCase %d blueprint: %v", testCaseID, err)
	}
	var blueprint map[string]any
	if err := json.Unmarshal([]byte(testCase.Blueprint), &blueprint); err != nil {
		t.Fatalf("parse TestCase %d blueprint: %v", testCaseID, err)
	}
	return blueprint
}

func marshalJSONMap(t *testing.T, value map[string]any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON map: %v", err)
	}
	return string(data)
}

func applyTestCaseUpdateDirect(env *generateContractEnv, testCaseID uint, blueprint map[string]any) error {
	title, _ := blueprint["title"].(string)
	description, _ := blueprint["description"].(string)
	data, err := json.Marshal(blueprint)
	if err != nil {
		return err
	}
	return env.db.Model(&models.TestCase{}).Where("id = ?", testCaseID).Updates(map[string]any{
		"title":       title,
		"description": description,
		"blueprint":   string(data),
	}).Error
}
