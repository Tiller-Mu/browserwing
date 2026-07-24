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
	"sync"
	"testing"
	"time"

	"github.com/browserwing/browserwing/models"
	browserSvc "github.com/browserwing/browserwing/services/browser"
	"gorm.io/gorm"
)

const (
	contractSecretCookieValue  = "secret-cookie-value"
	contractSecretLocalToken   = "secret-local-token"
	contractSecretSessionToken = "secret-session-token"
)

func TestCaptureProjectAuthStateScopesToProjectVersionAndRedactsValues(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	sameProjectOtherVersion := env.seedVersionInProject(t, project.ID, "v2-auth-state-isolation")
	_, foreignVersion, _ := env.seedProjectVersionPage(t)
	fake := newContractP45Runtime()
	fake.nextStorageState = contractStorageState("https://example.invalid/app/dashboard")
	env.installProjectAuthRuntimeFake(t, fake)

	res := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
		"name":             "default project auth",
		"captured_page_id": page.ID,
		"captured_url":     "https://example.invalid/app/dashboard",
		"origin_allowlist": []string{"https://example.invalid"},
		"replace":          true,
	})

	env.requireStatus(t, res, http.StatusOK)
	requireBodyOmitsAuthSecrets(t, res)
	body := env.decodeObject(t, res)
	authState := p45ObjectField(t, body, "auth_state")
	if authState["cookie_count"] != float64(1) || authState["origin_count"] != float64(1) {
		t.Fatalf("auth_state counts = %v, want one cookie and one origin", authState)
	}
	if authState["captured_url"] != "https://example.invalid/app/dashboard" {
		t.Fatalf("captured_url = %v", authState["captured_url"])
	}

	summary := env.getProjectAuthState(t, project.ID, version.ID)
	env.requireStatus(t, summary, http.StatusOK)
	requireBodyOmitsAuthSecrets(t, summary)

	sameProjectOtherSummary := env.getProjectAuthState(t, project.ID, sameProjectOtherVersion.ID)
	env.requireStatus(t, sameProjectOtherSummary, http.StatusOK)
	requireBodyOmitsAuthSecrets(t, sameProjectOtherSummary)
	sameProjectOtherBody := env.decodeObject(t, sameProjectOtherSummary)
	if _, ok := sameProjectOtherBody["auth_state"]; !ok {
		t.Fatalf("same-project other version response missing auth_state: %v", sameProjectOtherBody)
	}
	if sameProjectOtherBody["auth_state"] != nil {
		t.Fatalf("same-project other version auth_state = %v, want null", sameProjectOtherBody["auth_state"])
	}

	foreignVersionSummary := env.getProjectAuthState(t, project.ID, foreignVersion.ID)
	env.requireStatus(t, foreignVersionSummary, http.StatusNotFound)
	env.requireJSONError(t, foreignVersionSummary)
}

func TestDeleteProjectAuthStateScopesAndMakesProjectSavedRunFail(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	sameProjectOtherVersion := env.seedVersionInProject(t, project.ID, "v2-delete-isolation")
	sameProjectOtherPage := env.seedPageInVersion(t, sameProjectOtherVersion.ID, "delete auth state other version")
	otherProject, otherVersion, _ := env.seedProjectVersionPage(t)
	auth := env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
	sameProjectOtherAuth := env.seedProjectAuthState(t, project.ID, sameProjectOtherVersion.ID, sameProjectOtherPage.ID, contractStorageState("https://example.invalid/app/other-version"))

	for _, tc := range []struct {
		name      string
		projectID uint
		versionID uint
	}{
		{"wrong project", otherProject.ID, version.ID},
		{"wrong version", project.ID, otherVersion.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := env.deleteProjectAuthState(t, tc.projectID, tc.versionID)
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
			env.requireProjectAuthStateUnchanged(t, auth)
			env.requireProjectAuthStateUnchanged(t, sameProjectOtherAuth)
		})
	}

	deleted := env.deleteProjectAuthState(t, project.ID, version.ID)
	env.requireStatus(t, deleted, http.StatusOK)
	env.requireProjectAuthStateUnchanged(t, sameProjectOtherAuth)
	sameProjectOtherSummary := env.getProjectAuthState(t, project.ID, sameProjectOtherVersion.ID)
	env.requireStatus(t, sameProjectOtherSummary, http.StatusOK)
	requireBodyOmitsAuthSecrets(t, sameProjectOtherSummary)
	sameProjectOtherBody := env.decodeObject(t, sameProjectOtherSummary)
	sameProjectOtherSummaryAuth := p45ObjectField(t, sameProjectOtherBody, "auth_state")
	gotID, ok := sameProjectOtherSummaryAuth["id"].(float64)
	if !ok || uint(gotID) != sameProjectOtherAuth.ID {
		t.Fatalf("same-project other version auth_state.id = %v, want %d", sameProjectOtherSummaryAuth["id"], sameProjectOtherAuth.ID)
	}

	runner := newContractP45Runner(t)
	runner.failOnCall = true
	env.installAnyTestCaseRunner(t, runner)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:     "deleted auth state requires preflight",
		Status:    "active",
		Blueprint: executableBlueprintWithAuth("deleted auth state requires preflight", "project_saved"),
	})
	run := env.runTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{})
	env.requireStatus(t, run, http.StatusBadRequest)
	env.requireJSONError(t, run)
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d, want 0 after auth state deletion", runner.calls)
	}
	env.requireTestExecutionCount(t, testCase.ID, 0)
}

func TestDeleteVersionDeletesScopedProjectAuthState(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	sameProjectOtherVersion := env.seedVersionInProject(t, project.ID, "v2-delete-version-keeps-auth")
	sameProjectOtherPage := env.seedPageInVersion(t, sameProjectOtherVersion.ID, "delete version other page")
	auth := env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
	sameProjectOtherAuth := env.seedProjectAuthState(t, project.ID, sameProjectOtherVersion.ID, sameProjectOtherPage.ID, contractStorageState("https://example.invalid/app/other-version"))

	res := env.deleteVersion(t, project.ID, version.ID)

	env.requireStatus(t, res, http.StatusOK)
	env.requireProjectAuthStateMissing(t, auth.ID)
	env.requireActiveProjectAuthStateCount(t, project.ID, version.ID, 0)
	env.requireProjectAuthStateUnchanged(t, sameProjectOtherAuth)
	env.requireActiveProjectAuthStateCount(t, project.ID, sameProjectOtherVersion.ID, 1)
}

func TestCaptureProjectAuthStateUsesOnlyTheCurrentPageRecordingSessionScope(t *testing.T) {
	t.Run("normalizes a stopped recording session ID before runtime capture and acknowledgement", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		fake := newContractP45Runtime()
		env.installProjectAuthRuntimeFake(t, fake)

		started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
			"recording_kind": "login_flow",
			"auth_context":   "clean",
		})
		env.requireStatus(t, started, http.StatusOK)
		sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
		if sessionID == "" {
			t.Fatalf("recording_session_id missing: %s", started.Body.String())
		}
		fake.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-login"))
		if err := env.db.Model(&models.RecordingSession{}).Where("id = ?", sessionID).Updates(map[string]any{"status": "stopped"}).Error; err != nil {
			t.Fatalf("mark recording session stopped: %v", err)
		}

		captured := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
			"name":                 "login state after recording",
			"captured_page_id":     page.ID,
			"captured_url":         "https://example.invalid/app/home",
			"recording_session_id": " \t" + sessionID + "\n ",
			"replace":              true,
		})
		env.requireStatus(t, captured, http.StatusOK)
		fake.requireCaptureRecordingSessionID(t, sessionID)
		fake.requireAcknowledgedAuthStorageSnapshot(t, sessionID)
	})

	t.Run("rejects a recording session from a different page", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		otherPage := env.seedPageInVersion(t, version.ID, "other recording page")
		foreignSession := models.RecordingSession{
			ProjectID:     project.ID,
			VersionID:     version.ID,
			PageID:        otherPage.ID,
			RecordingKind: "login_flow",
			AuthContext:   "clean",
			TargetURL:     "https://example.invalid/app/login",
			Status:        "stopped",
			StartedAt:     time.Now().UTC(),
			StoppedAt:     time.Now().UTC(),
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		}
		if err := env.db.Create(&foreignSession).Error; err != nil {
			t.Fatalf("seed foreign recording session: %v", err)
		}
		fake := newContractP45Runtime()
		env.installProjectAuthRuntimeFake(t, fake)

		captured := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
			"captured_page_id":     page.ID,
			"recording_session_id": fmt.Sprint(foreignSession.ID),
			"replace":              true,
		})
		env.requireStatus(t, captured, http.StatusNotFound)
		env.requireJSONError(t, captured)
		fake.requireNoEvent(t, "capture_auth_state")
	})
}

func TestCaptureProjectAuthStateRejectsWhitespaceOnlyRecordingSessionIDWithoutActivePageFallback(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	existingAuth := env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/before"))
	runtime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, runtime)

	captured := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
		"captured_page_id":     page.ID,
		"captured_url":         "https://example.invalid/app/after-login",
		"recording_session_id": " \t\n ",
		"replace":              true,
	})
	env.requireStatus(t, captured, http.StatusBadRequest)
	body := env.decodeObject(t, captured)
	if got := body["code"]; got != "recording_session_id_invalid" {
		t.Fatalf("error code = %v, want recording_session_id_invalid", got)
	}
	runtime.requireNoEvent(t, "capture_auth_state")
	env.requireProjectAuthStateUnchanged(t, existingAuth)
}

