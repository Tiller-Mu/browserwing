package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/browserwing/browserwing/models"
)

func TestRunTestCaseRequiresHierarchyBeforeExecutionValidation(t *testing.T) {
	env := newGenerateContractEnv(t)
	runner := newFailOnCallTestCaseRunner(t)
	env.installFakeTestCaseRunner(t, runner)
	project, version, page := env.seedProjectVersionPage(t)
	siblingPage := env.seedPageInVersion(t, version.ID, "execution sibling page")
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:  "invalid executable blueprint",
		Status: "active",
		Blueprint: map[string]any{
			"title":       "invalid executable blueprint",
			"description": "",
		},
	})

	control := env.runTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{})
	env.requireStatus(t, control, http.StatusBadRequest)
	env.requireJSONError(t, control)
	env.requireTestExecutionCount(t, testCase.ID, 0)
	if runner.CallCount() != 0 {
		t.Fatalf("fake runner calls = %d, want 0 for preflight failure", runner.CallCount())
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
		{"missing testcase", project.ID, version.ID, page.ID, testCase.ID + 9999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.runTestCase(t, tc.projectID, tc.versionID, tc.pageID, tc.testCaseID, map[string]any{})
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
			env.requireTestExecutionCount(t, testCase.ID, 0)
			if runner.CallCount() != 0 {
				t.Fatalf("fake runner calls = %d, want 0 for hierarchy mismatch", runner.CallCount())
			}
		})
	}
}

func TestRunTestCaseRejectsNonActiveAndInvalidBlueprintWithoutExecution(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		blueprint  map[string]any
		beforeRun  func(t *testing.T, env *generateContractEnv, testCase models.TestCase)
		wantStatus int
	}{
		{
			name:      "draft cannot execute",
			status:    "draft",
			blueprint: executableBlueprint("draft cannot execute"),
		},
		{
			name:      "archived cannot execute",
			status:    "archived",
			blueprint: executableBlueprint("archived cannot execute"),
		},
		{
			name:      "blueprint is not json object",
			status:    "active",
			blueprint: executableBlueprint("corrupt after seed"),
			beforeRun: func(t *testing.T, env *generateContractEnv, testCase models.TestCase) {
				t.Helper()
				if err := env.db.Model(&models.TestCase{}).Where("id = ?", testCase.ID).Update("blueprint", "[]").Error; err != nil {
					t.Fatalf("corrupt blueprint: %v", err)
				}
			},
		},
		{
			name:      "blueprint missing steps even with script content",
			status:    "active",
			blueprint: map[string]any{"title": "script content must not be fallback", "description": ""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			runner := newFailOnCallTestCaseRunner(t)
			env.installFakeTestCaseRunner(t, runner)
			project, version, page := env.seedProjectVersionPage(t)
			testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
				Title:         tc.name,
				Status:        tc.status,
				Blueprint:     tc.blueprint,
				ScriptContent: "print('script content must not execute as a fallback')",
			})
			before := env.snapshotTestCase(t, testCase.ID)
			if tc.beforeRun != nil {
				tc.beforeRun(t, env, testCase)
				before = env.snapshotTestCase(t, testCase.ID)
			}

			res := env.runTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{})

			env.requireStatus(t, res, http.StatusBadRequest)
			env.requireJSONError(t, res)
			env.requireTestExecutionCount(t, testCase.ID, 0)
			env.requireTestCaseUnchanged(t, before, "execution preflight failure must not mutate TestCase")
			if runner.CallCount() != 0 {
				t.Fatalf("fake runner calls = %d, want 0 for preflight failure", runner.CallCount())
			}
		})
	}
}

