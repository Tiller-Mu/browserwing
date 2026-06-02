package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/browserwing/browserwing/models"
)

func TestListTestCasesReturnsOnlyPageSummaries(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	otherPage := env.seedPageInVersion(t, version.ID, "same version sibling page")
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	first := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:         "page case one",
		Description:   "first case",
		Status:        "active",
		ScriptContent: "large script must stay out of summaries",
	})
	second := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:       "page case two",
		Description: "second case",
		Status:      "draft",
	})
	third := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:       "page case three",
		Description: "third case",
		Status:      "archived",
	})
	newerUpdatedAt := time.Now()
	sameUpdatedAt := time.Now().Add(-time.Hour)
	if err := env.db.Model(&models.TestCase{}).Where("id = ?", first.ID).Update("updated_at", newerUpdatedAt).Error; err != nil {
		t.Fatalf("force newer updated_at: %v", err)
	}
	if err := env.db.Model(&models.TestCase{}).Where("id IN ?", []uint{second.ID, third.ID}).Update("updated_at", sameUpdatedAt).Error; err != nil {
		t.Fatalf("force tie updated_at: %v", err)
	}
	env.seedCustomTestCase(t, otherPage.ID, testCaseSeed{Title: "same version other page", Status: "active"})
	env.seedCustomTestCase(t, foreignPage.ID, testCaseSeed{Title: "other project page", Status: "active"})

	res := env.getTestCases(t, project.ID, version.ID, page.ID)

	env.requireStatus(t, res, http.StatusOK)
	body := env.decodeObject(t, res)
	if body["count"] != float64(3) {
		t.Fatalf("count = %v, want 3", body["count"])
	}
	items, ok := body["test_cases"].([]any)
	if !ok {
		t.Fatalf("test_cases = %T, want array", body["test_cases"])
	}
	if len(items) != 3 {
		t.Fatalf("len(test_cases) = %d, want 3", len(items))
	}
	firstItem := items[0].(map[string]any)
	secondItem := items[1].(map[string]any)
	thirdItem := items[2].(map[string]any)
	if uint(firstItem["id"].(float64)) != first.ID || uint(secondItem["id"].(float64)) != third.ID || uint(thirdItem["id"].(float64)) != second.ID {
		t.Fatalf("list order ids = [%v, %v, %v], want [%d, %d, %d]", firstItem["id"], secondItem["id"], thirdItem["id"], first.ID, third.ID, second.ID)
	}
	seen := map[uint]bool{}
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("list item = %T, want object", item)
		}
		id := uint(obj["id"].(float64))
		seen[id] = true
		if uint(obj["page_id"].(float64)) != page.ID {
			t.Fatalf("list item page_id = %v, want %d", obj["page_id"], page.ID)
		}
		if _, exists := obj["blueprint"]; exists {
			t.Fatalf("list item leaked blueprint: %v", obj)
		}
		if _, exists := obj["script_content"]; exists {
			t.Fatalf("list item leaked script_content: %v", obj)
		}
	}
	if !seen[first.ID] || !seen[second.ID] || !seen[third.ID] || len(seen) != 3 {
		t.Fatalf("list returned ids %v, want only %d, %d, and %d", seen, first.ID, second.ID, third.ID)
	}

	cases := []struct {
		name      string
		projectID uint
		versionID uint
		pageID    uint
	}{
		{"wrong project", otherProject.ID, version.ID, page.ID},
		{"wrong version", project.ID, otherVersion.ID, page.ID},
		{"page belongs to another version", project.ID, version.ID, foreignPage.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.getTestCases(t, tc.projectID, tc.versionID, tc.pageID)
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
		})
	}
}