func TestCaptureProjectAuthStateBoundSessionRejectsIneligibleSessionsWithoutConsumingSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        string
		authContext string
		status      string
		code        string
	}{
		{name: "business flow", kind: "business_flow", authContext: "clean", status: "stopped", code: "recording_session_auth_capture_not_allowed"},
		{name: "non clean login", kind: "login_flow", authContext: "project_saved", status: "stopped", code: "recording_session_auth_capture_not_allowed"},
		{name: "still recording", kind: "login_flow", authContext: "clean", status: "recording", code: "recording_session_auth_capture_not_ready"},
		{name: "cancelled", kind: "login_flow", authContext: "clean", status: "cancelled", code: "recording_session_auth_capture_not_ready"},
		{name: "failed", kind: "login_flow", authContext: "clean", status: "failed", code: "recording_session_auth_capture_not_ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			existingAuth := env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/before"))
			runtime := newContractP45Runtime()
			env.installProjectAuthRuntimeFake(t, runtime)

			now := time.Now().UTC()
			session := models.RecordingSession{
				ProjectID:     project.ID,
				VersionID:     version.ID,
				PageID:        page.ID,
				RecordingKind: tc.kind,
				AuthContext:   tc.authContext,
				TargetURL:     "https://example.invalid/app/recording",
				Status:        tc.status,
				StartedAt:     now,
				StoppedAt:     now,
				CreatedAt:     now,
				UpdatedAt:     now,
			}
			if err := env.db.Create(&session).Error; err != nil {
				t.Fatalf("seed recording session: %v", err)
			}
			sessionID := fmt.Sprint(session.ID)
			runtime.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-login"))

			captured := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
				"captured_page_id":     page.ID,
				"captured_url":         "https://example.invalid/app/after-login",
				"recording_session_id": sessionID,
				"replace":              true,
			})
			env.requireStatus(t, captured, http.StatusConflict)
			body := env.decodeObject(t, captured)
			if got := body["code"]; got != tc.code {
				t.Fatalf("error code = %v, want %q", got, tc.code)
			}
			runtime.requireNoEvent(t, "capture_auth_state")
			runtime.requirePendingAuthStorageSnapshot(t, sessionID)
			env.requireProjectAuthStateUnchanged(t, existingAuth)
		})
	}
}

func TestBrowserProjectAuthRuntimeUsesBoundSessionSnapshotBeforeActivePage(t *testing.T) {
	wantScope := browserSvc.RecordingStorageScope{
		ProjectID:          1,
		VersionID:          2,
		PageID:             3,
		RecordingSessionID: "42",
	}
	snapshot := contractStorageState("https://example.invalid/page-a-after-login")
	activePageReads := 0
	runtime := &browserProjectAuthRuntime{
		captureActiveProjectAuthStateFn: func(context.Context, map[string]any) (map[string]any, error) {
			activePageReads++
			return contractStorageState("https://example.invalid/page-b-active"), nil
		},
		peekRecordingStorageStateFn: func(scope browserSvc.RecordingStorageScope) map[string]any {
			if scope != wantScope {
				t.Fatalf("snapshot scope = %+v, want %+v", scope, wantScope)
			}
			return snapshot
		},
	}

	state, err := runtime.CaptureProjectAuthState(context.Background(), map[string]any{
		"project_id":           wantScope.ProjectID,
		"version_id":           wantScope.VersionID,
		"captured_page_id":     wantScope.PageID,
		"recording_session_id": wantScope.RecordingSessionID,
	})
	if err != nil {
		t.Fatalf("CaptureProjectAuthState: %v", err)
	}
	if activePageReads != 0 {
		t.Fatalf("active page was read %d times for a bound recording session", activePageReads)
	}
	if got := stringFromAny(state["captured_url"]); got != "https://example.invalid/page-a-after-login" {
		t.Fatalf("captured_url = %q, want session A snapshot", got)
	}
}

func TestBrowserProjectAuthRuntimeRejectsBoundSessionWithoutMatchingSnapshot(t *testing.T) {
	activePageReads := 0
	runtime := &browserProjectAuthRuntime{
		captureActiveProjectAuthStateFn: func(context.Context, map[string]any) (map[string]any, error) {
			activePageReads++
			return contractStorageState("https://example.invalid/page-b-active"), nil
		},
		peekRecordingStorageStateFn: func(browserSvc.RecordingStorageScope) map[string]any {
			return nil
		},
	}

	_, err := runtime.CaptureProjectAuthState(context.Background(), map[string]any{
		"project_id":           1,
		"version_id":           2,
		"captured_page_id":     3,
		"recording_session_id": "42",
	})
	if err == nil {
		t.Fatal("bound session capture unexpectedly fell back to the active page")
	}
	if activePageReads != 0 {
		t.Fatalf("active page was read %d times without a matching snapshot", activePageReads)
	}
}

func TestCaptureProjectAuthStateKeepsBoundSnapshotUntilDurablySaved(t *testing.T) {
	cases := []struct {
		name            string
		failRuntimeSave bool
		failTransaction bool
	}{
		{
			name:            "runtime save failure",
			failRuntimeSave: true,
		},
		{
			name:            "auth state transaction failure",
			failTransaction: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			runtime := newContractP45Runtime()
			env.installProjectAuthRuntimeFake(t, runtime)

			started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
				"recording_kind": "login_flow",
				"auth_context":   "clean",
			})
			env.requireStatus(t, started, http.StatusOK)
			sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
			runtime.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-login"))
			if err := env.db.Model(&models.RecordingSession{}).Where("id = ?", sessionID).Updates(map[string]any{"status": "stopped"}).Error; err != nil {
				t.Fatalf("mark recording session stopped: %v", err)
			}
			if tc.failRuntimeSave {
				runtime.failNextProjectAuthStateSave()
			}
			if tc.failTransaction {
				env.failNextProjectAuthStateCreate(t)
			}
			payload := map[string]any{
				"name":                 "retryable login state",
				"captured_page_id":     page.ID,
				"captured_url":         "https://example.invalid/app/after-login",
				"recording_session_id": sessionID,
				"replace":              true,
			}

			failed := env.captureProjectAuthState(t, project.ID, version.ID, payload)
			env.requireStatus(t, failed, http.StatusInternalServerError)
			runtime.requirePendingAuthStorageSnapshot(t, sessionID)

			retry := env.captureProjectAuthState(t, project.ID, version.ID, payload)
			env.requireStatus(t, retry, http.StatusOK)
			runtime.requireAcknowledgedAuthStorageSnapshot(t, sessionID)

			exhausted := env.captureProjectAuthState(t, project.ID, version.ID, payload)
			env.requireStatus(t, exhausted, http.StatusConflict)
			if got := env.decodeObject(t, exhausted)["code"]; got != "recording_session_auth_snapshot_unavailable" {
				t.Fatalf("exhausted capture code = %v", got)
			}
		})
	}
}

func TestCancelRecordingSessionDiscardsUncapturedLoginSnapshot(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, runtime)

	started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "login_flow",
		"auth_context":   "clean",
	})
	env.requireStatus(t, started, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
	runtime.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-login"))
	if err := env.db.Model(&models.RecordingSession{}).Where("id = ?", sessionID).Updates(map[string]any{"status": "stopped"}).Error; err != nil {
		t.Fatalf("mark recording session stopped: %v", err)
	}

	cancel := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/cancel",
		project.ID, version.ID, page.ID, sessionID,
	), nil, "")
	env.requireStatus(t, cancel, http.StatusOK)
	runtime.requireDiscardedAuthStorageSnapshot(t, sessionID)

	if _, err := runtime.CaptureProjectAuthState(context.Background(), map[string]any{"recording_session_id": sessionID}); err == nil {
		t.Fatal("cancelled session retained an uncaptured login snapshot")
	}
}

func TestCancelZeroActionBusinessRecordingReleasesSnapshotAndAllowsAnotherRecording(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, runtime)

	started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
	})
	env.requireStatus(t, started, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
	runtime.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-zero-action-recording"))

	stopped := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/stop",
		project.ID, version.ID, page.ID, sessionID,
	), map[string]any{}, "")
	env.requireStatus(t, stopped, http.StatusOK)
	if got := env.decodeObject(t, stopped)["action_count"]; got != float64(0) {
		t.Fatalf("zero-action stop action_count = %v, want 0", got)
	}

	cancelled := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/cancel",
		project.ID, version.ID, page.ID, sessionID,
	), nil, "")
	env.requireStatus(t, cancelled, http.StatusOK)
	if got := env.decodeObject(t, cancelled)["status"]; got != "cancelled" {
		t.Fatalf("zero-action business session status = %v, want cancelled", got)
	}
	runtime.requireDiscardedAuthStorageSnapshot(t, sessionID)

	next := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
	})
	env.requireStatus(t, next, http.StatusOK)
}