func TestRunTestCaseBlueprintSchemaRejectsInvalidStepsWithoutExecution(t *testing.T) {
	cases := []struct {
		name      string
		blueprint map[string]any
	}{
		{
			name:      "empty steps",
			blueprint: map[string]any{"title": "empty steps", "steps": []map[string]any{}},
		},
		{
			name: "unknown action",
			blueprint: map[string]any{
				"title": "unknown action",
				"steps": []map[string]any{{"action": "hover", "target": map[string]any{"text": "Save"}}},
			},
		},
		{
			name: "click missing target",
			blueprint: map[string]any{
				"title": "click missing target",
				"steps": []map[string]any{{"action": "click"}},
			},
		},
		{
			name: "fill missing value",
			blueprint: map[string]any{
				"title": "fill missing value",
				"steps": []map[string]any{{"action": "fill", "target": map[string]any{"placeholder": "Email"}}},
			},
		},
		{
			name: "select missing value",
			blueprint: map[string]any{
				"title": "select missing value",
				"steps": []map[string]any{{"action": "select", "target_hint": map[string]any{"label": "Role"}}},
			},
		},
		{
			name: "wait missing target or duration",
			blueprint: map[string]any{
				"title": "wait missing target or duration",
				"steps": []map[string]any{{"action": "wait"}},
			},
		},
		{
			name: "wait duration too large",
			blueprint: map[string]any{
				"title": "wait duration too large",
				"steps": []map[string]any{{"action": "wait", "duration_ms": 30001}},
			},
		},
		{
			name: "step timeout too large",
			blueprint: map[string]any{
				"title": "step timeout too large",
				"steps": []map[string]any{{"action": "expect_text", "text": "Done", "timeout_ms": 60001}},
			},
		},
		{
			name: "expect visible missing target",
			blueprint: map[string]any{
				"title": "expect visible missing target",
				"steps": []map[string]any{{"action": "expect_visible"}},
			},
		},
		{
			name: "expect text missing expected text",
			blueprint: map[string]any{
				"title": "expect text missing expected text",
				"steps": []map[string]any{{"action": "expect_text", "target": map[string]any{"selector": ".message"}}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			runner := newFailOnCallTestCaseRunner(t)
			env.installFakeTestCaseRunner(t, runner)
			project, version, page := env.seedProjectVersionPage(t)
			testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
				Title:     tc.name,
				Status:    "active",
				Blueprint: tc.blueprint,
			})
			before := env.snapshotTestCase(t, testCase.ID)

			res := env.runTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{})

			env.requireStatus(t, res, http.StatusBadRequest)
			env.requireJSONError(t, res)
			env.requireTestExecutionCount(t, testCase.ID, 0)
			env.requireTestCaseUnchanged(t, before, "invalid executable blueprint must not mutate TestCase")
			if runner.CallCount() != 0 {
				t.Fatalf("fake runner calls = %d, want 0 for invalid executable blueprint", runner.CallCount())
			}
		})
	}
}

func TestRunTestCasePersistsExecutionStatusesAndKeepsTestCaseStatusActive(t *testing.T) {
	cases := []struct {
		name             string
		blueprint        map[string]any
		runnerResult     contractRunTestCaseResult
		wantFailedIndex  any
		wantStepStatus   string
		wantStepHasError bool
	}{
		{
			name: "passed execution",
			blueprint: map[string]any{
				"title": "passed execution",
				"steps": []map[string]any{{
					"action":      "wait",
					"duration_ms": 1,
				}},
			},
			runnerResult:    contractFakePassedResult(),
			wantFailedIndex: nil,
			wantStepStatus:  "passed",
		},
		{
			name: "failed assertion execution",
			blueprint: map[string]any{
				"title": "failed assertion execution",
				"steps": []map[string]any{{
					"action": "expect_text",
					"text":   "contract text that is absent",
				}},
			},
			runnerResult:     contractFakeFailedResult(),
			wantFailedIndex:  float64(0),
			wantStepStatus:   "failed",
			wantStepHasError: true,
		},
		{
			name: "runtime error execution",
			blueprint: map[string]any{
				"title": "runtime error execution",
				"steps": []map[string]any{{
					"action": "click",
					"target": map[string]any{"selector": "#contract-runtime-error"},
				}},
			},
			runnerResult:     contractFakeErrorResult(),
			wantFailedIndex:  float64(0),
			wantStepStatus:   "error",
			wantStepHasError: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newContractFakeTestCaseRunner(t, tc.runnerResult)
			env := newGenerateContractEnv(t)
			env.installFakeTestCaseRunner(t, runner)
			project, version, page := env.seedProjectVersionPage(t)
			testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
				Title:     tc.name,
				Status:    "active",
				Blueprint: tc.blueprint,
			})
			before := env.snapshotTestCase(t, testCase.ID)

			res := env.runTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
				"capture_screenshot": true,
			})

			env.requireStatus(t, res, http.StatusOK)
			detail := env.decodeTestExecutionDetail(t, res)
			if detail["status"] != tc.runnerResult.Status {
				t.Fatalf("response status = %v, want %s", detail["status"], tc.runnerResult.Status)
			}
			if detail["test_case_id"] != float64(testCase.ID) {
				t.Fatalf("response test_case_id = %v, want %d", detail["test_case_id"], testCase.ID)
			}
			report := env.requireExecutionReportDiagnostics(t, detail, tc.wantFailedIndex, tc.wantStepStatus, tc.wantStepHasError)

			execution := env.requireSingleTestExecution(t, testCase.ID)
			if execution.Status != tc.runnerResult.Status {
				t.Fatalf("stored execution status = %q, want %q", execution.Status, tc.runnerResult.Status)
			}
			if tc.runnerResult.Status != "passed" && execution.ErrorMessage == "" {
				t.Fatalf("stored error_message is empty for %s execution", tc.runnerResult.Status)
			}
			if execution.ReportData == "" {
				t.Fatalf("stored report_data is empty")
			}
			env.requireStoredReportMatchesResponse(t, execution.ReportData, report)
			after := env.snapshotTestCase(t, testCase.ID)
			before.Status = "active"
			if after.Status != "active" {
				t.Fatalf("TestCase status = %q, want active after %s execution", after.Status, tc.runnerResult.Status)
			}
			env.requireTestCaseUnchanged(t, before, "execution result status must not mutate TestCase asset status or content")
			if runner.CallCount() != 1 {
				t.Fatalf("fake runner calls = %d, want 1", runner.CallCount())
			}
		})
	}
}