func TestGetTestCaseRequiresFullHierarchyAndReturnsDetail(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	siblingPage := env.seedPageInVersion(t, version.ID, "detail sibling page")
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:         "detail case",
		Description:   "detail description",
		Status:        "active",
		ScriptContent: "script body",
	})

	res := env.getTestCase(t, project.ID, version.ID, page.ID, testCase.ID)

	env.requireStatus(t, res, http.StatusOK)
	detail := env.decodeTestCaseDetail(t, res)
	if detail["id"] != float64(testCase.ID) || detail["page_id"] != float64(page.ID) {
		t.Fatalf("detail ids = %v, want id=%d page_id=%d", detail, testCase.ID, page.ID)
	}
	if detail["script_content"] != "script body" {
		t.Fatalf("script_content = %v, want script body", detail["script_content"])
	}
	blueprint, ok := detail["blueprint"].(map[string]any)
	if !ok {
		t.Fatalf("blueprint = %T, want object", detail["blueprint"])
	}
	if blueprint["title"] != "detail case" {
		t.Fatalf("blueprint title = %v, want detail case", blueprint["title"])
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
		{"testcase belongs to sibling page", project.ID, version.ID, siblingPage.ID, testCase.ID},
		{"wrong testcase", project.ID, version.ID, page.ID, testCase.ID + 9999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.getTestCase(t, tc.projectID, tc.versionID, tc.pageID, tc.testCaseID)
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
		})
	}
}

func TestCreateTestCaseManuallyDefaultsActiveWithoutMainFlowOrPlaybot(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)

	res := env.postTestCase(t, project.ID, version.ID, page.ID, map[string]any{
		"title":          "manual active case",
		"description":    "created without recording",
		"blueprint":      validBlueprint("manual active case", "created without recording"),
		"script_content": "manual script",
	})

	env.requireStatus(t, res, http.StatusOK)
	detail := env.decodeTestCaseDetail(t, res)
	if detail["status"] != "active" {
		t.Fatalf("status = %v, want active by default", detail["status"])
	}
	if detail["script_content"] != "manual script" {
		t.Fatalf("script_content = %v, want manual script", detail["script_content"])
	}
	env.requireTestCaseCount(t, page.ID, 1)
	env.requirePageScriptCount(t, page.ID, 0)
	env.requirePlaybotCalls(t, 0)
}

func TestCreateTestCaseRequiresProjectVersionPageHierarchy(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	control := env.postTestCase(t, project.ID, version.ID, page.ID, map[string]any{
		"title":     "control case confirms create handler is mounted",
		"blueprint": validBlueprint("control case confirms create handler is mounted", ""),
	})
	env.requireStatus(t, control, http.StatusOK)

	payload := map[string]any{
		"title":     "manual case should not be created on mismatch",
		"blueprint": validBlueprint("manual case should not be created on mismatch", ""),
	}

	cases := []struct {
		name      string
		projectID uint
		versionID uint
		pageID    uint
	}{
		{"wrong project", otherProject.ID, version.ID, page.ID},
		{"wrong version", project.ID, otherVersion.ID, page.ID},
		{"page belongs to another version", project.ID, version.ID, foreignPage.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.postTestCase(t, tc.projectID, tc.versionID, tc.pageID, payload)
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
			env.requireTestCaseCount(t, page.ID, 1)
			env.requireTestCaseCount(t, foreignPage.ID, 0)
		})
	}
	env.requirePlaybotCalls(t, 0)
}

func TestCreateTestCaseValidationRejectsInvalidInputWithoutSaving(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "empty title",
			payload: map[string]any{
				"title":     "   ",
				"blueprint": validBlueprint("ignored", "ignored"),
			},
		},
		{
			name: "invalid status",
			payload: map[string]any{
				"title":     "invalid status",
				"status":    "passed",
				"blueprint": validBlueprint("invalid status", ""),
			},
		},
		{
			name: "blueprint is not object",
			payload: map[string]any{
				"title":     "invalid blueprint",
				"blueprint": []any{"not", "object"},
			},
		},
		{
			name: "missing blueprint",
			payload: map[string]any{
				"title": "missing blueprint",
			},
		},
		{
			name: "active blueprint missing steps",
			payload: map[string]any{
				"title":     "missing steps",
				"status":    "active",
				"blueprint": map[string]any{"title": "missing steps", "description": ""},
			},
		},
		{
			name: "active blueprint empty steps",
			payload: map[string]any{
				"title":  "empty steps",
				"status": "active",
				"blueprint": map[string]any{
					"title":       "empty steps",
					"description": "",
					"steps":       []map[string]any{},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)

			res := env.postTestCase(t, project.ID, version.ID, page.ID, tc.payload)

			env.requireStatus(t, res, http.StatusBadRequest)
			env.requireJSONError(t, res)
			env.requireTestCaseCount(t, page.ID, 0)
			env.requirePlaybotCalls(t, 0)
		})
	}
}