func TestSavedLoginRecordingSessionCanDiscardRetainedSnapshotAfterCaptureFailure(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, runtime)

	started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "login_flow",
		"auth_context":   "clean",
	})
	env.requireStatus(t, started, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
	runtime.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-login"))

	stopped := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/stop",
		project.ID, version.ID, page.ID, sessionID,
	), map[string]any{}, "")
	env.requireStatus(t, stopped, http.StatusOK)
	saved := env.savePageRecording(t, project.ID, version.ID, page.ID, map[string]any{
		"name":                 "retained login snapshot",
		"recording_session_id": sessionID,
		"retain_auth_snapshot": true,
		"recording_meta": map[string]any{
			"schema_version": 1,
			"recording_kind": "login_flow",
			"auth_context":   "clean",
			"auth_state_id":  nil,
			"target_url":     "https://example.invalid/app/login",
		},
	})
	env.requireStatus(t, saved, http.StatusOK)
	runtime.requirePendingAuthStorageSnapshot(t, sessionID)

	runtime.failNextProjectAuthStateSave()
	failedCapture := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
		"captured_page_id":     page.ID,
		"captured_url":         "https://example.invalid/app/after-login",
		"recording_session_id": sessionID,
		"replace":              true,
	})
	env.requireStatus(t, failedCapture, http.StatusInternalServerError)
	runtime.requirePendingAuthStorageSnapshot(t, sessionID)

	discarded := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/cancel",
		project.ID, version.ID, page.ID, sessionID,
	), nil, "")
	env.requireStatus(t, discarded, http.StatusOK)
	body := env.decodeObject(t, discarded)
	if got := body["status"]; got != "saved" {
		t.Fatalf("discard-only session status = %v, want saved", got)
	}
	if got, _ := body["auth_snapshot_discarded"].(bool); !got {
		t.Fatalf("discard-only response = %v, want auth_snapshot_discarded=true", body)
	}
	runtime.requireDiscardedAuthStorageSnapshot(t, sessionID)
	env.requireP47RecordingSession(t, map[string]any{
		"project_id": project.ID,
		"version_id": version.ID,
		"page_id":    page.ID,
		"status":     "saved",
	})

	exhausted := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
		"captured_page_id":     page.ID,
		"captured_url":         "https://example.invalid/app/after-login",
		"recording_session_id": sessionID,
		"replace":              true,
	})
	env.requireStatus(t, exhausted, http.StatusConflict)
	if got := env.decodeObject(t, exhausted)["code"]; got != "recording_session_auth_snapshot_unavailable" {
		t.Fatalf("exhausted capture code = %v", got)
	}

	repeatedDiscard := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/cancel",
		project.ID, version.ID, page.ID, sessionID,
	), nil, "")
	env.requireStatus(t, repeatedDiscard, http.StatusConflict)
}

func TestSaveRecordingSessionReleasesOrRetainsAuthSnapshotByIntent(t *testing.T) {
	stopSession := func(t *testing.T, env *generateContractEnv, projectID, versionID, pageID uint, sessionID string) {
		t.Helper()
		stopped := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
			"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/stop",
			projectID, versionID, pageID, sessionID,
		), map[string]any{}, "")
		env.requireStatus(t, stopped, http.StatusOK)
	}
	savePayload := func(sessionID, kind, authContext string, retain bool) map[string]any {
		return map[string]any{
			"name":                 "recording snapshot contract",
			"recording_session_id": sessionID,
			"retain_auth_snapshot": retain,
			"recording_meta": map[string]any{
				"schema_version": 1,
				"recording_kind": kind,
				"auth_context":   authContext,
				"auth_state_id":  nil,
				"target_url":     "https://example.invalid/app/recording",
			},
		}
	}

	for _, tc := range []struct {
		name        string
		kind        string
		authContext string
	}{
		{name: "login save only", kind: "login_flow", authContext: "clean"},
		{name: "business flow", kind: "business_flow", authContext: "clean"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			runtime := newContractP45Runtime()
			env.installProjectAuthRuntimeFake(t, runtime)

			started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
				"recording_kind": tc.kind,
				"auth_context":   tc.authContext,
			})
			env.requireStatus(t, started, http.StatusOK)
			sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
			runtime.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-login"))
			stopSession(t, env, project.ID, version.ID, page.ID, sessionID)

			saved := env.savePageRecording(t, project.ID, version.ID, page.ID, savePayload(sessionID, tc.kind, tc.authContext, false))
			env.requireStatus(t, saved, http.StatusOK)
			runtime.requireDiscardedAuthStorageSnapshot(t, sessionID)
			if _, err := runtime.CaptureProjectAuthState(context.Background(), map[string]any{"recording_session_id": sessionID}); err == nil {
				t.Fatal("saved session retained a snapshot without an explicit capture intent")
			}
		})
	}

	t.Run("login save and capture retains until capture succeeds", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		runtime := newContractP45Runtime()
		env.installProjectAuthRuntimeFake(t, runtime)

		started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
			"recording_kind": "login_flow",
			"auth_context":   "clean",
		})
		env.requireStatus(t, started, http.StatusOK)
		sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
		runtime.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-login"))
		stopSession(t, env, project.ID, version.ID, page.ID, sessionID)

		saved := env.savePageRecording(t, project.ID, version.ID, page.ID, savePayload(sessionID, "login_flow", "clean", true))
		env.requireStatus(t, saved, http.StatusOK)
		runtime.requirePendingAuthStorageSnapshot(t, sessionID)

		captured := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
			"captured_page_id":     page.ID,
			"captured_url":         "https://example.invalid/app/after-login",
			"recording_session_id": sessionID,
			"replace":              true,
		})
		env.requireStatus(t, captured, http.StatusOK)
		runtime.requireAcknowledgedAuthStorageSnapshot(t, sessionID)
	})

	t.Run("business flow cannot retain an auth snapshot", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		runtime := newContractP45Runtime()
		env.installProjectAuthRuntimeFake(t, runtime)

		started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
			"recording_kind": "business_flow",
			"auth_context":   "clean",
		})
		env.requireStatus(t, started, http.StatusOK)
		sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
		runtime.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-business-flow"))
		stopSession(t, env, project.ID, version.ID, page.ID, sessionID)

		rejected := env.savePageRecording(t, project.ID, version.ID, page.ID, savePayload(sessionID, "business_flow", "clean", true))
		env.requireStatus(t, rejected, http.StatusBadRequest)
		runtime.requirePendingAuthStorageSnapshot(t, sessionID)
		env.requireP47RecordingSession(t, map[string]any{
			"project_id": project.ID,
			"version_id": version.ID,
			"page_id":    page.ID,
			"status":     "stopped",
		})
	})

	t.Run("save transaction failure keeps the snapshot", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		runtime := newContractP45Runtime()
		env.installProjectAuthRuntimeFake(t, runtime)

		started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
			"recording_kind": "login_flow",
			"auth_context":   "clean",
		})
		env.requireStatus(t, started, http.StatusOK)
		sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
		runtime.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-login"))
		stopSession(t, env, project.ID, version.ID, page.ID, sessionID)
		env.failNextRecordingSessionUpdate(t)

		failed := env.savePageRecording(t, project.ID, version.ID, page.ID, savePayload(sessionID, "login_flow", "clean", false))
		env.requireStatus(t, failed, http.StatusInternalServerError)
		runtime.requirePendingAuthStorageSnapshot(t, sessionID)
	})
}

func TestSaveRecordingSessionSerializesLifecycleWithCancelAndCapture(t *testing.T) {
	startStoppedSession := func(t *testing.T, env *generateContractEnv, projectID, versionID, pageID uint, kind string) string {
		t.Helper()
		started := env.startPageRecordingSession(t, projectID, versionID, pageID, map[string]any{
			"recording_kind": kind,
			"auth_context":   "clean",
			"target_url":     "https://example.invalid/app/recording",
		})
		env.requireStatus(t, started, http.StatusOK)
		sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
		stopped := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
			"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/stop",
			projectID, versionID, pageID, sessionID,
		), map[string]any{}, "")
		env.requireStatus(t, stopped, http.StatusOK)
		return sessionID
	}
	savePayload := func(sessionID, kind string, retain bool) map[string]any {
		return map[string]any{
			"recording_session_id": sessionID,
			"retain_auth_snapshot": retain,
			"recording_meta": map[string]any{
				"schema_version": 1,
				"recording_kind": kind,
				"auth_context":   "clean",
			},
		}
	}
	asyncJSONRequest := func(t *testing.T, env *generateContractEnv, method, path string, payload map[string]any) <-chan *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal concurrent request: %v", err)
		}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := httptest.NewRequest(method, path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			env.router.ServeHTTP(res, req)
			result <- res
		}()
		return result
	}
	requireBlocked := func(t *testing.T, result <-chan *httptest.ResponseRecorder, operation string) {
		t.Helper()
		select {
		case res := <-result:
			t.Fatalf("%s completed while save held the lifecycle lock: status=%d body=%s", operation, res.Code, res.Body.String())
		case <-time.After(100 * time.Millisecond):
		}
	}

	t.Run("duplicate save and cancel wait for the first save", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		runtime := newContractP45Runtime()
		env.installProjectAuthRuntimeFake(t, runtime)
		sessionID := startStoppedSession(t, env, project.ID, version.ID, page.ID, "business_flow")
		updateEntered, releaseUpdate := env.blockNextRecordingSessionUpdate(t)
		savePath := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/recordings", project.ID, version.ID, page.ID)
		cancelPath := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/cancel", project.ID, version.ID, page.ID, sessionID)

		firstSave := asyncJSONRequest(t, env, http.MethodPost, savePath, savePayload(sessionID, "business_flow", false))
		select {
		case <-updateEntered:
		case <-time.After(time.Second):
			t.Fatal("save did not reach the RecordingSession update barrier")
		}
		secondSave := asyncJSONRequest(t, env, http.MethodPost, savePath, savePayload(sessionID, "business_flow", false))
		cancel := asyncJSONRequest(t, env, http.MethodPost, cancelPath, map[string]any{})
		requireBlocked(t, secondSave, "duplicate save")
		requireBlocked(t, cancel, "cancel")
		releaseUpdate()

		env.requireStatus(t, <-firstSave, http.StatusOK)
		duplicate := <-secondSave
		env.requireStatus(t, duplicate, http.StatusConflict)
		if got := env.decodeObject(t, duplicate)["code"]; got != "recording_session_not_stopped" {
			t.Fatalf("duplicate save code = %v, want recording_session_not_stopped", got)
		}
		env.requireStatus(t, <-cancel, http.StatusConflict)
		env.requireP47RecordingSession(t, map[string]any{
			"project_id": project.ID,
			"version_id": version.ID,
			"page_id":    page.ID,
			"status":     "saved",
		})
	})

	t.Run("bound auth capture waits until save commits and retains its snapshot", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		runtime := newContractP45Runtime()
		env.installProjectAuthRuntimeFake(t, runtime)
		sessionID := startStoppedSession(t, env, project.ID, version.ID, page.ID, "login_flow")
		runtime.setPendingAuthStorageSnapshot(sessionID, contractStorageState("https://example.invalid/app/after-login"))
		updateEntered, releaseUpdate := env.blockNextRecordingSessionUpdate(t)
		savePath := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/recordings", project.ID, version.ID, page.ID)
		capturePath := fmt.Sprintf("/api/v1/projects/%d/versions/%d/auth-state/capture", project.ID, version.ID)

		save := asyncJSONRequest(t, env, http.MethodPost, savePath, savePayload(sessionID, "login_flow", true))
		select {
		case <-updateEntered:
		case <-time.After(time.Second):
			t.Fatal("save did not reach the RecordingSession update barrier")
		}
		capture := asyncJSONRequest(t, env, http.MethodPost, capturePath, map[string]any{
			"captured_page_id":     page.ID,
			"captured_url":         "https://example.invalid/app/after-login",
			"recording_session_id": sessionID,
			"replace":              true,
		})
		requireBlocked(t, capture, "auth capture")
		releaseUpdate()

		env.requireStatus(t, <-save, http.StatusOK)
		env.requireStatus(t, <-capture, http.StatusOK)
		runtime.requireAcknowledgedAuthStorageSnapshot(t, sessionID)
	})
}