func TestRunTestCaseBlueprintCompatibilityAndNavigationReport(t *testing.T) {
	cases := []struct {
		name                  string
		blueprint             map[string]any
		wantNavigationMode    string
		wantNavigationStep    any
		wantFirstTargetPrefix string
	}{
		{
			name: "default navigation and target hint",
			blueprint: map[string]any{
				"title": "default navigation and target hint",
				"steps": []map[string]any{{
					"action":      "fill",
					"target_hint": map[string]any{"placeholder": "Email"},
					"text":        "alice@example.invalid",
				}},
			},
			wantNavigationMode:    "default",
			wantNavigationStep:    nil,
			wantFirstTargetPrefix: "placeholder:",
		},
		{
			name: "explicit navigate skips default navigation",
			blueprint: map[string]any{
				"title": "explicit navigate skips default navigation",
				"steps": []map[string]any{
					{"action": "navigate", "url": "/custom-login"},
					{"action": "expect_text", "text": "Login"},
				},
			},
			wantNavigationMode: "explicit_step",
			wantNavigationStep: float64(0),
		},
		{
			name: "role and text target wins over plain text",
			blueprint: map[string]any{
				"title": "role and text target wins over plain text",
				"steps": []map[string]any{{
					"action": "click",
					"target": map[string]any{
						"role":     "button",
						"text":     "Save",
						"selector": ".less-preferred-selector",
					},
				}},
			},
			wantNavigationMode:    "default",
			wantNavigationStep:    nil,
			wantFirstTargetPrefix: "role+text:",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newContractFakeTestCaseRunner(t)
			runner.expectNormalizedInput(contractExpectedRunnerInput{
				initialNavigationMode:      tc.wantNavigationMode,
				initialNavigationStepIndex: tc.wantNavigationStep,
				firstTargetSummaryPrefix:   tc.wantFirstTargetPrefix,
			})
			env := newGenerateContractEnv(t)
			env.installFakeTestCaseRunner(t, runner)
			project, version, page := env.seedProjectVersionPage(t)
			testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
				Title:     tc.name,
				Status:    "active",
				Blueprint: tc.blueprint,
			})

			res := env.runTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
				"capture_screenshot": true,
			})

			env.requireStatus(t, res, http.StatusOK)
			detail := env.decodeTestExecutionDetail(t, res)
			report := detail["report_data"].(map[string]any)
			if report["source"] != "blueprint" {
				t.Fatalf("report source = %v, want blueprint", report["source"])
			}
			initialNavigation, ok := report["initial_navigation"].(map[string]any)
			if !ok {
				t.Fatalf("initial_navigation = %T, want object; report: %v", report["initial_navigation"], report)
			}
			if initialNavigation["mode"] != tc.wantNavigationMode {
				t.Fatalf("initial_navigation.mode = %v, want %s", initialNavigation["mode"], tc.wantNavigationMode)
			}
			if initialNavigation["step_index"] != tc.wantNavigationStep {
				t.Fatalf("initial_navigation.step_index = %v, want %v", initialNavigation["step_index"], tc.wantNavigationStep)
			}
			if tc.wantFirstTargetPrefix != "" {
				steps := report["steps"].([]any)
				first := steps[0].(map[string]any)
				targetSummary := fmt.Sprint(first["target_summary"])
				if !strings.HasPrefix(targetSummary, tc.wantFirstTargetPrefix) {
					t.Fatalf("target_summary = %q, want prefix %q", targetSummary, tc.wantFirstTargetPrefix)
				}
			}
			env.requireTestExecutionCount(t, testCase.ID, 1)
			if runner.CallCount() != 1 {
				t.Fatalf("fake runner calls = %d, want 1", runner.CallCount())
			}
		})
	}
}