func TestTestCaseStatusContract(t *testing.T) {
	for _, status := range []string{"active", "draft", "archived"} {
		t.Run("allows "+status, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)

			res := env.postTestCase(t, project.ID, version.ID, page.ID, map[string]any{
				"title":     "status " + status,
				"status":    status,
				"blueprint": validBlueprint("status "+status, ""),
			})

			env.requireStatus(t, res, http.StatusOK)
			env.requireTestCaseCount(t, page.ID, 1)
		})
	}

	draftBlueprints := []struct {
		name      string
		blueprint map[string]any
	}{
		{"draft without steps", map[string]any{"title": "draft without steps", "description": ""}},
		{"draft with empty steps", map[string]any{"title": "draft with empty steps", "description": "", "steps": []map[string]any{}}},
	}
	for _, tc := range draftBlueprints {
		t.Run("allows "+tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)

			res := env.postTestCase(t, project.ID, version.ID, page.ID, map[string]any{
				"title":     tc.name,
				"status":    "draft",
				"blueprint": tc.blueprint,
			})

			env.requireStatus(t, res, http.StatusOK)
			env.requireTestCaseCount(t, page.ID, 1)
		})
	}

	for _, status := range []string{"passed", "failed", "error"} {
		t.Run("rejects "+status, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)

			res := env.postTestCase(t, project.ID, version.ID, page.ID, map[string]any{
				"title":     "execution status must not be asset status",
				"status":    status,
				"blueprint": validBlueprint("execution status must not be asset status", ""),
			})

			env.requireStatus(t, res, http.StatusBadRequest)
			env.requireTestCaseCount(t, page.ID, 0)
		})
	}
}

func TestUpdateTestCasePartiallyNormalizesBlueprintSavesScriptContentAndUpdatesTimestamp(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:         "old title",
		Description:   "old description",
		Status:        "draft",
		ScriptContent: "old script",
	})
	oldUpdatedAt := time.Now().Add(-time.Hour)
	if err := env.db.Model(&models.TestCase{}).Where("id = ?", testCase.ID).Update("updated_at", oldUpdatedAt).Error; err != nil {
		t.Fatalf("force old updated_at: %v", err)
	}

	res := env.putTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
		"title":          "new title",
		"description":    "new description",
		"script_content": "new script",
		"blueprint": map[string]any{
			"title":       "drift title",
			"description": "drift description",
			"steps":       []map[string]any{{"action": "click", "target": "save"}},
		},
	})

	env.requireStatus(t, res, http.StatusOK)
	detail := env.decodeTestCaseDetail(t, res)
	if detail["title"] != "new title" || detail["description"] != "new description" {
		t.Fatalf("detail title/description = %v/%v", detail["title"], detail["description"])
	}
	if detail["status"] != "draft" || detail["script_content"] != "new script" {
		t.Fatalf("partial update changed omitted fields: %v", detail)
	}
	blueprint := detail["blueprint"].(map[string]any)
	if blueprint["title"] != "new title" || blueprint["description"] != "new description" {
		t.Fatalf("blueprint title/description not synchronized: %v", blueprint)
	}

	var stored models.TestCase
	if err := env.db.First(&stored, testCase.ID).Error; err != nil {
		t.Fatalf("load stored TestCase: %v", err)
	}
	env.requireBlueprintTitle(t, stored.Blueprint, "new title")
	if stored.ScriptContent != "new script" {
		t.Fatalf("stored script_content = %q, want new script", stored.ScriptContent)
	}
	if !stored.UpdatedAt.After(oldUpdatedAt) {
		t.Fatalf("updated_at = %s, want after %s", stored.UpdatedAt, oldUpdatedAt)
	}
}