func TestProjectSavedRecordingSessionKeepsItsSourceAuthStateID(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	auth := env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
	runtime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, runtime)

	started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "project_saved",
	})
	env.requireStatus(t, started, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))

	var session models.RecordingSession
	if err := env.db.First(&session, sessionID).Error; err != nil {
		t.Fatalf("load RecordingSession: %v", err)
	}
	if session.SourceAuthStateID == nil || *session.SourceAuthStateID != auth.ID {
		t.Fatalf("source_auth_state_id = %v, want %d", session.SourceAuthStateID, auth.ID)
	}

	resumed := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "project_saved",
	})
	env.requireStatus(t, resumed, http.StatusConflict)
	resumedMeta := env.decodeObject(t, resumed)["recording_meta"].(map[string]any)
	if got := uint(resumedMeta["auth_state_id"].(float64)); got != auth.ID {
		t.Fatalf("resumed recording auth_state_id = %d, want original %d", got, auth.ID)
	}

	stopped := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/stop",
		project.ID, version.ID, page.ID, sessionID,
	), map[string]any{}, "")
	env.requireStatus(t, stopped, http.StatusOK)
	saved := env.savePageRecording(t, project.ID, version.ID, page.ID, map[string]any{
		"name":                 "project saved recording",
		"recording_session_id": sessionID,
		"recording_meta": map[string]any{
			"schema_version": 1,
			"recording_kind": "business_flow",
			"auth_context":   "project_saved",
			"auth_state_id":  auth.ID + 999,
			"target_url":     "https://example.invalid/app/orders",
		},
	})
	env.requireStatus(t, saved, http.StatusOK)
	script := env.requireSinglePageScript(t, page.ID)
	meta := p45ParseJSONMap(t, pageScriptRecordingMetaJSON(t, &script))
	if got := uint(meta["auth_state_id"].(float64)); got != auth.ID {
		t.Fatalf("PageScript auth_state_id = %d, want original %d", got, auth.ID)
	}

	for _, tc := range []struct {
		kind        string
		authContext string
	}{
		{kind: "login_flow", authContext: "clean"},
		{kind: "business_flow", authContext: "clean"},
	} {
		t.Run(tc.kind+"-"+tc.authContext, func(t *testing.T) {
			isolated := newGenerateContractEnv(t)
			p, v, pg := isolated.seedProjectVersionPage(t)
			fake := newContractP45Runtime()
			isolated.installProjectAuthRuntimeFake(t, fake)
			response := isolated.startPageRecordingSession(t, p.ID, v.ID, pg.ID, map[string]any{
				"recording_kind": tc.kind,
				"auth_context":   tc.authContext,
			})
			isolated.requireStatus(t, response, http.StatusOK)
			id := strings.TrimSpace(fmt.Sprint(isolated.decodeObject(t, response)["recording_session_id"]))
			var cleanSession models.RecordingSession
			if err := isolated.db.First(&cleanSession, id).Error; err != nil {
				t.Fatalf("load clean RecordingSession: %v", err)
			}
			if cleanSession.SourceAuthStateID != nil {
				t.Fatalf("clean session source_auth_state_id = %v, want nil", *cleanSession.SourceAuthStateID)
			}
		})
	}
}

func TestBrowserProjectAuthRuntimeSkipsUncontrolledDownloadArtifacts(t *testing.T) {
	scope := browserSvc.RecordingStorageScope{
		ProjectID:          1,
		VersionID:          2,
		PageID:             3,
		RecordingSessionID: "4",
	}
	runtime := &browserProjectAuthRuntime{
		stopRecordingWithStorageScopeFn: func(context.Context, browserSvc.RecordingStorageScope) ([]models.ScriptAction, []models.DownloadedFile, error) {
			return []models.ScriptAction{{Type: "click", Selector: "#export"}}, []models.DownloadedFile{{
				FileName: "export.csv",
				FilePath: "C:/uncontrolled/export.csv",
			}}, nil
		},
		recordingArtifactStorageKeyFn: func(models.DownloadedFile) string {
			return ""
		},
	}

	result, err := runtime.StopPageRecording(context.Background(), map[string]any{
		"project_id":           scope.ProjectID,
		"version_id":           scope.VersionID,
		"page_id":              scope.PageID,
		"recording_session_id": scope.RecordingSessionID,
	})
	if err != nil {
		t.Fatalf("StopPageRecording rejected an uncontrolled download: %v", err)
	}
	if actions, ok := result["actions"].([]models.ScriptAction); !ok || len(actions) != 1 || actions[0].Selector != "#export" {
		t.Fatalf("actions = %#v, want preserved click action", result["actions"])
	}
	if artifacts, ok := result["artifacts"].([]map[string]any); !ok || len(artifacts) != 0 {
		t.Fatalf("artifacts = %#v, want no metadata for uncontrolled download", result["artifacts"])
	}
}

func TestP47UncontrolledDownloadDoesNotBlockStopOrCancel(t *testing.T) {
	for _, terminal := range []struct {
		name   string
		route  string
		status string
	}{
		{name: "stop", route: "stop", status: "stopped"},
		{name: "cancel", route: "cancel", status: "cancelled"},
	} {
		t.Run(terminal.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			nextPage := env.seedPageInVersion(t, version.ID, "after uncontrolled download")
			baseRuntime := newContractP45Runtime()
			baseRuntime.stopPublishesPending = true
			browserRuntime := &browserProjectAuthRuntime{
				stopRecordingWithStorageScopeFn: func(context.Context, browserSvc.RecordingStorageScope) ([]models.ScriptAction, []models.DownloadedFile, error) {
					return []models.ScriptAction{{Type: "click", Selector: "#export"}}, []models.DownloadedFile{{
						FileName: "export.csv",
						FilePath: "C:/uncontrolled/export.csv",
					}}, nil
				},
				recordingArtifactStorageKeyFn: func(models.DownloadedFile) string { return "" },
			}
			runtime := &contractRuntimeWithBrowserStop{
				contractP45Runtime: baseRuntime,
				browserRuntime:     browserRuntime,
			}
			env.installProjectAuthRuntimeFake(t, runtime)

			started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
				"recording_kind": "business_flow",
				"auth_context":   "clean",
			})
			env.requireStatus(t, started, http.StatusOK)
			sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
			response := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
				"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/%s",
				project.ID, version.ID, page.ID, sessionID, terminal.route,
			), map[string]any{}, "")
			env.requireStatus(t, response, http.StatusOK)
			baseRuntime.requireAcknowledgedStoppedSession(t, sessionID)
			env.requireP47RecordingSession(t, map[string]any{
				"project_id": project.ID,
				"version_id": version.ID,
				"page_id":    page.ID,
				"status":     terminal.status,
			})

			var artifactCount int64
			if err := env.db.Model(&models.RecordingArtifact{}).Where("recording_session_id = ?", sessionID).Count(&artifactCount).Error; err != nil {
				t.Fatalf("count RecordingArtifact rows: %v", err)
			}
			if artifactCount != 0 {
				t.Fatalf("RecordingArtifact count = %d, want 0 for uncontrolled download", artifactCount)
			}

			next := env.startPageRecordingSession(t, project.ID, version.ID, nextPage.ID, map[string]any{
				"recording_kind": "business_flow",
				"auth_context":   "clean",
			})
			env.requireStatus(t, next, http.StatusOK)
		})
	}
}