func TestListTestCaseExecutionsScopesSortsAndOmitsReportData(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	otherPage := env.seedPageInVersion(t, version.ID, "execution list sibling page")
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "execution owner", Status: "active"})
	otherCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "same page other case", Status: "active"})
	otherPageCase := env.seedCustomTestCase(t, otherPage.ID, testCaseSeed{Title: "other page case", Status: "active"})
	foreignCase := env.seedCustomTestCase(t, foreignPage.ID, testCaseSeed{Title: "foreign case", Status: "active"})

	older := env.seedTestExecution(t, testCase.ID, testExecutionSeed{Status: "passed", CreatedAt: time.Now().Add(-2 * time.Hour)})
	sameTime := time.Now().Add(-time.Hour)
	tieOlderID := env.seedTestExecution(t, testCase.ID, testExecutionSeed{Status: "failed", CreatedAt: sameTime})
	tieNewerID := env.seedTestExecution(t, testCase.ID, testExecutionSeed{Status: "error", CreatedAt: sameTime})
	env.seedTestExecution(t, otherCase.ID, testExecutionSeed{Status: "passed", CreatedAt: time.Now()})
	env.seedTestExecution(t, otherPageCase.ID, testExecutionSeed{Status: "passed", CreatedAt: time.Now()})
	env.seedTestExecution(t, foreignCase.ID, testExecutionSeed{Status: "passed", CreatedAt: time.Now()})

	res := env.listTestCaseExecutions(t, project.ID, version.ID, page.ID, testCase.ID, "")

	env.requireStatus(t, res, http.StatusOK)
	body := env.decodeObject(t, res)
	if body["count"] != float64(3) {
		t.Fatalf("count = %v, want 3", body["count"])
	}
	items, ok := body["executions"].([]any)
	if !ok {
		t.Fatalf("executions = %T, want array", body["executions"])
	}
	if len(items) != 3 {
		t.Fatalf("len(executions) = %d, want 3", len(items))
	}
	first := items[0].(map[string]any)
	second := items[1].(map[string]any)
	third := items[2].(map[string]any)
	if uint(first["id"].(float64)) != tieNewerID.ID || uint(second["id"].(float64)) != tieOlderID.ID || uint(third["id"].(float64)) != older.ID {
		t.Fatalf("execution order ids = [%v, %v, %v], want [%d, %d, %d]", first["id"], second["id"], third["id"], tieNewerID.ID, tieOlderID.ID, older.ID)
	}
	for _, item := range items {
		obj := item.(map[string]any)
		if uint(obj["test_case_id"].(float64)) != testCase.ID {
			t.Fatalf("execution test_case_id = %v, want %d", obj["test_case_id"], testCase.ID)
		}
		if _, exists := obj["report_data"]; exists {
			t.Fatalf("execution summary leaked report_data: %v", obj)
		}
	}

	limitRes := env.listTestCaseExecutions(t, project.ID, version.ID, page.ID, testCase.ID, "limit=2")
	env.requireStatus(t, limitRes, http.StatusOK)
	limitItems := env.decodeObject(t, limitRes)["executions"].([]any)
	if len(limitItems) != 2 {
		t.Fatalf("limit=2 returned %d executions, want 2", len(limitItems))
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
			res := env.listTestCaseExecutions(t, tc.projectID, tc.versionID, tc.pageID, tc.testCaseID, "")
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
		})
	}
}

func TestGetTestCaseExecutionRequiresHierarchyAndParsesReportData(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	siblingPage := env.seedPageInVersion(t, version.ID, "execution detail sibling page")
	otherProject, otherVersion, foreignPage := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "execution detail owner", Status: "active"})
	otherCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "same page other case", Status: "active"})
	execution := env.seedTestExecution(t, testCase.ID, testExecutionSeed{
		Status:       "failed",
		ErrorMessage: "step 1 expect_text failed",
		ReportData: map[string]any{
			"schema_version": 1,
			"source":         "blueprint",
			"summary": map[string]any{
				"total_steps":       2,
				"passed_steps":      1,
				"failed_steps":      1,
				"failed_step_index": 1,
			},
			"steps": []map[string]any{{"index": 1, "action": "expect_text", "status": "failed"}},
		},
	})
	otherExecution := env.seedTestExecution(t, otherCase.ID, testExecutionSeed{Status: "passed"})

	res := env.getTestCaseExecution(t, project.ID, version.ID, page.ID, testCase.ID, execution.ID)

	env.requireStatus(t, res, http.StatusOK)
	detail := env.decodeTestExecutionDetail(t, res)
	if detail["id"] != float64(execution.ID) || detail["test_case_id"] != float64(testCase.ID) {
		t.Fatalf("execution ids = %v, want id=%d test_case_id=%d", detail, execution.ID, testCase.ID)
	}
	if _, exists := detail["report_data"].(map[string]any); !exists {
		t.Fatalf("report_data = %T, want parsed object", detail["report_data"])
	}

	cases := []struct {
		name        string
		projectID   uint
		versionID   uint
		pageID      uint
		testCaseID  uint
		executionID uint
	}{
		{"wrong project", otherProject.ID, version.ID, page.ID, testCase.ID, execution.ID},
		{"wrong version", project.ID, otherVersion.ID, page.ID, testCase.ID, execution.ID},
		{"page belongs to another version", project.ID, version.ID, foreignPage.ID, testCase.ID, execution.ID},
		{"testcase belongs to sibling page", project.ID, version.ID, siblingPage.ID, testCase.ID, execution.ID},
		{"execution belongs to another testcase", project.ID, version.ID, page.ID, testCase.ID, otherExecution.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := env.getTestCaseExecution(t, tc.projectID, tc.versionID, tc.pageID, tc.testCaseID, tc.executionID)
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
		})
	}
}