func TestUpdateTestCaseRequiresFullHierarchyWithoutMutatingExistingCase(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	siblingPage := env.seedPageInVersion(t, version.ID, "update sibling page")
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	controlCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:  "control update case",
		Status: "draft",
	})
	control := env.putTestCase(t, project.ID, version.ID, page.ID, controlCase.ID, map[string]any{
		"title": "control update case changed",
	})
	env.requireStatus(t, control, http.StatusOK)
	controlDetail := env.decodeTestCaseDetail(t, control)
	controlBlueprint, ok := controlDetail["blueprint"].(map[string]any)
	if !ok {
		t.Fatalf("control blueprint = %T, want object", controlDetail["blueprint"])
	}
	if controlDetail["title"] != "control update case changed" || controlBlueprint["title"] != "control update case changed" {
		t.Fatalf("title-only update did not synchronize blueprint title: %v", controlDetail)
	}
	var storedControl models.TestCase
	if err := env.db.First(&storedControl, controlCase.ID).Error; err != nil {
		t.Fatalf("load control TestCase: %v", err)
	}
	env.requireBlueprintTitle(t, storedControl.Blueprint, "control update case changed")

	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:         "protected case",
		Description:   "must not change",
		Status:        "active",
		ScriptContent: "protected script",
	})
	before := env.snapshotTestCase(t, testCase.ID)
	payload := map[string]any{
		"title":       "should not be applied",
		"description": "should not be applied",
		"blueprint":   validBlueprint("should not be applied", "should not be applied"),
	}

	cases := []struct {
		name      string
		projectID uint
		versionID uint
		pageID    uint
	}{
		{"wrong project", otherProject.ID, version.ID, page.ID},
		{"wrong version", project.ID, otherVersion.ID, page.ID},
		{"page belongs to another version", project.ID, version.ID, foreignPage.ID},
		{"testcase belongs to sibling page", project.ID, version.ID, siblingPage.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.putTestCase(t, tc.projectID, tc.versionID, tc.pageID, testCase.ID, payload)
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
			env.requireTestCaseUnchanged(t, before, "mismatched update must not mutate existing TestCase")
		})
	}
}

func TestUpdateTestCaseValidationFailureDoesNotMutateExistingCase(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"empty title", map[string]any{"title": "  "}},
		{"invalid status", map[string]any{"status": "failed"}},
		{"invalid blueprint", map[string]any{"blueprint": "not an object"}},
		{"active missing steps", map[string]any{
			"status":    "active",
			"blueprint": map[string]any{"title": "still invalid"},
		}},
		{"active empty steps", map[string]any{
			"status": "active",
			"blueprint": map[string]any{
				"title":       "still invalid",
				"description": "",
				"steps":       []map[string]any{},
			},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
				Title:         "unchanged title",
				Description:   "unchanged description",
				Status:        "active",
				ScriptContent: "unchanged script",
			})
			before := env.snapshotTestCase(t, testCase.ID)

			res := env.putTestCase(t, project.ID, version.ID, page.ID, testCase.ID, tc.payload)

			env.requireStatus(t, res, http.StatusBadRequest)
			env.requireJSONError(t, res)
			env.requireTestCaseUnchanged(t, before, "failed update must leave TestCase unchanged")
		})
	}

	t.Run("status only active requires existing blueprint steps", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
			Title:       "draft without steps",
			Description: "cannot become active without steps",
			Status:      "draft",
			Blueprint:   map[string]any{"title": "draft without steps", "description": "cannot become active without steps"},
		})
		before := env.snapshotTestCase(t, testCase.ID)

		res := env.putTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
			"status": "active",
		})

		env.requireStatus(t, res, http.StatusBadRequest)
		env.requireJSONError(t, res)
		env.requireTestCaseUnchanged(t, before, "status-only active update must leave draft TestCase unchanged")
	})
}

func TestDeleteTestCaseRemovesOnlyTargetAndRequiresHierarchy(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	otherPage := env.seedPageInVersion(t, version.ID, "delete sibling page")
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	target := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "delete target", Status: "active"})
	samePageSurvivor := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "same page survivor", Status: "active"})
	otherPageSurvivor := env.seedCustomTestCase(t, otherPage.ID, testCaseSeed{Title: "other page survivor", Status: "active"})

	res := env.deleteTestCase(t, project.ID, version.ID, page.ID, target.ID)

	env.requireStatus(t, res, http.StatusOK)
	env.requireTestCaseMissing(t, target.ID, "delete should remove target")
	env.requireTestCaseExists(t, samePageSurvivor.ID, "delete must not remove same-page survivor")
	env.requireTestCaseExists(t, otherPageSurvivor.ID, "delete must not remove other-page survivor")

	protected := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "protected delete case", Status: "active"})
	cases := []struct {
		name      string
		projectID uint
		versionID uint
		pageID    uint
	}{
		{"wrong project", otherProject.ID, version.ID, page.ID},
		{"wrong version", project.ID, otherVersion.ID, page.ID},
		{"page belongs to another version", project.ID, version.ID, foreignPage.ID},
		{"testcase belongs to sibling page", project.ID, version.ID, otherPage.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.deleteTestCase(t, tc.projectID, tc.versionID, tc.pageID, protected.ID)
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
			env.requireTestCaseExists(t, protected.ID, "mismatched delete must not remove target")
			env.requireTestCaseExists(t, samePageSurvivor.ID, "mismatched delete must not remove same-page survivor")
			env.requireTestCaseExists(t, otherPageSurvivor.ID, "mismatched delete must not remove sibling-page survivor")
		})
	}
}