func TestCaptureProjectAuthStateRejectsEmptyStateAndKeepsPreviousOnFailure(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	previous := env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
	fake := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, fake)

	t.Run("replace false", func(t *testing.T) {
		fake.nextStorageState = contractStorageState("https://example.invalid/app/new")
		res := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
			"name":             "must not create second active auth",
			"captured_page_id": page.ID,
			"captured_url":     "https://example.invalid/app/new",
			"replace":          false,
		})
		env.requireStatus(t, res, http.StatusBadRequest)
		env.requireJSONError(t, res)
		fake.requireNoEvent(t, "capture_auth_state")
		env.requireProjectAuthStateUnchanged(t, previous)
		env.requireActiveProjectAuthStateCount(t, project.ID, version.ID, 1)
	})

	t.Run("empty state", func(t *testing.T) {
		fake.nextStorageState = map[string]any{
			"schema_version": 1,
			"kind":           "browser_storage_state",
			"origins":        []any{},
			"cookies":        []any{},
			"extensions":     map[string]any{},
		}
		res := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
			"name":             "empty state",
			"captured_page_id": page.ID,
			"captured_url":     "https://example.invalid/app/empty",
			"replace":          true,
		})
		env.requireStatus(t, res, http.StatusBadRequest)
		env.requireJSONError(t, res)
		env.requireProjectAuthStateUnchanged(t, previous)
	})

	t.Run("replace failure", func(t *testing.T) {
		fake.nextStorageState = contractStorageState("https://example.invalid/app/new")
		fake.failNextSave = true
		res := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
			"name":             "must not replace",
			"captured_page_id": page.ID,
			"captured_url":     "https://example.invalid/app/new",
			"replace":          true,
		})
		env.requireStatus(t, res, http.StatusInternalServerError)
		env.requireJSONError(t, res)
		env.requireProjectAuthStateUnchanged(t, previous)
	})
}

func TestStartLoginFlowRecordingAlwaysUsesCleanSession(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
	fake := newContractP45Runtime()
	fake.hasGlobalBrowserCookieStore = true
	env.installProjectAuthRuntimeFake(t, fake)

	res := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "login_flow",
		"auth_context":   "clean",
	})

	env.requireStatus(t, res, http.StatusOK)
	fake.requireEvents(t, "new_clean_context", "open_target_url", "start_recording")
	fake.requireNoEvent(t, "restore_project_auth_state")
	fake.requireNoEvent(t, "load_global_browser_cookie_store")
}

func TestStartBusinessFlowRecordingRequiresAndRestoresProjectAuthState(t *testing.T) {
	t.Run("missing auth state", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		fake := newContractP45Runtime()
		env.installProjectAuthRuntimeFake(t, fake)

		res := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
			"recording_kind": "business_flow",
			"auth_context":   "project_saved",
		})

		env.requireStatus(t, res, http.StatusBadRequest)
		env.requireJSONError(t, res)
		fake.requireNoEvent(t, "open_target_url")
		fake.requireNoEvent(t, "start_recording")
	})

	t.Run("restores before opening target", func(t *testing.T) {
		env := newGenerateContractEnv(t)
		project, version, page := env.seedProjectVersionPage(t)
		env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
		fake := newContractP45Runtime()
		fake.hasGlobalBrowserCookieStore = true
		env.installProjectAuthRuntimeFake(t, fake)

		res := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
			"recording_kind": "business_flow",
			"auth_context":   "project_saved",
		})

		env.requireStatus(t, res, http.StatusOK)
		fake.requireEvents(t, "new_clean_context", "restore_project_auth_state", "open_target_url", "start_recording")
		fake.requireNoEvent(t, "load_global_browser_cookie_store")
	})
}

func TestStartBusinessFlowRecordingCleanDoesNotRestoreAuthState(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
	fake := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, fake)

	res := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
	})

	env.requireStatus(t, res, http.StatusOK)
	fake.requireEvents(t, "new_clean_context", "open_target_url", "start_recording")
	fake.requireNoEvent(t, "restore_project_auth_state")
}

func TestSavePageRecordingPersistsRecordingMetaAndValidatesAuthContext(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)

	login := env.savePageRecording(t, project.ID, version.ID, page.ID, map[string]any{
		"name":         "login recording",
		"action_trace": `{"steps":[{"type":"fill"}]}`,
		"dom_snapshot": `{"elements":[]}`,
		"recording_meta": map[string]any{
			"schema_version": 1,
			"recording_kind": "login_flow",
			"auth_context":   "clean",
			"auth_state_id":  nil,
			"target_url":     "https://example.invalid/app/login",
		},
	})
	env.requireStatus(t, login, http.StatusOK)
	loginScript := env.requireSinglePageScript(t, page.ID)
	loginMeta := p45ParseJSONMap(t, pageScriptRecordingMetaJSON(t, &loginScript))
	if loginMeta["recording_kind"] != "login_flow" || loginMeta["auth_context"] != "clean" {
		t.Fatalf("login recording_meta = %v", loginMeta)
	}

	business := env.savePageRecording(t, project.ID, version.ID, page.ID, map[string]any{
		"name":         "business recording",
		"action_trace": `{"steps":[{"type":"click"}]}`,
		"dom_snapshot": `{"elements":[]}`,
		"recording_meta": map[string]any{
			"schema_version": 1,
			"recording_kind": "business_flow",
			"auth_context":   "project_saved",
			"auth_state_id":  12,
			"target_url":     "https://example.invalid/app/orders",
		},
	})
	env.requireStatus(t, business, http.StatusOK)
	before := env.requireSinglePageScript(t, page.ID)
	beforeMeta := pageScriptRecordingMetaJSON(t, &before)
	if p45ParseJSONMap(t, beforeMeta)["auth_context"] != "project_saved" {
		t.Fatalf("business recording_meta = %s", beforeMeta)
	}

	invalid := env.savePageRecording(t, project.ID, version.ID, page.ID, map[string]any{
		"name":         "invalid recording",
		"action_trace": `{"steps":[]}`,
		"dom_snapshot": `{"elements":[]}`,
		"recording_meta": map[string]any{
			"schema_version": 1,
			"recording_kind": "business_flow",
			"auth_context":   "reuse_browser",
			"target_url":     "https://example.invalid/app/orders",
		},
	})
	env.requireStatus(t, invalid, http.StatusBadRequest)
	env.requireJSONError(t, invalid)
	after := env.requireSinglePageScript(t, page.ID)
	if pageScriptRecordingMetaJSON(t, &after) != beforeMeta {
		t.Fatalf("invalid recording changed existing metadata\nbefore: %s\nafter: %s", beforeMeta, pageScriptRecordingMetaJSON(t, &after))
	}
}

func TestGenerateTestCasesCarriesAuthContextWithoutSendingAuthSecretsToPlaybot(t *testing.T) {
	cases := []struct {
		name        string
		meta        map[string]any
		wantContext string
	}{
		{"login flow clean", recordingMeta("login_flow", "clean"), "clean"},
		{"business flow project saved", recordingMeta("business_flow", "project_saved"), "project_saved"},
		{"business flow clean", recordingMeta("business_flow", "clean"), "clean"},
		{"default clean meta", nil, "clean"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			env.installRecordingFakePlaybotCommand(t)
			project, version, page := env.seedProjectVersionPage(t)
			env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
			if tc.meta == nil {
				env.seedMainFlow(t, page.ID)
			} else {
				env.seedMainFlowWithRecordingMeta(t, page.ID, tc.meta)
			}
			env.setPlaybotStdout(t, validPlaybotOutputWithAuthContext("generated "+tc.name, tc.wantContext))

			res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "append"})

			env.requireStatus(t, res, http.StatusOK)
			input := env.readRecordedPlaybotInput(t)
			requireBodyOmitsAuthSecrets(t, res)
			requireJSONValueOmitsAuthSecrets(t, input)
			source := requireMapField(t, input, "recording_source")
			meta := requireMapField(t, source, "recording_meta")
			if meta["auth_context"] != tc.wantContext {
				t.Fatalf("Playbot job recording_meta.auth_context = %v, want %s; input: %v", meta["auth_context"], tc.wantContext, input)
			}
			var stored models.TestCase
			if err := env.db.Where("page_id = ?", page.ID).First(&stored).Error; err != nil {
				t.Fatalf("load generated TestCase: %v", err)
			}
			blueprint := p45ParseJSONMap(t, stored.Blueprint)
			if blueprint["auth_context"] != tc.wantContext {
				t.Fatalf("stored blueprint auth_context = %v, want %s; blueprint: %v", blueprint["auth_context"], tc.wantContext, blueprint)
			}
		})
	}
}

func TestGenerateTestCasesRejectsInvalidBlueprintAuthContext(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedMainFlow(t, page.ID)
	old := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "old case", Status: "active"})
	before := env.snapshotTestCase(t, old.ID)
	env.setPlaybotStdout(t, validPlaybotOutputWithAuthContext("invalid auth context", "auto"))

	res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "replace"})

	env.requireStatus(t, res, http.StatusBadRequest)
	env.requireJSONError(t, res)
	env.requireTestCaseUnchanged(t, before, "invalid generated auth_context must not replace old cases")
	env.requireTestCaseCount(t, page.ID, 1)
}

func TestGenerateTestCasesRejectsInvalidRecordingMetaAuthContextBeforePlaybot(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedMainFlowWithRecordingMeta(t, page.ID, recordingMeta("business_flow", "auto"))
	old := env.seedCustomTestCase(t, page.ID, testCaseSeed{Title: "old case", Status: "active"})
	before := env.snapshotTestCase(t, old.ID)
	env.setPlaybotStdout(t, validPlaybotOutputWithAuthContext("must not be used", "clean"))
	beforeCalls := env.playbotCalls(t)

	res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{"mode": "replace"})

	env.requireStatus(t, res, http.StatusBadRequest)
	env.requireJSONError(t, res)
	if got := env.playbotCalls(t); got != beforeCalls {
		t.Fatalf("Playbot calls = %d, want unchanged at %d", got, beforeCalls)
	}
	env.requireTestCaseUnchanged(t, before, "invalid recording_meta auth_context must not replace old cases")
	env.requireTestCaseCount(t, page.ID, 1)
}