func TestGetTestCaseExecutionRejectsCorruptReportData(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "corrupt report owner", Status: "active"})
	execution := env.seedTestExecution(t, testCase.ID, testExecutionSeed{Status: "error"})
	if err := env.db.Model(&models.TestExecution{}).Where("id = ?", execution.ID).Update("report_data", "{not-json").Error; err != nil {
		t.Fatalf("corrupt report_data: %v", err)
	}

	res := env.getTestCaseExecution(t, project.ID, version.ID, page.ID, testCase.ID, execution.ID)

	env.requireStatus(t, res, http.StatusInternalServerError)
	env.requireJSONError(t, res)
}

type testExecutionSeed struct {
	Status       string
	ErrorMessage string
	DurationMs   int
	ReportData   map[string]any
	CreatedAt    time.Time
}

type contractRunTestCaseInput map[string]any

type contractRunTestCaseResult struct {
	Status       string
	ErrorMessage string
	DurationMs   int
	ReportData   map[string]any
}

type contractFakeTestCaseRunner struct {
	t        *testing.T
	results  []contractRunTestCaseResult
	expected *contractExpectedRunnerInput
	failCall bool
	calls    int
}

type contractExpectedRunnerInput struct {
	initialNavigationMode      string
	initialNavigationStepIndex any
	firstTargetSummaryPrefix   string
}

func newContractFakeTestCaseRunner(t *testing.T, results ...contractRunTestCaseResult) *contractFakeTestCaseRunner {
	t.Helper()
	return &contractFakeTestCaseRunner{t: t, results: results}
}

func newFailOnCallTestCaseRunner(t *testing.T) *contractFakeTestCaseRunner {
	t.Helper()
	return &contractFakeTestCaseRunner{t: t, failCall: true}
}

func (r *contractFakeTestCaseRunner) expectNormalizedInput(expected contractExpectedRunnerInput) {
	r.expected = &expected
}

func (r *contractFakeTestCaseRunner) CallCount() int {
	return r.calls
}

func (r *contractFakeTestCaseRunner) Run(_ context.Context, input contractRunTestCaseInput) (contractRunTestCaseResult, error) {
	r.calls++
	if r.failCall {
		r.t.Fatalf("fake runner was called during preflight rejection; input: %v", input)
	}
	r.requireNormalizedInput(input)
	if len(r.results) == 0 {
		if r.expected != nil {
			return contractFakePassedResultFromInput(r.t, input), nil
		}
		return contractFakePassedResult(), nil
	}
	result := r.results[0]
	r.results = r.results[1:]
	return result, nil
}