type testCaseSeed struct {
	Title         string
	Description   string
	Status        string
	ScriptContent string
	Blueprint     map[string]any
}

func (e *generateContractEnv) seedCustomTestCase(t *testing.T, pageID uint, seed testCaseSeed) models.TestCase {
	t.Helper()
	title := strings.TrimSpace(seed.Title)
	if title == "" {
		title = "seed case"
	}
	description := seed.Description
	status := seed.Status
	if status == "" {
		status = "active"
	}
	blueprint := seed.Blueprint
	if blueprint == nil {
		blueprint = validBlueprint(title, description)
	}
	blueprint["title"] = title
	blueprint["description"] = description
	blueprintJSON, err := json.Marshal(blueprint)
	if err != nil {
		t.Fatalf("marshal seed blueprint: %v", err)
	}
	testCase := models.TestCase{
		PageID:        pageID,
		Title:         title,
		Description:   description,
		Blueprint:     string(blueprintJSON),
		ScriptContent: seed.ScriptContent,
		Status:        status,
	}
	if err := e.db.Create(&testCase).Error; err != nil {
		t.Fatalf("seed custom TestCase: %v", err)
	}
	return testCase
}

func validBlueprint(title, description string) map[string]any {
	return map[string]any{
		"title":       title,
		"description": description,
		"steps":       []map[string]any{{"action": "click", "target": "primary"}},
	}
}

func (e *generateContractEnv) seedPageInVersion(t *testing.T, versionID uint, name string) models.TestPage {
	t.Helper()
	page := models.TestPage{
		VersionID:   versionID,
		Name:        name,
		Path:        "/" + strings.ReplaceAll(name, " ", "-"),
		Description: "sibling page in the same version",
	}
	if err := e.db.Create(&page).Error; err != nil {
		t.Fatalf("seed sibling page: %v", err)
	}
	return page
}

func (e *generateContractEnv) getTestCases(t *testing.T, projectID, versionID, pageID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases", projectID, versionID, pageID)
	return e.performTestCaseRequest(t, http.MethodGet, path, nil)
}

func (e *generateContractEnv) postTestCase(t *testing.T, projectID, versionID, pageID uint, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases", projectID, versionID, pageID)
	return e.performTestCaseRequest(t, http.MethodPost, path, payload)
}

func (e *generateContractEnv) getTestCase(t *testing.T, projectID, versionID, pageID, testCaseID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d", projectID, versionID, pageID, testCaseID)
	return e.performTestCaseRequest(t, http.MethodGet, path, nil)
}

func (e *generateContractEnv) putTestCase(t *testing.T, projectID, versionID, pageID, testCaseID uint, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d", projectID, versionID, pageID, testCaseID)
	return e.performTestCaseRequest(t, http.MethodPut, path, payload)
}

func (e *generateContractEnv) deleteTestCase(t *testing.T, projectID, versionID, pageID, testCaseID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d", projectID, versionID, pageID, testCaseID)
	return e.performTestCaseRequest(t, http.MethodDelete, path, nil)
}

func (e *generateContractEnv) performTestCaseRequest(t *testing.T, method, path string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal request payload: %v", err)
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

func (e *generateContractEnv) decodeTestCaseDetail(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := e.decodeObject(t, res)
	testCase, ok := body["test_case"].(map[string]any)
	if !ok {
		t.Fatalf("test_case = %T, want object; body: %v", body["test_case"], body)
	}
	return testCase
}

func (e *generateContractEnv) requirePageScriptCount(t *testing.T, pageID uint, want int64) {
	t.Helper()
	var count int64
	if err := e.db.Model(&models.PageScript{}).Where("page_id = ?", pageID).Count(&count).Error; err != nil {
		t.Fatalf("count PageScript rows: %v", err)
	}
	if count != want {
		t.Fatalf("PageScript count for page %d = %d, want %d", pageID, count, want)
	}
}