func TestRunTestCaseCleanOrLegacyAuthContextDoesNotRestoreAuthState(t *testing.T) {
	cases := []struct {
		name        string
		blueprint   map[string]any
		source      string
		wantContext string
	}{
		{"explicit clean", executableBlueprintWithAuth("explicit clean", "clean"), "blueprint", "clean"},
		{"legacy missing auth context", executableBlueprint("legacy clean"), "legacy_default", "clean"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
			fakeRuntime := newContractP45Runtime()
			fakeRuntime.hasGlobalBrowserCookieStore = true
			env.installProjectAuthRuntimeFake(t, fakeRuntime)
			runner := newContractP45Runner(t)
			runner.recordEventsTo(fakeRuntime)
			env.installAnyTestCaseRunner(t, runner)
			testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
				Title:     tc.name,
				Status:    "active",
				Blueprint: tc.blueprint,
			})

			res := env.runTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{})

			env.requireStatus(t, res, http.StatusOK)
			fakeRuntime.requireEventBefore(t, "new_clean_context", "runner_start")
			fakeRuntime.requireNoEvent(t, "restore_project_auth_state")
			fakeRuntime.requireNoEvent(t, "load_global_browser_cookie_store")
			detail := env.decodeTestExecutionDetail(t, res)
			report := p45ObjectField(t, detail, "report_data")
			if report["auth_context"] != tc.wantContext || report["auth_context_source"] != tc.source {
				t.Fatalf("report auth metadata = %v, want context=%s source=%s", report, tc.wantContext, tc.source)
			}
			requireJSONValueOmitsAuthSecrets(t, report)
		})
	}
}

func TestRunTestCaseProjectSavedRequiresAuthStateBeforeRunner(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	fakeRuntime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, fakeRuntime)
	runner := newContractP45Runner(t)
	runner.failOnCall = true
	env.installAnyTestCaseRunner(t, runner)
	testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
		Title:     "requires project auth",
		Status:    "active",
		Blueprint: executableBlueprintWithAuth("requires project auth", "project_saved"),
	})

	res := env.runTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{})

	env.requireStatus(t, res, http.StatusBadRequest)
	env.requireJSONError(t, res)
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d, want 0", runner.calls)
	}
	fakeRuntime.requireNoEvent(t, "new_clean_context")
	env.requireTestExecutionCount(t, testCase.ID, 0)
}

func TestRunTestCaseProjectSavedRestoresAuthStateBeforeNavigation(t *testing.T) {
	cases := []struct {
		name               string
		blueprint          map[string]any
		wantNavigationMode string
	}{
		{"default navigation", executableBlueprintWithAuth("default navigation", "project_saved"), "default"},
		{"explicit navigation", executableNavigateBlueprintWithAuth("explicit navigation", "project_saved"), "explicit_step"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			auth := env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
			fakeRuntime := newContractP45Runtime()
			fakeRuntime.hasGlobalBrowserCookieStore = true
			env.installProjectAuthRuntimeFake(t, fakeRuntime)
			runner := newContractP45Runner(t)
			runner.recordEventsTo(fakeRuntime)
			env.installAnyTestCaseRunner(t, runner)
			testCase := env.seedCustomTestCase(t, page.ID, testCaseSeed{
				Title:     tc.name,
				Status:    "active",
				Blueprint: tc.blueprint,
			})

			res := env.runTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{})

			env.requireStatus(t, res, http.StatusOK)
			fakeRuntime.requireEventBefore(t, "new_clean_context", "restore_project_auth_state")
			fakeRuntime.requireEventBefore(t, "restore_project_auth_state", "runner_start")
			fakeRuntime.requireNoEvent(t, "load_global_browser_cookie_store")
			detail := env.decodeTestExecutionDetail(t, res)
			report := p45ObjectField(t, detail, "report_data")
			initialNavigation := p45ObjectField(t, report, "initial_navigation")
			if initialNavigation["mode"] != tc.wantNavigationMode {
				t.Fatalf("initial_navigation.mode = %v, want %s", initialNavigation["mode"], tc.wantNavigationMode)
			}
			summary := p45ObjectField(t, report, "auth_state")
			if uint(summary["id"].(float64)) != auth.ID {
				t.Fatalf("report auth_state.id = %v, want %d", summary["id"], auth.ID)
			}
			requireJSONValueOmitsAuthSecrets(t, report)
		})
	}
}

type contractP45Runtime struct {
	mu                          sync.Mutex
	nextStorageState            map[string]any
	failNextSave                bool
	hasGlobalBrowserCookieStore bool
	events                      []string
	stopResult                  map[string]any
	stopErr                     error
	stopPublishesPending        bool
	recordingActive             bool
	recordingSessionID          string
	pendingStoppedSessionID     string
	acknowledgedStoppedSessions []string
	pendingAuthStorageState     map[string]any
	pendingAuthStorageSessionID string
	unavailableAuthSessions     map[string]struct{}
	discardedAuthSessions       []string
	acknowledgedAuthSessions    []string
	captureRecordingSessionID   string
	startEntered                chan struct{}
	releaseStart                <-chan struct{}
	stopEntered                 chan struct{}
	releaseStop                 <-chan struct{}
}

type contractRuntimeWithBrowserStop struct {
	*contractP45Runtime
	browserRuntime *browserProjectAuthRuntime
}

func (r *contractRuntimeWithBrowserStop) StopPageRecording(ctx context.Context, input map[string]any) (map[string]any, error) {
	result, err := r.browserRuntime.StopPageRecording(ctx, input)
	if err != nil {
		return nil, err
	}
	r.markInPageStopped(stringFromAny(input["recording_session_id"]))
	return result, nil
}

func newContractP45Runtime() *contractP45Runtime {
	return &contractP45Runtime{}
}

func (r *contractP45Runtime) CaptureProjectAuthState(_ context.Context, input map[string]any) (map[string]any, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "capture_auth_state")
	r.captureRecordingSessionID = stringFromAny(input["recording_session_id"])
	if _, unavailable := r.unavailableAuthSessions[r.captureRecordingSessionID]; unavailable {
		return nil, errRecordingSessionStorageSnapshotUnavailable
	}
	if r.pendingAuthStorageState != nil {
		if r.pendingAuthStorageSessionID != r.captureRecordingSessionID {
			return nil, errRecordingSessionStorageSnapshotUnavailable
		}
		return r.pendingAuthStorageState, nil
	}
	if r.nextStorageState == nil {
		r.nextStorageState = contractStorageState("https://example.invalid/app/home")
	}
	return r.nextStorageState, nil
}

func (r *contractP45Runtime) failNextProjectAuthStateSave() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failNextSave = true
}

func (r *contractP45Runtime) requireCaptureRecordingSessionID(t *testing.T, want string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.captureRecordingSessionID != want {
		t.Fatalf("capture recording_session_id = %q, want %q", r.captureRecordingSessionID, want)
	}
}

func (r *contractP45Runtime) SaveProjectAuthState(_ context.Context, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "save_auth_state")
	if r.failNextSave {
		r.failNextSave = false
		return fmt.Errorf("contract save failure")
	}
	return nil
}

func (r *contractP45Runtime) StartPageRecording(_ context.Context, input map[string]any) (map[string]any, error) {
	r.mu.Lock()
	r.events = append(r.events, "new_clean_context")
	if input["auth_context"] == "project_saved" {
		r.events = append(r.events, "restore_project_auth_state")
	}
	r.events = append(r.events, "open_target_url", "start_recording")
	r.recordingActive = true
	r.recordingSessionID = stringFromAny(input["recording_session_id"])
	startEntered := r.startEntered
	releaseStart := r.releaseStart
	r.startEntered = nil
	r.mu.Unlock()
	if startEntered != nil {
		close(startEntered)
	}
	if releaseStart != nil {
		<-releaseStart
	}
	return map[string]any{"recording_session_id": "contract-recording-session"}, nil
}

func (r *contractP45Runtime) StopPageRecording(_ context.Context, _ map[string]any) (map[string]any, error) {
	r.mu.Lock()
	r.events = append(r.events, "stop_recording")
	if r.stopErr != nil {
		err := r.stopErr
		r.mu.Unlock()
		return nil, err
	}
	recordingSessionID := r.recordingSessionID
	r.recordingActive = false
	r.recordingSessionID = ""
	if r.stopPublishesPending && recordingSessionID != "" {
		r.pendingStoppedSessionID = recordingSessionID
	}
	result := r.stopResult
	stopEntered := r.stopEntered
	releaseStop := r.releaseStop
	r.stopEntered = nil
	r.mu.Unlock()
	if stopEntered != nil {
		close(stopEntered)
	}
	if releaseStop != nil {
		<-releaseStop
	}
	if result != nil {
		return result, nil
	}
	return map[string]any{
		"actions":      []map[string]any{},
		"dom_snapshot": map[string]any{},
		"artifacts":    []map[string]any{},
	}, nil
}

func (r *contractP45Runtime) PrepareTestExecution(_ context.Context, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "new_clean_context")
	return nil
}

func (r *contractP45Runtime) RestoreProjectAuthState(_ context.Context, _ map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, "restore_project_auth_state")
	return nil
}

func (r *contractP45Runtime) ActivePageRecording() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recordingSessionID, r.recordingActive
}