func (r *contractFakeTestCaseRunner) requireNormalizedInput(input contractRunTestCaseInput) {
	r.t.Helper()
	if r.expected == nil {
		return
	}
	data, err := json.Marshal(input)
	if err != nil {
		r.t.Fatalf("marshal runner input: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		r.t.Fatalf("decode runner input: %v; input: %s", err, data)
	}
	initialNavigation := objectField(r.t, obj, "initial_navigation", "initialNavigation", "InitialNavigation")
	if fmt.Sprint(firstExisting(initialNavigation, "mode", "Mode")) != r.expected.initialNavigationMode {
		r.t.Fatalf("runner input initial_navigation.mode = %v, want %s; input: %v", firstExisting(initialNavigation, "mode", "Mode"), r.expected.initialNavigationMode, obj)
	}
	if fmt.Sprint(firstExisting(initialNavigation, "step_index", "stepIndex", "StepIndex")) != fmt.Sprint(r.expected.initialNavigationStepIndex) {
		r.t.Fatalf("runner input initial_navigation.step_index = %v, want %v; input: %v", firstExisting(initialNavigation, "step_index", "stepIndex", "StepIndex"), r.expected.initialNavigationStepIndex, obj)
	}
	if r.expected.firstTargetSummaryPrefix != "" {
		steps := arrayField(r.t, obj, "steps", "Steps")
		if len(steps) == 0 {
			r.t.Fatalf("runner input steps empty; input: %v", obj)
		}
		firstStep, ok := steps[0].(map[string]any)
		if !ok {
			r.t.Fatalf("runner input first step = %T, want object; input: %v", steps[0], obj)
		}
		targetSummary := fmt.Sprint(firstExisting(firstStep, "target_summary", "targetSummary", "TargetSummary"))
		if !strings.HasPrefix(targetSummary, r.expected.firstTargetSummaryPrefix) {
			r.t.Fatalf("runner input first target_summary = %q, want prefix %q; input: %v", targetSummary, r.expected.firstTargetSummaryPrefix, obj)
		}
	}
}

func contractFakePassedResultFromInput(t *testing.T, input contractRunTestCaseInput) contractRunTestCaseResult {
	t.Helper()
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal runner input: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("decode runner input: %v; input: %s", err, data)
	}
	initialNavigation := objectField(t, obj, "initial_navigation", "initialNavigation", "InitialNavigation")
	steps := arrayField(t, obj, "steps", "Steps")
	reportSteps := make([]map[string]any, 0, len(steps))
	for i, stepValue := range steps {
		step, ok := stepValue.(map[string]any)
		if !ok {
			t.Fatalf("runner input step %d = %T, want object", i, stepValue)
		}
		reportStep := map[string]any{
			"index":  i,
			"status": "passed",
		}
		if action := firstExisting(step, "action", "Action"); action != nil {
			reportStep["action"] = action
		}
		if duration := firstExisting(step, "duration_ms", "durationMs", "DurationMs"); duration != nil {
			reportStep["duration_ms"] = duration
		}
		if targetSummary := firstExisting(step, "target_summary", "targetSummary", "TargetSummary"); targetSummary != nil {
			reportStep["target_summary"] = targetSummary
		}
		reportSteps = append(reportSteps, reportStep)
	}
	return contractRunTestCaseResult{
		Status:     "passed",
		DurationMs: 120,
		ReportData: map[string]any{
			"schema_version":     1,
			"source":             "blueprint",
			"execution_url":      firstExisting(obj, "execution_url", "executionUrl", "ExecutionURL"),
			"initial_navigation": initialNavigation,
			"summary": map[string]any{
				"total_steps":       len(reportSteps),
				"passed_steps":      len(reportSteps),
				"failed_steps":      0,
				"failed_step_index": nil,
			},
			"steps":     reportSteps,
			"final_url": firstExisting(obj, "execution_url", "executionUrl", "ExecutionURL"),
		},
	}
}

func contractFakePassedResult() contractRunTestCaseResult {
	return contractRunTestCaseResult{
		Status:     "passed",
		DurationMs: 120,
		ReportData: map[string]any{
			"schema_version": 1,
			"source":         "blueprint",
			"execution_url":  "https://example.invalid/app/orders",
			"initial_navigation": map[string]any{
				"mode":       "default",
				"url":        "https://example.invalid/app/orders",
				"step_index": nil,
			},
			"summary": map[string]any{
				"total_steps":       1,
				"passed_steps":      1,
				"failed_steps":      0,
				"failed_step_index": nil,
			},
			"steps": []map[string]any{{
				"index":          0,
				"action":         "wait",
				"status":         "passed",
				"duration_ms":    1,
				"target_summary": "duration:1ms",
			}},
			"final_url": "https://example.invalid/app/orders",
		},
	}
}

func contractFakeFailedResult() contractRunTestCaseResult {
	return contractRunTestCaseResult{
		Status:       "failed",
		ErrorMessage: "step 0 expect_text failed: expected text not found",
		DurationMs:   240,
		ReportData: map[string]any{
			"schema_version": 1,
			"source":         "blueprint",
			"execution_url":  "https://example.invalid/app/orders",
			"initial_navigation": map[string]any{
				"mode":       "default",
				"url":        "https://example.invalid/app/orders",
				"step_index": nil,
			},
			"summary": map[string]any{
				"total_steps":       1,
				"passed_steps":      0,
				"failed_steps":      1,
				"failed_step_index": 0,
			},
			"steps": []map[string]any{{
				"index":          0,
				"action":         "expect_text",
				"status":         "failed",
				"error":          "expected text not found",
				"target_summary": "page:text",
			}},
			"final_url": "https://example.invalid/app/orders",
		},
	}
}

func contractFakeErrorResult() contractRunTestCaseResult {
	return contractRunTestCaseResult{
		Status:       "error",
		ErrorMessage: "step 0 click error: element not found",
		DurationMs:   180,
		ReportData: map[string]any{
			"schema_version": 1,
			"source":         "blueprint",
			"execution_url":  "https://example.invalid/app/orders",
			"initial_navigation": map[string]any{
				"mode":       "default",
				"url":        "https://example.invalid/app/orders",
				"step_index": nil,
			},
			"summary": map[string]any{
				"total_steps":       1,
				"passed_steps":      0,
				"failed_steps":      1,
				"failed_step_index": 0,
			},
			"steps": []map[string]any{{
				"index":          0,
				"action":         "click",
				"status":         "error",
				"error":          "element not found",
				"target_summary": "css:#contract-runtime-error",
			}},
			"final_url": "https://example.invalid/app/orders",
		},
	}
}

func executableBlueprint(title string) map[string]any {
	return map[string]any{
		"title":       title,
		"description": "",
		"steps": []map[string]any{{
			"action": "expect_text",
			"text":   "ready",
		}},
	}
}

func (e *generateContractEnv) installFakeTestCaseRunner(t *testing.T, runner *contractFakeTestCaseRunner) {
	t.Helper()
	if runner == nil {
		t.Fatal("fake TestCase runner is required")
	}
	if e.handler == nil {
		t.Fatal("test environment missing production Handler")
	}
	method := reflect.ValueOf(e.handler).MethodByName("SetTestCaseRunnerForTest")
	if !method.IsValid() {
		t.Fatalf("SetTestCaseRunnerForTest is not available on production Handler; P3 /run tests require fake runner injection through SetupRouter initialization")
	}
	if method.Type().NumIn() != 1 {
		t.Fatalf("SetTestCaseRunnerForTest input count = %d, want 1", method.Type().NumIn())
	}
	arg := reflect.ValueOf(runner)
	want := method.Type().In(0)
	if !arg.Type().AssignableTo(want) {
		if arg.Type().ConvertibleTo(want) {
			arg = arg.Convert(want)
		} else {
			t.Fatalf("fake runner type %s is not assignable to SetTestCaseRunnerForTest(%s)", arg.Type(), want)
		}
	}
	method.Call([]reflect.Value{arg})
}

func firstExisting(obj map[string]any, names ...string) any {
	for _, name := range names {
		if value, ok := obj[name]; ok {
			return value
		}
	}
	return nil
}

func objectField(t *testing.T, obj map[string]any, names ...string) map[string]any {
	t.Helper()
	value := firstExisting(obj, names...)
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("field %v = %T, want object; object: %v", names, value, obj)
	}
	return result
}

func arrayField(t *testing.T, obj map[string]any, names ...string) []any {
	t.Helper()
	value := firstExisting(obj, names...)
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("field %v = %T, want array; object: %v", names, value, obj)
	}
	return result
}

func (e *generateContractEnv) seedTestExecution(t *testing.T, testCaseID uint, seed testExecutionSeed) models.TestExecution {
	t.Helper()
	status := seed.Status
	if status == "" {
		status = "passed"
	}
	duration := seed.DurationMs
	if duration == 0 {
		duration = 123
	}
	report := seed.ReportData
	if report == nil {
		report = map[string]any{
			"schema_version": 1,
			"source":         "blueprint",
			"summary": map[string]any{
				"total_steps":       1,
				"passed_steps":      1,
				"failed_steps":      0,
				"failed_step_index": nil,
			},
			"steps": []map[string]any{{"index": 0, "action": "expect_text", "status": "passed"}},
		}
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report_data: %v", err)
	}
	createdAt := seed.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	execution := models.TestExecution{
		TestCaseID:   testCaseID,
		Status:       status,
		ErrorMessage: seed.ErrorMessage,
		DurationMs:   duration,
		ReportData:   string(reportJSON),
		CreatedAt:    createdAt,
	}
	if err := e.db.Create(&execution).Error; err != nil {
		t.Fatalf("seed TestExecution: %v", err)
	}
	return execution
}

func (e *generateContractEnv) requireTestExecutionCount(t *testing.T, testCaseID uint, want int64) {
	t.Helper()
	var count int64
	if err := e.db.Model(&models.TestExecution{}).Where("test_case_id = ?", testCaseID).Count(&count).Error; err != nil {
		t.Fatalf("count TestExecution rows: %v", err)
	}
	if count != want {
		t.Fatalf("TestExecution count for testcase %d = %d, want %d", testCaseID, count, want)
	}
}