func (r *contractP45Runtime) PendingStoppedPageRecording() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingStoppedSessionID, r.pendingStoppedSessionID != ""
}

func (r *contractP45Runtime) AcknowledgeStoppedPageRecording(_ context.Context, input map[string]any) {
	recordingSessionID := stringFromAny(input["recording_session_id"])
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingStoppedSessionID != recordingSessionID {
		return
	}
	r.acknowledgedStoppedSessions = append(r.acknowledgedStoppedSessions, recordingSessionID)
	r.pendingStoppedSessionID = ""
}

func (r *contractP45Runtime) AcknowledgeProjectAuthStateCapture(_ context.Context, input map[string]any) {
	recordingSessionID := stringFromAny(input["recording_session_id"])
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingAuthStorageState == nil || r.pendingAuthStorageSessionID != recordingSessionID {
		return
	}
	r.acknowledgedAuthSessions = append(r.acknowledgedAuthSessions, recordingSessionID)
	r.pendingAuthStorageState = nil
	r.pendingAuthStorageSessionID = ""
	if r.unavailableAuthSessions == nil {
		r.unavailableAuthSessions = make(map[string]struct{})
	}
	r.unavailableAuthSessions[recordingSessionID] = struct{}{}
}

func (r *contractP45Runtime) DiscardProjectAuthStateCapture(_ context.Context, input map[string]any) {
	recordingSessionID := stringFromAny(input["recording_session_id"])
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingAuthStorageSessionID == recordingSessionID {
		r.pendingAuthStorageState = nil
		r.pendingAuthStorageSessionID = ""
	}
	if r.unavailableAuthSessions == nil {
		r.unavailableAuthSessions = make(map[string]struct{})
	}
	r.unavailableAuthSessions[recordingSessionID] = struct{}{}
	r.discardedAuthSessions = append(r.discardedAuthSessions, recordingSessionID)
}

func (r *contractP45Runtime) HasProjectAuthStateCapture(_ context.Context, input map[string]any) bool {
	recordingSessionID := stringFromAny(input["recording_session_id"])
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pendingAuthStorageState != nil && r.pendingAuthStorageSessionID == recordingSessionID
}

func (r *contractP45Runtime) markInPageStopped(recordingSessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordingActive = false
	r.recordingSessionID = ""
	r.pendingStoppedSessionID = recordingSessionID
}

func (r *contractP45Runtime) setPendingAuthStorageSnapshot(recordingSessionID string, state map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingAuthStorageSessionID = recordingSessionID
	r.pendingAuthStorageState = state
}

func (r *contractP45Runtime) requirePendingStoppedSession(t *testing.T, want string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingStoppedSessionID != want {
		t.Fatalf("pending stopped session = %q, want %q", r.pendingStoppedSessionID, want)
	}
}

func (r *contractP45Runtime) requireAcknowledgedStoppedSession(t *testing.T, want string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, got := range r.acknowledgedStoppedSessions {
		if got == want {
			return
		}
	}
	t.Fatalf("acknowledged stopped sessions = %v, want %q", r.acknowledgedStoppedSessions, want)
}

func (r *contractP45Runtime) requirePendingAuthStorageSnapshot(t *testing.T, wantSessionID string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingAuthStorageState == nil || r.pendingAuthStorageSessionID != wantSessionID {
		t.Fatalf("pending auth snapshot session = %q, want %q", r.pendingAuthStorageSessionID, wantSessionID)
	}
}

func (r *contractP45Runtime) requireAcknowledgedAuthStorageSnapshot(t *testing.T, want string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, got := range r.acknowledgedAuthSessions {
		if got == want {
			return
		}
	}
	t.Fatalf("acknowledged auth sessions = %v, want %q", r.acknowledgedAuthSessions, want)
}

func (r *contractP45Runtime) requireDiscardedAuthStorageSnapshot(t *testing.T, want string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, got := range r.discardedAuthSessions {
		if got == want {
			return
		}
	}
	t.Fatalf("discarded auth sessions = %v, want %q", r.discardedAuthSessions, want)
}

func (r *contractP45Runtime) requireEvents(t *testing.T, want ...string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != len(want) {
		t.Fatalf("events = %v, want %v", r.events, want)
	}
	for i := range want {
		if r.events[i] != want[i] {
			t.Fatalf("events = %v, want %v", r.events, want)
		}
	}
}

func (r *contractP45Runtime) requireNoEvent(t *testing.T, event string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, got := range r.events {
		if got == event {
			t.Fatalf("events unexpectedly contain %q: %v", event, r.events)
		}
	}
}

func (r *contractP45Runtime) requireEventBefore(t *testing.T, before, after string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	beforeIndex, afterIndex := -1, -1
	for i, event := range r.events {
		if event == before && beforeIndex == -1 {
			beforeIndex = i
		}
		if event == after && afterIndex == -1 {
			afterIndex = i
		}
	}
	if beforeIndex == -1 || afterIndex == -1 || beforeIndex > afterIndex {
		t.Fatalf("events = %v, want %s before %s", r.events, before, after)
	}
}

type contractP45Runner struct {
	t          *testing.T
	calls      int
	failOnCall bool
	inputs     []map[string]any
	events     *contractP45Runtime
}

func newContractP45Runner(t *testing.T) *contractP45Runner {
	t.Helper()
	return &contractP45Runner{t: t}
}

func (r *contractP45Runner) recordEventsTo(events *contractP45Runtime) {
	r.events = events
}

func (r *contractP45Runner) Run(_ context.Context, input map[string]any) (map[string]any, error) {
	if r.events != nil {
		r.events.events = append(r.events.events, "runner_start")
	}
	r.calls++
	if r.failOnCall {
		r.t.Fatalf("runner must not be called before P4.5 auth preflight; input: %v", input)
	}
	r.inputs = append(r.inputs, input)
	authContext := strings.TrimSpace(stringFromAny(input["auth_context"]))
	if authContext == "" {
		r.t.Fatalf("runner input missing auth_context: %v", input)
	}
	authSource := strings.TrimSpace(stringFromAny(input["auth_context_source"]))
	if authSource == "" {
		r.t.Fatalf("runner input missing auth_context_source: %v", input)
	}
	report := map[string]any{
		"schema_version":      1,
		"source":              "blueprint",
		"auth_context":        authContext,
		"auth_context_source": authSource,
		"initial_navigation":  input["initial_navigation"],
		"summary":             map[string]any{"total_steps": 1, "passed_steps": 1, "failed_steps": 0, "failed_step_index": nil},
		"steps":               []map[string]any{{"index": 0, "action": "expect_text", "status": "passed"}},
	}
	if authContext == "project_saved" {
		authState, ok := input["auth_state"].(map[string]any)
		if !ok || len(authState) == 0 {
			r.t.Fatalf("project_saved runner input missing auth_state summary: %v", input)
		}
		report["auth_state"] = authState
	}
	return map[string]any{
		"status":        "passed",
		"duration_ms":   7,
		"error_message": "",
		"report_data":   report,
	}, nil
}

func (e *generateContractEnv) captureProjectAuthState(t *testing.T, projectID, versionID uint, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/auth-state/capture", projectID, versionID)
	return e.performP45JSONRequest(t, http.MethodPost, path, payload)
}

func (e *generateContractEnv) getProjectAuthState(t *testing.T, projectID, versionID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/auth-state", projectID, versionID)
	return e.performP45JSONRequest(t, http.MethodGet, path, nil)
}

func (e *generateContractEnv) deleteProjectAuthState(t *testing.T, projectID, versionID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/auth-state", projectID, versionID)
	return e.performP45JSONRequest(t, http.MethodDelete, path, nil)
}

func (e *generateContractEnv) deleteVersion(t *testing.T, projectID, versionID uint) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d", projectID, versionID)
	return e.performP45JSONRequest(t, http.MethodDelete, path, nil)
}

func (e *generateContractEnv) startPageRecordingSession(t *testing.T, projectID, versionID, pageID uint, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/recording-session", projectID, versionID, pageID)
	return e.performP45JSONRequest(t, http.MethodPost, path, payload)
}

func (e *generateContractEnv) savePageRecording(t *testing.T, projectID, versionID, pageID uint, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/recordings", projectID, versionID, pageID)
	return e.performP45JSONRequest(t, http.MethodPost, path, payload)
}