func (e *generateContractEnv) requireExecutionReportDiagnostics(t *testing.T, detail map[string]any, wantFailedIndex any, wantStepStatus string, wantStepHasError bool) map[string]any {
	t.Helper()
	report, ok := detail["report_data"].(map[string]any)
	if !ok {
		t.Fatalf("response report_data = %T, want parsed object", detail["report_data"])
	}
	summary, ok := report["summary"].(map[string]any)
	if !ok {
		t.Fatalf("report.summary = %T, want object; report: %v", report["summary"], report)
	}
	if summary["failed_step_index"] != wantFailedIndex {
		t.Fatalf("failed_step_index = %v, want %v; summary: %v", summary["failed_step_index"], wantFailedIndex, summary)
	}
	steps, ok := report["steps"].([]any)
	if !ok || len(steps) == 0 {
		t.Fatalf("report.steps = %T len=%d, want non-empty array; report: %v", report["steps"], len(steps), report)
	}
	step, ok := steps[0].(map[string]any)
	if !ok {
		t.Fatalf("first step = %T, want object", steps[0])
	}
	if step["status"] != wantStepStatus {
		t.Fatalf("step status = %v, want %s; step: %v", step["status"], wantStepStatus, step)
	}
	errorValue := strings.TrimSpace(fmt.Sprint(step["error"]))
	if wantStepHasError && errorValue == "" {
		t.Fatalf("step error is empty for %s step: %v", wantStepStatus, step)
	}
	if !wantStepHasError && errorValue != "" {
		t.Fatalf("step error = %q, want empty for %s step", errorValue, wantStepStatus)
	}
	if wantStepHasError && strings.TrimSpace(fmt.Sprint(detail["error_message"])) == "" {
		t.Fatalf("response error_message is empty for %s execution: %v", detail["status"], detail)
	}
	return report
}

func (e *generateContractEnv) requireStoredReportMatchesResponse(t *testing.T, stored string, responseReport map[string]any) {
	t.Helper()
	var storedReport map[string]any
	if err := json.Unmarshal([]byte(stored), &storedReport); err != nil {
		t.Fatalf("stored report_data is invalid JSON: %v; report_data: %s", err, stored)
	}
	if fmt.Sprint(storedReport["source"]) != fmt.Sprint(responseReport["source"]) {
		t.Fatalf("stored report source = %v, response source = %v", storedReport["source"], responseReport["source"])
	}
	storedSummary, ok := storedReport["summary"].(map[string]any)
	if !ok {
		t.Fatalf("stored report summary = %T, want object", storedReport["summary"])
	}
	responseSummary, ok := responseReport["summary"].(map[string]any)
	if !ok {
		t.Fatalf("response report summary = %T, want object", responseReport["summary"])
	}
	if fmt.Sprint(storedSummary["failed_step_index"]) != fmt.Sprint(responseSummary["failed_step_index"]) {
		t.Fatalf("stored failed_step_index = %v, response failed_step_index = %v", storedSummary["failed_step_index"], responseSummary["failed_step_index"])
	}
}

func (e *generateContractEnv) requireSingleTestExecution(t *testing.T, testCaseID uint) models.TestExecution {
	t.Helper()
	var executions []models.TestExecution
	if err := e.db.Where("test_case_id = ?", testCaseID).Order("id asc").Find(&executions).Error; err != nil {
		t.Fatalf("load TestExecution rows: %v", err)
	}
	if len(executions) != 1 {
		t.Fatalf("TestExecution count for testcase %d = %d, want 1: %+v", testCaseID, len(executions), executions)
	}
	return executions[0]
}

func (e *generateContractEnv) runTestCase(t *testing.T, projectID, versionID, pageID, testCaseID uint, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d/run", projectID, versionID, pageID, testCaseID)
	return e.performExecutionRequest(t, http.MethodPost, path, payload)
}

func (e *generateContractEnv) listTestCaseExecutions(t *testing.T, projectID, versionID, pageID, testCaseID uint, query string) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d/executions", projectID, versionID, pageID, testCaseID)
	if query != "" {
		path += "?" + query
	}
	return e.performExecutionRequest(t, http.MethodGet, path, nil)
}

func (e *generateContractEnv) getTestCaseExecution(t *testing.T, projectID, versionID, pageID, testCaseID, executionID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/%d/executions/%d", projectID, versionID, pageID, testCaseID, executionID)
	return e.performExecutionRequest(t, http.MethodGet, path, nil)
}

func (e *generateContractEnv) performExecutionRequest(t *testing.T, method, path string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal execution request payload: %v", err)
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

func (e *generateContractEnv) decodeTestExecutionDetail(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := e.decodeObject(t, res)
	execution, ok := body["execution"].(map[string]any)
	if !ok {
		t.Fatalf("execution = %T, want object; body: %v", body["execution"], body)
	}
	return execution
}