func (e *generateContractEnv) performP45JSONRequest(t *testing.T, method, path string, payload map[string]any) *httptest.ResponseRecorder {
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

func (e *generateContractEnv) installProjectAuthRuntimeFake(t *testing.T, runtime any) {
	t.Helper()
	if runtime == nil {
		t.Fatal("fake project auth runtime is required")
	}
	method := reflect.ValueOf(e.handler).MethodByName("SetProjectAuthRuntimeForTest")
	if !method.IsValid() {
		t.Fatalf("SetProjectAuthRuntimeForTest is not available on production Handler; P4.5 tests require fake browser/auth-state service injection through SetupRouter initialization")
	}
	if method.Type().NumIn() != 1 {
		t.Fatalf("SetProjectAuthRuntimeForTest input count = %d, want 1", method.Type().NumIn())
	}
	arg := reflect.ValueOf(runtime)
	want := method.Type().In(0)
	if !arg.Type().AssignableTo(want) {
		if arg.Type().ConvertibleTo(want) {
			arg = arg.Convert(want)
		} else {
			t.Fatalf("fake runtime type %s is not assignable to SetProjectAuthRuntimeForTest(%s)", arg.Type(), want)
		}
	}
	method.Call([]reflect.Value{arg})
}

func (e *generateContractEnv) installAnyTestCaseRunner(t *testing.T, runner any) {
	t.Helper()
	method := reflect.ValueOf(e.handler).MethodByName("SetTestCaseRunnerForTest")
	if !method.IsValid() {
		t.Fatalf("SetTestCaseRunnerForTest is not available on production Handler")
	}
	method.Call([]reflect.Value{reflect.ValueOf(runner)})
}

func (e *generateContractEnv) seedVersionInProject(t *testing.T, projectID uint, versionName string) models.ProjectVersion {
	t.Helper()
	version := models.ProjectVersion{
		ProjectID:   projectID,
		VersionName: versionName,
		BaseURL:     "https://example.invalid/app",
	}
	if err := e.db.Create(&version).Error; err != nil {
		t.Fatalf("seed version in project: %v", err)
	}
	return version
}

func (e *generateContractEnv) seedProjectAuthState(t *testing.T, projectID, versionID, pageID uint, state map[string]any) models.ProjectAuthState {
	t.Helper()
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal auth state: %v", err)
	}
	now := time.Now().UTC()
	row := models.ProjectAuthState{
		ProjectID:           projectID,
		VersionID:           versionID,
		Name:                "Default auth state",
		Status:              "active",
		SchemaVersion:       1,
		StateJSON:           string(stateJSON),
		StateDigest:         "contract-digest",
		OriginAllowlistJSON: `["https://example.invalid"]`,
		CookieCount:         1,
		OriginCount:         1,
		CapturedURL:         stringFromAny(state["captured_url"]),
		CapturedPageID:      pageID,
		CapturedAt:          now,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if row.CapturedURL == "" {
		row.CapturedURL = "https://example.invalid/app/home"
	}
	if err := e.db.Create(&row).Error; err != nil {
		t.Fatalf("seed ProjectAuthState through production model: %v", err)
	}
	return row
}

func (e *generateContractEnv) requireProjectAuthStateUnchanged(t *testing.T, want models.ProjectAuthState) {
	t.Helper()
	var got models.ProjectAuthState
	if err := e.db.Where("id = ?", want.ID).First(&got).Error; err != nil {
		t.Fatalf("load ProjectAuthState %d: %v", want.ID, err)
	}
	if got.StateJSON != want.StateJSON || got.StateDigest != want.StateDigest || got.Status != want.Status {
		t.Fatalf("ProjectAuthState changed\nwant: %+v\n got: %+v", want, got)
	}
}

func (e *generateContractEnv) requireProjectAuthStateMissing(t *testing.T, authStateID uint) {
	t.Helper()
	var got models.ProjectAuthState
	err := e.db.Where("id = ?", authStateID).First(&got).Error
	if err == nil {
		t.Fatalf("ProjectAuthState %d still exists after scoped delete", authStateID)
	}
	if err != gorm.ErrRecordNotFound {
		t.Fatalf("load deleted ProjectAuthState %d: %v", authStateID, err)
	}
}

func (e *generateContractEnv) requireActiveProjectAuthStateCount(t *testing.T, projectID, versionID uint, want int64) {
	t.Helper()
	var count int64
	if err := e.db.Model(&models.ProjectAuthState{}).
		Where("project_id = ? AND version_id = ? AND status = ?", projectID, versionID, "active").
		Count(&count).Error; err != nil {
		t.Fatalf("count active ProjectAuthState: %v", err)
	}
	if count != want {
		t.Fatalf("active ProjectAuthState count = %d, want %d", count, want)
	}
}

func (e *generateContractEnv) seedMainFlowWithRecordingMeta(t *testing.T, pageID uint, meta map[string]any) models.PageScript {
	t.Helper()
	script := envSeedMainFlowBase(pageID)
	recordingMetaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal recording_meta: %v", err)
	}
	setPageScriptRecordingMetaJSON(t, &script, string(recordingMetaJSON))
	if err := e.db.Create(&script).Error; err != nil {
		t.Fatalf("seed PageScript with recording meta: %v", err)
	}
	return script
}

func envSeedMainFlowBase(pageID uint) models.PageScript {
	return models.PageScript{
		PageID:            pageID,
		Name:              "recorded main flow",
		ActionTrace:       `{"steps":[{"type":"click","target":{"role":"button","text":"primary action","recorded_selector":"button.primary"}}]}`,
		DOMSnapshot:       `{"elements":[{"role":"button","text":"primary action","recorded_selector":"button.primary"}]}`,
		RecordingMetaJSON: defaultRecordingMetaJSON(),
	}
}

func (e *generateContractEnv) requireSinglePageScript(t *testing.T, pageID uint) models.PageScript {
	t.Helper()
	var scripts []models.PageScript
	if err := e.db.Where("page_id = ?", pageID).Order("id asc").Find(&scripts).Error; err != nil {
		t.Fatalf("load PageScript rows: %v", err)
	}
	if len(scripts) != 1 {
		t.Fatalf("PageScript count for page %d = %d, want 1: %+v", pageID, len(scripts), scripts)
	}
	return scripts[0]
}

func contractStorageState(capturedURL string) map[string]any {
	return map[string]any{
		"schema_version": 1,
		"kind":           "browser_storage_state",
		"captured_url":   capturedURL,
		"captured_at":    time.Now().UTC().Format(time.RFC3339),
		"origins": []map[string]any{{
			"origin": "https://example.invalid",
			"local_storage": []map[string]any{{
				"name":  "access_token",
				"value": contractSecretLocalToken,
			}},
			"session_storage": []map[string]any{{
				"name":  "csrf_token",
				"value": contractSecretSessionToken,
			}},
		}},
		"cookies": []map[string]any{{
			"name":      "session",
			"value":     contractSecretCookieValue,
			"domain":    "example.invalid",
			"path":      "/",
			"http_only": true,
			"secure":    true,
			"same_site": "Lax",
		}},
		"extensions": map[string]any{},
	}
}

func recordingMeta(kind, authContext string) map[string]any {
	return map[string]any{
		"schema_version": 1,
		"recording_kind": kind,
		"auth_context":   authContext,
		"auth_state_id":  nil,
		"target_url":     "https://example.invalid/app/orders",
		"started_at":     time.Now().UTC().Format(time.RFC3339),
		"ended_at":       time.Now().UTC().Add(time.Minute).Format(time.RFC3339),
	}
}

func validPlaybotOutputWithAuthContext(title, authContext string) string {
	data, err := json.Marshal(map[string]any{
		"schema_version": "p4.7.5",
		"status":         "success",
		"test_cases": []map[string]any{{
			"title":        title,
			"description":  "generated with auth context",
			"auth_context": authContext,
			"steps": []map[string]any{{
				"action": "expect_text",
				"target": map[string]any{"text": "ready", "recorded_selector": ".ready"},
				"value":  "ready",
			}},
		}},
		"analysis":        map[string]any{"summary": "contract"},
		"generated_count": 1,
		"error":           nil,
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}

func executableBlueprintWithAuth(title, authContext string) map[string]any {
	blueprint := executableBlueprint(title)
	blueprint["auth_context"] = authContext
	return blueprint
}

func executableNavigateBlueprintWithAuth(title, authContext string) map[string]any {
	return map[string]any{
		"title":        title,
		"description":  "",
		"auth_context": authContext,
		"steps": []map[string]any{{
			"action": "navigate",
			"url":    "/orders",
		}},
	}
}

func p45ObjectField(t *testing.T, obj map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := obj[name].(map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want object; object: %v", name, obj[name], obj)
	}
	return value
}

func p45ParseJSONMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		t.Fatalf("parse JSON object: %v; raw: %s", err, raw)
	}
	return obj
}

func pageScriptRecordingMetaJSON(t *testing.T, script *models.PageScript) string {
	t.Helper()
	field := pageScriptField(t, script, "RecordingMetaJSON")
	if field.Kind() != reflect.String {
		t.Fatalf("models.PageScript.RecordingMetaJSON = %s, want string", field.Type())
	}
	return field.String()
}

func setPageScriptRecordingMetaJSON(t *testing.T, script *models.PageScript, value string) {
	t.Helper()
	field := pageScriptField(t, script, "RecordingMetaJSON")
	if field.Kind() != reflect.String {
		t.Fatalf("models.PageScript.RecordingMetaJSON = %s, want string", field.Type())
	}
	if !field.CanSet() {
		t.Fatalf("models.PageScript.RecordingMetaJSON is not settable")
	}
	field.SetString(value)
}

func pageScriptField(t *testing.T, script *models.PageScript, name string) reflect.Value {
	t.Helper()
	value := reflect.ValueOf(script)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		t.Fatalf("PageScript must be a non-nil pointer")
	}
	field := value.Elem().FieldByName(name)
	if !field.IsValid() {
		t.Fatalf("models.PageScript missing P4.5 field %s", name)
	}
	return field
}

func requireBodyOmitsAuthSecrets(t *testing.T, res *httptest.ResponseRecorder) {
	t.Helper()
	body := res.Body.String()
	for _, secret := range []string{contractSecretCookieValue, contractSecretLocalToken, contractSecretSessionToken} {
		if strings.Contains(body, secret) {
			t.Fatalf("response body leaked auth secret %q: %s", secret, body)
		}
	}
}

func requireJSONValueOmitsAuthSecrets(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value for secret check: %v", err)
	}
	raw := string(data)
	for _, secret := range []string{contractSecretCookieValue, contractSecretLocalToken, contractSecretSessionToken} {
		if strings.Contains(raw, secret) {
			t.Fatalf("JSON value leaked auth secret %q: %s", secret, raw)
		}
	}
}
