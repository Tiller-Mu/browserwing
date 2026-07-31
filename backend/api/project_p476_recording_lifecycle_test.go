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
	"sync/atomic"
	"testing"
	"time"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/services/browser"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// P4.7.6 红测：录制动作必须由 operation_id 绑定，不能再由 HTTP
// handler 或 Manager 的瞬态状态隐式决定业务结果。
func TestP476RecordingActionsRequireOperationID(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, runtime)

	start := func(payload map[string]any) *httptest.ResponseRecorder {
		return env.startPageRecordingSession(t, project.ID, version.ID, page.ID, payload)
	}

	missingPayload := map[string]any{
		"recording_kind":      recordingKindLoginFlow,
		"auth_context":        authContextClean,
		"browser_instance_id": "p476-instance-a",
		"runtime_page_id":     "page-a",
	}
	missingData, err := json.Marshal(missingPayload)
	if err != nil {
		t.Fatalf("marshal missing operation request: %v", err)
	}
	missingReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/recording-session", project.ID, version.ID, page.ID), bytes.NewReader(missingData))
	missingReq.Header.Set("Content-Type", "application/json")
	missing := httptest.NewRecorder()
	env.router.ServeHTTP(missing, missingReq)
	env.requireStatus(t, missing, http.StatusBadRequest)
	requireP476ErrorCode(t, missing, "operation_id_invalid")

	operationID := "7184bb57-7a61-4a0f-b8b6-98cf5780ca11"
	payload := map[string]any{
		"operation_id":        operationID,
		"recording_kind":      recordingKindLoginFlow,
		"auth_context":        authContextClean,
		"browser_instance_id": "p476-instance-a",
		"runtime_page_id":     "page-a",
	}
	first := start(payload)
	env.requireStatus(t, first, http.StatusOK)
	second := start(payload)
	env.requireStatus(t, second, http.StatusOK)

	var operations int64
	if err := env.db.Table("recording_operations").Where("operation_id = ?", operationID).Count(&operations).Error; err != nil {
		t.Fatalf("count RecordingOperation: %v", err)
	}
	if operations != 1 {
		t.Fatalf("operation count = %d, want 1", operations)
	}
	var sessions int64
	if err := env.db.Model(&models.RecordingSession{}).Where("browser_instance_id = ?", "p476-instance-a").Count(&sessions).Error; err != nil {
		t.Fatalf("count RecordingSession: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("session count = %d, want 1", sessions)
	}

	conflicting := start(map[string]any{
		"operation_id":        operationID,
		"recording_kind":      recordingKindLoginFlow,
		"auth_context":        authContextClean,
		"browser_instance_id": "p476-instance-a",
		"runtime_page_id":     "page-b",
	})
	env.requireStatus(t, conflicting, http.StatusConflict)
	requireP476ErrorCode(t, conflicting, "operation_id_payload_conflict")
}

func TestP476BrowserRuntimeStartScopeUsesLifecyclePageID(t *testing.T) {
	scope := recordingLifecycleRuntimeScope(map[string]any{
		"project_id":           uint(101),
		"version_id":           uint(102),
		"page_id":              uint(103),
		"captured_page_id":     uint(999),
		"recording_session_id": "p476-start-scope-session",
		"browser_instance_id":  "p476-start-scope-instance",
		"runtime_page_id":      "p476-start-scope-page",
		"runtime_generation":   "p476-start-scope-runtime",
		"lease_generation":     "p476-start-scope-lease",
	})
	if scope.PageID != 103 {
		t.Fatalf("Start runtime scope PageID = %d, want lifecycle page_id 103", scope.PageID)
	}
	if scope.ProjectID != 101 || scope.VersionID != 102 || scope.RecordingSessionID != "p476-start-scope-session" || scope.BrowserInstanceID != "p476-start-scope-instance" || scope.RuntimePageID != "p476-start-scope-page" || scope.RuntimeGeneration != "p476-start-scope-runtime" || scope.LeaseGeneration != "p476-start-scope-lease" {
		t.Fatalf("Start runtime scope = %+v, want the complete lifecycle identity", scope)
	}
}

// HTTP Stop must not call RecordingLifecycleService as an alternate stopped
// writer. It drives the scoped runtime once, then the same Coordinator
// recording_stopped path commits and ACKs the frozen receipt.
func TestP476HTTPStopConvergesThroughCoordinatorStoppedEvent(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.stopResult = map[string]any{
		"actions":                                []map[string]any{},
		"dom_snapshot":                           map[string]any{},
		"artifacts":                              []map[string]any{},
		"runtime_final_receipt_id":               "p476-http-coordinator-receipt",
		"runtime_final_receipt_claim_generation": uint64(1),
		"runtime_final_sync_revision":            uint64(1),
	}
	runtime.stopPublishesPending = true
	env.installProjectAuthRuntimeFake(t, runtime)

	started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"operation_id":        "6e7a9883-5faf-415e-afac-d21c72f1ca1f",
		"recording_kind":      "business_flow",
		"auth_context":        authContextClean,
		"browser_instance_id": "p476-http-coordinator-instance",
		"runtime_page_id":     "p476-http-coordinator-page",
	})
	env.requireStatus(t, started, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, started)["recording_session_id"]))
	operationID := "769dfda2-40f0-49fd-89b4-770d6292e359"
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/stop", project.ID, version.ID, page.ID, sessionID)

	stopped := env.p47JSONRequest(t, http.MethodPost, path, p476OperationPayload(map[string]any{"operation_id": operationID}), "")
	env.requireStatus(t, stopped, http.StatusOK)
	// A response-loss replay must reuse Coordinator's completed operation and
	// must not send a second stop request to Recorder.
	replayed := env.p47JSONRequest(t, http.MethodPost, path, p476OperationPayload(map[string]any{"operation_id": operationID}), "")
	env.requireStatus(t, replayed, http.StatusOK)
	runtime.requireEvents(t, "new_clean_context", "open_target_url", "start_recording", "stop_recording")

	var session models.RecordingSession
	if err := env.db.First(&session, sessionID).Error; err != nil {
		t.Fatalf("reload stopped session: %v", err)
	}
	if session.Status != "stopped" {
		t.Fatalf("session status = %q, want stopped", session.Status)
	}
	var operation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&operation).Error; err != nil {
		t.Fatalf("load Stop operation: %v", err)
	}
	if operation.Status != "completed" || operation.ReceiptID == "" || operation.RuntimeReceiptClaimGeneration == 0 {
		t.Fatalf("Coordinator Stop operation = %+v, want completed bound receipt", operation)
	}
	runtime.requireAcknowledgedStoppedSession(t, sessionID)
}

func TestP476StartRequiresStableRuntimePageIdentity(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.installProjectAuthRuntimeFake(t, newContractP45Runtime())

	payload := map[string]any{
		"operation_id":        "0cb9d31f-3d34-4977-8178-6f0cbcc637d2",
		"recording_kind":      recordingKindLoginFlow,
		"auth_context":        authContextClean,
		"browser_instance_id": "p476-stable-runtime",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal start without runtime_page_id: %v", err)
	}
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/recording-session", project.ID, version.ID, page.ID)
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	env.router.ServeHTTP(response, request)
	env.requireStatus(t, response, http.StatusBadRequest)
	requireP476ErrorCode(t, response, "recording_source_invalid")
}

func TestP476CompletedStartReplaysBeforeReadingCurrentProjectAuthState(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	auth := env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/home"))
	env.installProjectAuthRuntimeFake(t, newContractP45Runtime())
	payload := map[string]any{
		"operation_id":        "2ee70ba1-f3d8-4e8f-9122-2f4341833e1b",
		"recording_kind":      "business_flow",
		"auth_context":        authContextProjectSaved,
		"browser_instance_id": "p476-auth-replay-instance",
		"runtime_page_id":     "p476-auth-replay-page",
	}
	first := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, payload)
	env.requireStatus(t, first, http.StatusOK)
	firstSessionID := fmt.Sprint(env.decodeObject(t, first)["recording_session_id"])
	if err := env.db.Model(&models.ProjectAuthState{}).Where("id = ?", auth.ID).Update("status", "invalid").Error; err != nil {
		t.Fatalf("invalidate saved auth state: %v", err)
	}

	replayed := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, payload)
	env.requireStatus(t, replayed, http.StatusOK)
	if got := fmt.Sprint(env.decodeObject(t, replayed)["recording_session_id"]); got != firstSessionID {
		t.Fatalf("completed Start replay session = %q, want %q", got, firstSessionID)
	}
}

func TestP476ProjectSavedStartPersistsSourceAuthMetaAndReplaysMissingAuthFailure(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, runtime)
	operationID := "b27e3c94-22d7-4b84-a7d6-bc7e997e2201"
	payload := map[string]any{
		"operation_id": operationID, "browser_instance_id": "missing-auth-instance", "runtime_page_id": "missing-auth-page",
		"recording_kind": "business_flow", "auth_context": "project_saved", "target_url": "https://example.invalid/orders",
	}
	missing := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, payload)
	env.requireStatus(t, missing, http.StatusConflict)
	if got := env.decodeObject(t, missing)["code"]; got != "recording_auth_state_unavailable" {
		t.Fatalf("missing saved auth Start code = %v", got)
	}
	// Publishing an auth state later cannot turn a response-loss retry of this
	// operation into a different Start.
	env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/after-login"))
	retry := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, payload)
	env.requireStatus(t, retry, http.StatusConflict)
	if got := env.decodeObject(t, retry)["code"]; got != "recording_auth_state_unavailable" {
		t.Fatalf("missing saved auth Start replay code = %v", got)
	}

	started := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"operation_id": "f38222f0-04e8-40ac-91c0-f0c0ac2a7df8", "browser_instance_id": "saved-auth-instance", "runtime_page_id": "saved-auth-page",
		"recording_kind": "business_flow", "auth_context": "project_saved", "target_url": "https://example.invalid/orders",
	})
	env.requireStatus(t, started, http.StatusOK)
	meta, _ := env.decodeObject(t, started)["recording_meta"].(map[string]any)
	if meta["auth_state_id"] == nil {
		t.Fatalf("project_saved Start recording_meta omitted fixed auth state: %#v", meta)
	}
}

func TestP476RequestCanonicalizerNormalizesNestedJSON(t *testing.T) {
	left := canonicalRequestHash(map[string]any{
		"recording_meta": json.RawMessage(`{"nested":{"alpha":1,"beta":2},"schema_version":1}`),
		"dom_snapshot":   json.RawMessage(`{"root":{"name":"app","attributes":{"a":"1","b":"2"}}}`),
	})
	right := canonicalRequestHash(map[string]any{
		"recording_meta": json.RawMessage(`{"schema_version":1,"nested":{"beta":2,"alpha":1}}`),
		"dom_snapshot":   json.RawMessage(`{"root":{"attributes":{"b":"2","a":"1"},"name":"app"}}`),
	})
	if left != right {
		t.Fatalf("semantic nested JSON produced different request hashes: %s != %s", left, right)
	}
	valid := canonicalRequestHash(map[string]any{"recording_meta": json.RawMessage(`{"schema_version":1}`)})
	trailing := canonicalRequestHash(map[string]any{"recording_meta": json.RawMessage(`{"schema_version":1}{"unexpected":true}`)})
	if valid == trailing {
		t.Fatal("canonical request hash accepted trailing JSON as the same request")
	}
}

func TestP476PendingStopValidatesRequestBeforeRecovery(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "pending-stop-identity")
	operationID := "e8f8082d-b6f0-44cc-a6be-0fc55753e0f8"
	requestHash := canonicalRequestHash(map[string]any{"dom_snapshot": json.RawMessage(`{"version":1}`)})
	sessionID := session.ID
	if err := env.db.Create(&models.RecordingOperation{
		OperationID: operationID, Action: recordingActionStop, Scope: recordingSessionScope(session), RequestPayloadHash: requestHash,
		RequestCanonicalizerVersion: requestCanonicalizerVersion, Status: "pending", RuntimeEffectKey: runtimeEffectKey("stop:" + fmt.Sprint(session.ID)),
		RecordingSessionID: &sessionID, ProjectID: session.ProjectID, VersionID: session.VersionID, PageID: session.PageID,
	}).Error; err != nil {
		t.Fatalf("create pending Stop operation: %v", err)
	}

	runtime := newContractP45Runtime()
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	_, err := service.Stop(t.Context(), stopRecordingLifecycleInput{
		OperationID: operationID, Session: session, DOMSnapshot: json.RawMessage(`{"version":2}`),
	})
	requireP476LifecycleError(t, err, http.StatusConflict, "operation_id_payload_conflict")
}

func TestP476RuntimeHolderDynamicallyForwardsScopedRecoveryLookup(t *testing.T) {
	first := newContractP45Runtime()
	first.setActiveRuntimeScope(map[string]any{"recording_session_id": "session-a", "project_id": "1", "version_id": "1", "page_id": "1", "browser_instance_id": "instance-a", "runtime_page_id": "page-a", "runtime_generation": "runtime-a", "lease_generation": "lease-a"})
	holder := newProjectAuthRuntimeHolder(first)
	locator, ok := any(holder).(recordingRuntimeScopeLocator)
	if !ok {
		t.Fatal("projectAuthRuntimeHolder must forward scoped runtime recovery lookup")
	}
	scope := map[string]any{"recording_session_id": "session-a", "project_id": "1", "version_id": "1", "page_id": "1", "browser_instance_id": "instance-a", "runtime_page_id": "page-a", "runtime_generation": "runtime-a", "lease_generation": "lease-a"}
	if !locator.HasActivePageRecordingScope(t.Context(), scope) {
		t.Fatal("holder did not forward active scoped runtime lookup")
	}

	second := newContractP45Runtime()
	second.setPendingStoppedRuntimeScope(scope)
	holder.set(second)
	if locator.HasActivePageRecordingScope(t.Context(), scope) {
		t.Fatal("holder retained the replaced runtime's scoped lookup")
	}
	if !locator.HasPendingStoppedPageRecordingScope(t.Context(), scope) {
		t.Fatal("holder did not dynamically forward pending stopped scoped lookup")
	}
}

func TestP476PendingStopThroughRuntimeHolderWaitsForScopedReceipt(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "holder-pending-stop")
	opID := "548e2e1f-657d-4513-861a-ff56fa8013c5"
	p476PendingOperation(t, env, opID, recordingActionStop, session, "stop:"+fmt.Sprint(session.ID))
	runtime := newContractP45Runtime()
	runtime.setPendingStoppedRuntimeScope(recordingSessionRuntimeScope(session))
	service := NewRecordingLifecycleService(env.db, newProjectAuthRuntimeHolder(runtime), env.handler.config)
	_, handled, err := service.RecoverPendingOperation(t.Context(), opID)
	if err != nil || handled {
		t.Fatalf("pending Stop recovery through holder = handled:%v err:%v, want deferred to scoped receipt", handled, err)
	}
	var current models.RecordingSession
	if err := env.db.First(&current, session.ID).Error; err != nil || current.Status != "recording" {
		t.Fatalf("scoped pending Stop changed session before receipt reconciliation: %+v err=%v", current, err)
	}
	var operation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", opID).First(&operation).Error; err != nil || operation.Status != "pending" {
		t.Fatalf("scoped pending Stop changed operation before receipt reconciliation: %+v err=%v", operation, err)
	}
}

func TestP476StartupRecoveryClosesPendingStartWithoutDeletingItsOperation(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "starting", "startup-start")
	operationID := "18e8bf1f-6fd9-4d05-a035-36bcfd64d27d"
	p476PendingOperation(t, env, operationID, recordingActionStart, session, "start:"+session.BrowserInstanceID)

	service := NewRecordingLifecycleService(env.db, nil, env.handler.config)
	if err := service.RecoverPendingOperations(t.Context()); err != nil {
		t.Fatalf("startup recovery: %v", err)
	}
	var recoveredSession models.RecordingSession
	if err := env.db.First(&recoveredSession, session.ID).Error; err != nil || recoveredSession.Status != "failed" || recoveredSession.FailureCode != "runtime_lease_lost" {
		t.Fatalf("pending Start session after recovery = %+v, err=%v", recoveredSession, err)
	}
	var recoveredOperation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&recoveredOperation).Error; err != nil || recoveredOperation.Status != "failed" || recoveredOperation.ErrorCode != "runtime_lease_lost" {
		t.Fatalf("pending Start operation after recovery = %+v, err=%v", recoveredOperation, err)
	}
}

func TestP476StartupRecoveryDefersLiveStartFenceThenCoordinatorRetriesAtExpiry(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "starting", "startup-live-start")
	operationID := "79165a9c-b090-4f26-b042-6d0bf1dcf5d8"
	p476PendingOperation(t, env, operationID, recordingActionStart, session, "start:"+session.BrowserInstanceID)
	now := time.Now().UTC()
	if err := env.db.Model(&models.RecordingOperation{}).Where("operation_id = ?", operationID).Updates(map[string]any{
		"runtime_driver_token":            "live-start-fence",
		"runtime_driver_claim_generation": 1,
		"runtime_driver_claimed_at":       now,
		"runtime_driver_lease_expires_at": now.Add(startDriverClaimTTL),
	}).Error; err != nil {
		t.Fatalf("set live Start fence: %v", err)
	}

	runtime := newContractP45Runtime()
	runtime.setActiveRuntimeScope(recordingSessionRuntimeScope(session))
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	if err := service.RecoverPendingOperations(t.Context()); err != nil {
		t.Fatalf("startup recovery with live Start fence: %v", err)
	}
	var currentSession models.RecordingSession
	if err := env.db.First(&currentSession, session.ID).Error; err != nil || currentSession.Status != "starting" {
		t.Fatalf("startup recovery changed live Start session = %+v err=%v, want starting", currentSession, err)
	}
	var currentOperation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&currentOperation).Error; err != nil || currentOperation.Status != "pending" {
		t.Fatalf("startup recovery changed live Start operation = %+v err=%v, want pending", currentOperation, err)
	}
	if err := env.db.Model(&models.RecordingOperation{}).Where("id = ?", currentOperation.ID).Update("runtime_driver_lease_expires_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatalf("expire deferred Start fence: %v", err)
	}
	coordinator := NewRecordingRecoveryCoordinator(service, nil)
	coordinator.reconcile(t.Context())
	var failedSession models.RecordingSession
	if err := env.db.First(&failedSession, session.ID).Error; err != nil || failedSession.Status != "failed" || failedSession.FailureCode != "runtime_lease_lost" {
		t.Fatalf("expired deferred Start session = %+v err=%v, want failed/runtime_lease_lost", failedSession, err)
	}
	var failedOperation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&failedOperation).Error; err != nil || failedOperation.Status != "failed" || failedOperation.ErrorCode != "runtime_lease_lost" {
		t.Fatalf("expired deferred Start operation = %+v err=%v, want failed/runtime_lease_lost", failedOperation, err)
	}
	runtime.requireEvents(t, "stop_recording")
	runtime.requireReleasedRecordingScope(t, recordingSessionRuntimeScope(session))
}

func TestP476ExpiredStartRetryFailsDurablyReleasesRuntimeAndAllowsNewStart(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	input := startRecordingLifecycleInput{
		OperationID:       "283acd00-2d44-4bc6-bd11-c2b348bd7965",
		ProjectID:         project.ID,
		VersionID:         version.ID,
		PageID:            page.ID,
		RecordingKind:     recordingKindLoginFlow,
		AuthContext:       authContextClean,
		TargetURL:         "https://example.invalid/app/expired-start",
		BrowserInstanceID: "p476-instance-expired-start",
		RuntimePageID:     "p476-page-expired-start",
	}
	stale := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "starting", "expired-start")
	stale.BrowserInstanceID = input.BrowserInstanceID
	stale.RuntimePageID = input.RuntimePageID
	if err := env.db.Save(&stale).Error; err != nil {
		t.Fatalf("align stale Start runtime scope: %v", err)
	}
	p476PendingOperation(t, env, input.OperationID, recordingActionStart, stale, "start:"+input.BrowserInstanceID)
	expiredAt := time.Now().UTC().Add(-time.Second)
	requestHash := canonicalRequestHash(map[string]any{
		"recording_kind":      input.RecordingKind,
		"auth_context":        input.AuthContext,
		"target_url":          input.TargetURL,
		"browser_instance_id": input.BrowserInstanceID,
		"runtime_page_id":     input.RuntimePageID,
	})
	if err := env.db.Model(&models.RecordingOperation{}).Where("operation_id = ?", input.OperationID).Updates(map[string]any{
		"scope":                           recordingStartScope(input),
		"request_payload_hash":            requestHash,
		"runtime_driver_token":            "expired-start-token",
		"runtime_driver_claim_generation": 1,
		"runtime_driver_claimed_at":       expiredAt,
		"runtime_driver_lease_expires_at": expiredAt,
	}).Error; err != nil {
		t.Fatalf("expire pending Start claim: %v", err)
	}
	runtime.setActiveRuntimeScope(recordingSessionRuntimeScope(stale))

	_, err := service.Start(t.Context(), input)
	requireP476LifecycleError(t, err, http.StatusConflict, "runtime_lease_lost")

	var failedSession models.RecordingSession
	if err := env.db.First(&failedSession, stale.ID).Error; err != nil || failedSession.Status != "failed" || failedSession.FailureCode != "runtime_lease_lost" {
		t.Fatalf("expired Start session = %+v err=%v, want durable runtime_lease_lost", failedSession, err)
	}
	var failedOperation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", input.OperationID).First(&failedOperation).Error; err != nil || failedOperation.Status != "failed" || failedOperation.ErrorCode != "runtime_lease_lost" {
		t.Fatalf("expired Start operation = %+v err=%v, want durable runtime_lease_lost", failedOperation, err)
	}
	runtime.requireEvents(t, "stop_recording")
	runtime.requireReleasedRecordingScope(t, recordingSessionRuntimeScope(stale))

	retry := input
	retry.OperationID = "d0cd3826-9638-4b10-aea4-4f9cd1c7d898"
	result, err := service.Start(t.Context(), retry)
	if err != nil || result.Status != http.StatusOK {
		t.Fatalf("new Start after expired claim cleanup = %+v err=%v", result, err)
	}
}

func TestP476StartRecoveryDoesNotFailFenceRenewedBeforeLockedRecovery(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		renew func(*RecordingLifecycleService, *generateContractEnv, models.RecordingOperation) error
	}{
		{
			name: "same fence heartbeat renewal",
			renew: func(service *RecordingLifecycleService, _ *generateContractEnv, operation models.RecordingOperation) error {
				return service.renewStartRuntimeDriver(operation.ID, startDriverToken(operation), operation.RuntimeDriverClaimGeneration)
			},
		},
		{
			name: "replacement fence",
			renew: func(_ *RecordingLifecycleService, env *generateContractEnv, operation models.RecordingOperation) error {
				now := time.Now().UTC()
				expires := now.Add(startDriverClaimTTL)
				return env.db.Model(&models.RecordingOperation{}).Where("id = ?", operation.ID).Updates(map[string]any{
					"runtime_driver_token":            "replacement-start-fence",
					"runtime_driver_claim_generation": operation.RuntimeDriverClaimGeneration + 1,
					"runtime_driver_claimed_at":       now,
					"runtime_driver_lease_expires_at": expires,
					"updated_at":                      now,
				}).Error
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			runtime := newContractP45Runtime()
			service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
			input := startRecordingLifecycleInput{
				OperationID:       "427a6d42-6f88-4ad2-b2af-593b6dd3f91e",
				ProjectID:         project.ID,
				VersionID:         version.ID,
				PageID:            page.ID,
				RecordingKind:     recordingKindLoginFlow,
				AuthContext:       authContextClean,
				TargetURL:         "https://example.invalid/app/renewed-start",
				BrowserInstanceID: "p476-instance-renewed-start",
				RuntimePageID:     "p476-page-renewed-start",
			}
			stale := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "starting", "renewed-start")
			stale.BrowserInstanceID = input.BrowserInstanceID
			stale.RuntimePageID = input.RuntimePageID
			if err := env.db.Save(&stale).Error; err != nil {
				t.Fatalf("align stale Start runtime scope: %v", err)
			}
			p476PendingOperation(t, env, input.OperationID, recordingActionStart, stale, "start:"+input.BrowserInstanceID)
			expiredAt := time.Now().UTC().Add(-time.Second)
			requestHash := canonicalRequestHash(map[string]any{
				"recording_kind":      input.RecordingKind,
				"auth_context":        input.AuthContext,
				"target_url":          input.TargetURL,
				"browser_instance_id": input.BrowserInstanceID,
				"runtime_page_id":     input.RuntimePageID,
			})
			if err := env.db.Model(&models.RecordingOperation{}).Where("operation_id = ?", input.OperationID).Updates(map[string]any{
				"scope":                           recordingStartScope(input),
				"request_payload_hash":            requestHash,
				"runtime_driver_token":            "expired-start-fence",
				"runtime_driver_claim_generation": 1,
				"runtime_driver_claimed_at":       expiredAt,
				"runtime_driver_lease_expires_at": expiredAt,
			}).Error; err != nil {
				t.Fatalf("expire pending Start claim: %v", err)
			}
			var initial models.RecordingOperation
			if err := env.db.Where("operation_id = ?", input.OperationID).First(&initial).Error; err != nil {
				t.Fatalf("load expired Start operation: %v", err)
			}
			p476RenewStartFenceBetweenBeginAndRecovery(t, env, func() error {
				return scenario.renew(service, env, initial)
			})

			_, err := service.Start(t.Context(), input)
			requireP476LifecycleError(t, err, http.StatusConflict, "recording_operation_in_progress")
			if retry, ok := err.(*recordingLifecycleError); !ok || retry.RetryAfter != 1 {
				t.Fatalf("Start fence renewal result = %#v, want Retry-After 1", err)
			}
			var currentSession models.RecordingSession
			if err := env.db.First(&currentSession, stale.ID).Error; err != nil || currentSession.Status != "starting" {
				t.Fatalf("renewed Start changed session = %+v err=%v, want starting", currentSession, err)
			}
			var currentOperation models.RecordingOperation
			if err := env.db.Where("operation_id = ?", input.OperationID).First(&currentOperation).Error; err != nil || currentOperation.Status != "pending" {
				t.Fatalf("renewed Start changed operation = %+v err=%v, want pending", currentOperation, err)
			}
			runtime.requireEvents(t)
			runtime.mu.Lock()
			releaseCount := len(runtime.releasedRecordingScopes)
			runtime.mu.Unlock()
			if releaseCount != 0 {
				t.Fatalf("renewed Start released runtime scope %d times, want 0", releaseCount)
			}
		})
	}
}

// p476RenewStartFenceBetweenBeginAndRecovery makes the request's first
// beginStart read observe the expired row, then completes renewal before the
// recovery transaction runs its locking read. The callbacks are test-only and
// removed during cleanup, so production code has no scheduling hook.
func p476RenewStartFenceBetweenBeginAndRecovery(t *testing.T, env *generateContractEnv, renew func() error) {
	t.Helper()
	callbackPrefix := fmt.Sprintf("p476_start_fence_renewal_%d", time.Now().UnixNano())
	renewed := make(chan error, 1)
	var operationReads int32
	var waitedForRenewal int32
	if err := env.db.Callback().Query().After("gorm:query").Register(callbackPrefix+"_schedule", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "RecordingOperation" || atomic.AddInt32(&operationReads, 1) != 1 {
			return
		}
		go func() { renewed <- renew() }()
	}); err != nil {
		t.Fatalf("register Start fence renewal schedule callback: %v", err)
	}
	if err := env.db.Callback().Query().Before("gorm:query").Register(callbackPrefix+"_wait", func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "RecordingOperation" || atomic.LoadInt32(&operationReads) != 1 || !atomic.CompareAndSwapInt32(&waitedForRenewal, 0, 1) {
			return
		}
		select {
		case err := <-renewed:
			if err != nil {
				tx.AddError(fmt.Errorf("renew Start fence before recovery lock: %w", err))
			}
		case <-time.After(5 * time.Second):
			tx.AddError(fmt.Errorf("timed out renewing Start fence before recovery lock"))
		}
	}); err != nil {
		t.Fatalf("register Start fence renewal wait callback: %v", err)
	}
	t.Cleanup(func() {
		_ = env.db.Callback().Query().Remove(callbackPrefix + "_wait")
		_ = env.db.Callback().Query().Remove(callbackPrefix + "_schedule")
	})
}

func TestP476StartDriverClaimFencesLateCompletion(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "starting", "fenced-start")
	operationID := "ebc48e05-c421-4c05-b9d1-c9ee4b60c87e"
	p476PendingOperation(t, env, operationID, recordingActionStart, session, "start:"+session.BrowserInstanceID)
	var operation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&operation).Error; err != nil {
		t.Fatalf("load pending Start: %v", err)
	}
	service := NewRecordingLifecycleService(env.db, newContractP45Runtime(), env.handler.config)
	first, owned, err := service.claimStartRuntimeDriver(t.Context(), operation, session)
	if err != nil || !owned || first.RuntimeDriverClaimGeneration != 1 {
		t.Fatalf("first driver claim = %+v owned=%v err=%v", first, owned, err)
	}
	if sameFence, owned, err := service.claimStartRuntimeDriver(t.Context(), first, session); err != nil || !owned || sameFence.RuntimeDriverClaimGeneration != first.RuntimeDriverClaimGeneration {
		t.Fatalf("same pending claim = %+v owned=%v err=%v, want current fence for Manager arbitration", sameFence, owned, err)
	}
	if err := env.db.Model(&models.RecordingOperation{}).Where("id = ?", first.ID).Update("runtime_driver_lease_expires_at", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatalf("expire first claim: %v", err)
	}
	second, owned, err := service.claimStartRuntimeDriver(t.Context(), first, session)
	if err != nil || !owned || second.RuntimeDriverClaimGeneration != 2 {
		t.Fatalf("takeover claim = %+v owned=%v err=%v", second, owned, err)
	}
	_, err = service.completeStart(t.Context(), first, session, nil, startDriverToken(first), first.RuntimeDriverClaimGeneration)
	requireP476LifecycleError(t, err, http.StatusConflict, "recording_operation_in_progress")
	var current models.RecordingSession
	if err := env.db.First(&current, session.ID).Error; err != nil || current.Status != "starting" {
		t.Fatalf("late fenced driver changed session = %+v err=%v", current, err)
	}
}

func TestP476StartTerminalIsNotReturnedWhenFailedReceiptCannotCommit(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "starting", "start-failed-receipt")
	operationID := "8ac9e80f-fc18-4a39-bd19-3be4ff2a18fd"
	p476PendingOperation(t, env, operationID, recordingActionStart, session, "start:"+session.BrowserInstanceID)
	service := NewRecordingLifecycleService(env.db, newContractP45Runtime(), env.handler.config)

	var operation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&operation).Error; err != nil {
		t.Fatalf("load pending Start: %v", err)
	}
	claimed, owned, err := service.claimStartRuntimeDriver(t.Context(), operation, session)
	if err != nil || !owned {
		t.Fatalf("claim Start driver: %+v owned=%v err=%v", claimed, owned, err)
	}
	if err := env.db.Model(&models.RecordingSession{}).Where("id = ?", session.ID).Update("status", "cancelled").Error; err != nil {
		t.Fatalf("cancel starting session: %v", err)
	}
	p476FailNextRecordingOperationUpdate(t, env)
	_, err = service.completeStart(t.Context(), claimed, session, nil, startDriverToken(claimed), claimed.RuntimeDriverClaimGeneration)
	requireP476LifecycleError(t, err, http.StatusInternalServerError, "recording_lifecycle_store_failed")

	var persisted models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&persisted).Error; err != nil || persisted.Status != "pending" {
		t.Fatalf("failed Start receipt became %+v, err=%v; want pending", persisted, err)
	}
}

func TestP476SynchronousPendingOperationsDoNotReserveGlobalEmptyRuntimeEffect(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	base := map[string]any{
		"action":                        recordingActionSync,
		"scope":                         "recording_session:1",
		"request_payload_hash":          "sha256:one",
		"request_canonicalizer_version": requestCanonicalizerVersion,
		"status":                        "pending",
		"project_id":                    project.ID,
		"version_id":                    version.ID,
		"page_id":                       page.ID,
		"runtime_effect_key":            nil,
	}
	first := make(map[string]any, len(base)+1)
	for key, value := range base {
		first[key] = value
	}
	first["operation_id"] = "2fde5658-f229-4a44-8af6-9f21ca3413fd"
	if err := env.db.Table("recording_operations").Create(first).Error; err != nil {
		t.Fatalf("create first no-effect pending operation: %v", err)
	}
	second := make(map[string]any, len(base)+1)
	for key, value := range base {
		second[key] = value
	}
	second["operation_id"] = "4ecc1b78-5458-4802-890a-117839ca3404"
	second["scope"] = "recording_session:2"
	second["request_payload_hash"] = "sha256:two"
	if err := env.db.Table("recording_operations").Create(second).Error; err != nil {
		t.Fatalf("second no-effect pending operation was globally blocked: %v", err)
	}
}

func TestP476SessionSchemaCarriesCASAndPageFlowBaseline(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)

	requireP476ModelField(t, models.TestPage{}, "PageFlowRevision")
	requireP476ModelField(t, models.RecordingSession{}, "LifecycleRevision")
	requireP476ModelField(t, models.RecordingSession{}, "SyncRevision")
	requireP476ModelField(t, models.RecordingSession{}, "BasePageFlowRevision")
	requireP476ModelField(t, models.RecordingSession{}, "BrowserInstanceID")
	requireP476ModelField(t, models.RecordingSession{}, "RuntimePageID")
	requireP476ModelField(t, models.ProjectAuthState{}, "StateCiphertext")
	requireP476ModelField(t, models.ProjectAuthState{}, "SourceSnapshotReceiptID")
	session := models.RecordingSession{
		ProjectID:     project.ID,
		VersionID:     version.ID,
		PageID:        page.ID,
		RecordingKind: recordingKindLoginFlow,
		AuthContext:   authContextClean,
		Status:        "recording",
	}
	if err := env.db.Create(&session).Error; err != nil {
		t.Fatalf("create P4.7.6 RecordingSession: %v", err)
	}

	if err := env.db.Create(&models.RecordingSession{
		ProjectID: project.ID, VersionID: version.ID, PageID: page.ID,
		RecordingKind: recordingKindLoginFlow, AuthContext: authContextClean,
		Status: "recording",
	}).Error; err == nil {
		t.Fatal("active instance partial unique index allowed a second starting|recording session")
	}
	firstAuth := models.ProjectAuthState{ProjectID: project.ID, VersionID: version.ID, Status: "active", SourceSnapshotReceiptID: "p476-snapshot-receipt"}
	if err := env.db.Create(&firstAuth).Error; err != nil {
		t.Fatalf("create source snapshot receipt: %v", err)
	}
	if err := env.db.Create(&models.ProjectAuthState{ProjectID: project.ID, VersionID: version.ID, Status: "active", SourceSnapshotReceiptID: "p476-other-snapshot-receipt"}).Error; err == nil {
		t.Fatal("active ProjectAuthState uniqueness allowed two published states for one project version")
	}
	if err := env.db.Create(&models.ProjectAuthState{ProjectID: project.ID, VersionID: version.ID, Status: "inactive", SourceSnapshotReceiptID: "p476-snapshot-receipt"}).Error; err == nil {
		t.Fatal("source snapshot receipt uniqueness allowed duplicate ProjectAuthState publication")
	}
}

func TestP476LegacyBrowserRecordingRoutesAreRemoved(t *testing.T) {
	env := newGenerateContractEnv(t)
	for _, endpoint := range []string{"/api/v1/browser/record/start", "/api/v1/browser/record/stop", "/api/v1/browser/record/status", "/api/v1/browser/record/clear-state"} {
		req := httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader([]byte(`{}`)))
		res := httptest.NewRecorder()
		env.router.ServeHTTP(res, req)
		if res.Code != http.StatusNotFound && res.Code != http.StatusGone {
			t.Fatalf("legacy endpoint %s returned %d, want 404 or 410: %s", endpoint, res.Code, res.Body.String())
		}
	}
}

func TestP476ProjectAuthStatePersistsCiphertextOnly(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.nextStorageState = contractStorageState("https://example.invalid/app/home")
	env.installProjectAuthRuntimeFake(t, runtime)

	// Capture must be tied to a stopped session; direct active-page capture is no
	// longer a valid path once the lifecycle service owns the operation.
	session := models.RecordingSession{
		ProjectID: project.ID, VersionID: version.ID, PageID: page.ID,
		RecordingKind: recordingKindLoginFlow, AuthContext: authContextClean,
		Status:      "stopped",
		ActionsJSON: `[{"type":"click","selector":"#login"}]`, ActionCount: 1,
		DOMSnapshot:       `{}`,
		RecordingMetaJSON: `{"schema_version":1,"recording_kind":"login_flow","auth_context":"clean","target_url":"https://example.invalid/app/home"}`,
	}
	if err := env.db.Create(&session).Error; err != nil {
		t.Fatalf("create stopped session: %v", err)
	}

	res := env.captureProjectAuthState(t, project.ID, version.ID, map[string]any{
		"operation_id":         "c3c1fdb0-58e9-4071-9f4e-60f62bd4d4de",
		"recording_session_id": fmt.Sprint(session.ID),
		"captured_page_id":     page.ID,
	})
	env.requireStatus(t, res, http.StatusOK)

	var row models.ProjectAuthState
	if err := env.db.Where("project_id = ? AND version_id = ?", project.ID, version.ID).First(&row).Error; err != nil {
		t.Fatalf("load persisted auth state: %v", err)
	}
	if strings.TrimSpace(row.StateJSON) != "" {
		t.Fatalf("ProjectAuthState.StateJSON contains plaintext after capture: %q", row.StateJSON)
	}
	state := reflect.ValueOf(row)
	ciphertext := state.FieldByName("StateCiphertext")
	nonce := state.FieldByName("StateNonce")
	keyID := state.FieldByName("EncryptionKeyID")
	if !ciphertext.IsValid() || !nonce.IsValid() || !keyID.IsValid() || ciphertext.String() == "" || nonce.String() == "" || keyID.String() == "" {
		t.Fatalf("ProjectAuthState encryption metadata missing: %+v", row)
	}
}

func TestP476CompletedCaptureReplaysAfterLaterCancel(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.nextStorageState = contractStorageState("https://example.invalid/app/home")
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "stopped", "capture-replay")
	captureID := "d4e9b6f8-7922-4f45-9de8-d0446c0df5d5"
	captureInput := captureRecordingLifecycleInput{
		OperationID: captureID, ProjectID: project.ID, VersionID: version.ID, Session: session,
		CapturedURL: "https://example.invalid/app/home",
	}
	first, err := service.Capture(t.Context(), captureInput)
	if err != nil || first.Status != http.StatusOK {
		t.Fatalf("first Capture = %#v, %v", first, err)
	}
	if _, err := service.Cancel(t.Context(), cancelRecordingLifecycleInput{
		OperationID: "f6fae5cd-bad5-4ef2-a00d-bbea30e2315c", Session: session,
	}); err != nil {
		t.Fatalf("Cancel after Capture: %v", err)
	}
	replayed, err := service.Capture(t.Context(), captureInput)
	firstBody, marshalFirstErr := json.Marshal(first.Body)
	replayedBody, marshalReplayErr := json.Marshal(replayed.Body)
	if err != nil || marshalFirstErr != nil || marshalReplayErr != nil || replayed.Status != http.StatusOK || !bytes.Equal(replayedBody, firstBody) {
		t.Fatalf("completed Capture replay after Cancel = %#v, %v; want %#v", replayed, err, first)
	}
}

func TestP476SaveUsesPageFlowBaselineAndReplaysTerminalConflicts(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	service := NewRecordingLifecycleService(env.db, newContractP45Runtime(), env.handler.config)
	first := p476StoppedSession(t, env, project.ID, version.ID, page.ID, 0, "first")
	second := p476StoppedSession(t, env, project.ID, version.ID, page.ID, 0, "second")

	firstResult, err := service.Save(t.Context(), saveRecordingLifecycleInput{OperationID: "318b9340-ec85-4ce7-9cb1-25eb4f1ce5ee", Session: first, Name: "first"})
	if err != nil || firstResult.Status != http.StatusOK {
		t.Fatalf("first Save = %#v, %v", firstResult, err)
	}

	conflictID := "6d126089-0eb7-4305-9cbe-5cdc499b3df5"
	_, err = service.Save(t.Context(), saveRecordingLifecycleInput{OperationID: conflictID, Session: second, Name: "second"})
	requireP476LifecycleError(t, err, http.StatusConflict, "page_script_replaced_conflict")
	_, replayErr := service.Save(t.Context(), saveRecordingLifecycleInput{OperationID: conflictID, Session: second, Name: "second"})
	requireP476LifecycleError(t, replayErr, http.StatusConflict, "page_script_replaced_conflict")

	var failed models.RecordingOperation
	if err := env.db.Where("operation_id = ?", conflictID).First(&failed).Error; err != nil {
		t.Fatalf("load failed Save operation: %v", err)
	}
	if failed.Status != "failed" || failed.ErrorCode != "page_script_replaced_conflict" {
		t.Fatalf("failed Save operation = %+v", failed)
	}

	var currentPage models.TestPage
	if err := env.db.First(&currentPage, page.ID).Error; err != nil {
		t.Fatalf("reload page: %v", err)
	}
	third := p476StoppedSession(t, env, project.ID, version.ID, page.ID, currentPage.PageFlowRevision, "third")
	if _, err := service.Save(t.Context(), saveRecordingLifecycleInput{OperationID: "6d8bb462-1b4d-4f95-a4d5-2ba142fe8032", Session: third, Name: "third"}); err != nil {
		t.Fatalf("third Save: %v", err)
	}
	_, err = service.Save(t.Context(), saveRecordingLifecycleInput{OperationID: "bb4a1fcc-7271-4652-8732-5daf2e16334d", Session: first, Name: "first"})
	requireP476LifecycleError(t, err, http.StatusConflict, "page_script_superseded")
}

func TestP476PendingRuntimeEffectsRecoverOrFailDeterministically(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	service := NewRecordingLifecycleService(env.db, nil, env.handler.config)

	recoverable := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "recoverable")
	stopID := "4bb9d81c-33bd-4e5d-a1d9-8c98372a8397"
	p476PendingOperation(t, env, stopID, recordingActionStop, recoverable, "stop:"+fmt.Sprint(recoverable.ID))
	result, handled, err := service.RecoverPendingOperation(t.Context(), stopID)
	if err != nil || !handled || result.Status != http.StatusOK {
		t.Fatalf("recover pending Stop = %#v, handled=%v, err=%v", result, handled, err)
	}
	var recovered models.RecordingSession
	if err := env.db.First(&recovered, recoverable.ID).Error; err != nil || recovered.Status != "stopped" {
		t.Fatalf("recovered session = %+v, err=%v", recovered, err)
	}

	captureSession := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "stopped", "capture-lost")
	captureID := "c9e8f229-5cee-4e1d-8da6-6d426e3bda75"
	p476PendingOperation(t, env, captureID, recordingActionCapture, captureSession, "capture:"+fmt.Sprint(captureSession.ID))
	_, handled, err = service.RecoverPendingOperation(t.Context(), captureID)
	if !handled {
		t.Fatal("pending Capture was not handled after runtime loss")
	}
	requireP476LifecycleError(t, err, http.StatusConflict, "auth_snapshot_unavailable")
	var failed models.RecordingOperation
	if err := env.db.Where("operation_id = ?", captureID).First(&failed).Error; err != nil || failed.Status != "failed" || failed.ErrorCode != "auth_snapshot_unavailable" {
		t.Fatalf("pending Capture result = %+v, err=%v", failed, err)
	}
	var captureAfter models.RecordingSession
	if err := env.db.First(&captureAfter, captureSession.ID).Error; err != nil || captureAfter.Status != "stopped" {
		t.Fatalf("Capture recovery changed session = %+v, err=%v", captureAfter, err)
	}
	if err := service.RecoverPendingOperations(t.Context()); err != nil {
		t.Fatalf("persisted Capture failure must converge startup recovery, got: %v", err)
	}

	cancelledStop := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "cancelled", "cancelled-stop")
	cancelledStopID := "84035192-7f44-4e68-a9ca-1ea5ff10f3a2"
	p476PendingOperation(t, env, cancelledStopID, recordingActionStop, cancelledStop, "stop:"+fmt.Sprint(cancelledStop.ID))
	_, handled, err = service.RecoverPendingOperation(t.Context(), cancelledStopID)
	if !handled {
		t.Fatal("pending Stop after Cancel was not closed")
	}
	requireP476LifecycleError(t, err, http.StatusConflict, "recording_action_not_allowed")
	failed = models.RecordingOperation{}
	if err := env.db.Where("operation_id = ?", cancelledStopID).First(&failed).Error; err != nil || failed.Status != "failed" || failed.ErrorCode != "recording_action_not_allowed" {
		t.Fatalf("pending Stop after Cancel = %+v, err=%v", failed, err)
	}
}

func TestP476InvalidFinalReceiptClosesSessionOperationAndRuntimeReceipts(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.stopPublishesPending = true
	runtime.stopResult = map[string]any{
		"actions":                     []any{"not-an-action"},
		"dom_snapshot":                map[string]any{"schema_version": 1, "kind": "semantic_dom_snapshot", "url": "https://example.invalid/login"},
		"runtime_final_receipt_id":    "invalid-final-receipt",
		"runtime_final_sync_revision": uint64(2),
	}
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "invalid-final")
	runtime.recordingActive = true
	runtime.recordingSessionID = fmt.Sprint(session.ID)

	operationID := "f5afc8bf-9eb8-44c2-b71a-032708d4cc12"
	_, err := service.Stop(t.Context(), stopRecordingLifecycleInput{OperationID: operationID, Session: session})
	requireP476LifecycleError(t, err, http.StatusUnprocessableEntity, "recording_actions_invalid")

	var storedSession models.RecordingSession
	if err := env.db.First(&storedSession, session.ID).Error; err != nil || storedSession.Status != "failed" || storedSession.FailureCode != "recording_actions_invalid" {
		t.Fatalf("invalid final session = %+v, err=%v", storedSession, err)
	}
	var storedOperation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&storedOperation).Error; err != nil || storedOperation.Status != "failed" || storedOperation.ErrorCode != "recording_actions_invalid" {
		t.Fatalf("invalid final operation = %+v, err=%v", storedOperation, err)
	}
	if len(runtime.discardedStoppedSessions) == 0 || len(runtime.discardedAuthSessions) == 0 {
		t.Fatalf("invalid final did not release runtime receipts: stopped=%v auth=%v", runtime.discardedStoppedSessions, runtime.discardedAuthSessions)
	}
}

func TestP476CoordinatorInvalidFinalReceiptUsesStopFailureClosure(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "coordinator-invalid-final")
	coordinator := NewRecordingRecoveryCoordinator(service, nil)
	event := browser.RecordingRuntimeEvent{
		ID: "coordinator-invalid-final-event", Kind: "recording_stopped", ReceiptID: "coordinator-invalid-final-receipt", SyncRevision: 2,
		Actions: []models.ScriptAction{{}}, DOMSnapshot: json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/login"}`),
		Scope: browser.RecordingStorageScope{ProjectID: project.ID, VersionID: version.ID, PageID: page.ID, RecordingSessionID: fmt.Sprint(session.ID), BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration},
	}
	outcome := coordinator.processRuntimeEvent(t.Context(), event)
	if outcome.disposition != runtimeEventDispositionRelease {
		t.Fatalf("invalid final coordinator outcome = %+v, want durable release", outcome)
	}
	requireP476LifecycleError(t, outcome.err, http.StatusUnprocessableEntity, "recording_actions_invalid")
	var storedSession models.RecordingSession
	if err := env.db.First(&storedSession, session.ID).Error; err != nil || storedSession.Status != "failed" {
		t.Fatalf("coordinator invalid final session = %+v, err=%v", storedSession, err)
	}
	var stop models.RecordingOperation
	if err := env.db.Where("recording_session_id = ? AND action = ?", session.ID, recordingActionStop).First(&stop).Error; err != nil || stop.Status != "failed" || stop.ErrorCode != "recording_actions_invalid" {
		t.Fatalf("coordinator invalid final Stop operation = %+v, err=%v", stop, err)
	}
}

func TestP476CaptureRejectsMissingRuntimeSnapshotReceiptIdentity(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.omitSnapshotReceiptID = true
	runtime.nextStorageState = contractStorageState("https://example.invalid/app/home")
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "stopped", "missing-snapshot-receipt")

	_, err := service.Capture(t.Context(), captureRecordingLifecycleInput{
		OperationID:     "b68bb39a-d4f2-408c-88af-0c0065ed2fd1",
		ProjectID:       project.ID,
		VersionID:       version.ID,
		Session:         session,
		Name:            "missing receipt",
		CapturedURL:     "https://example.invalid/app/home",
		OriginAllowlist: []string{"https://example.invalid"},
	})
	requireP476LifecycleError(t, err, http.StatusConflict, "runtime_receipt_unavailable")
	var operation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", "b68bb39a-d4f2-408c-88af-0c0065ed2fd1").First(&operation).Error; err != nil || operation.Status != "failed" || operation.ErrorCode != "runtime_receipt_unavailable" {
		t.Fatalf("Capture missing receipt operation = %+v, err=%v", operation, err)
	}
}

func TestP476CaptureRechecksReplaceInsideProjectVersionLock(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.nextStorageState = contractStorageState("https://example.invalid/app/captured")
	runtime.captureEntered = make(chan struct{})
	releaseCapture := make(chan struct{})
	runtime.releaseCapture = releaseCapture
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "stopped", "capture-recheck")
	replace := false

	done := make(chan error, 1)
	go func() {
		_, err := service.Capture(context.Background(), captureRecordingLifecycleInput{
			OperationID:     "91d0f263-ec62-42ae-9a60-fd6b0ace41a6",
			ProjectID:       project.ID,
			VersionID:       version.ID,
			Session:         session,
			Name:            "do not replace",
			CapturedURL:     "https://example.invalid/app/captured",
			OriginAllowlist: []string{"https://example.invalid"},
			Replace:         &replace,
		})
		done <- err
	}()
	<-runtime.captureEntered
	previous := env.seedProjectAuthState(t, project.ID, version.ID, page.ID, contractStorageState("https://example.invalid/app/existing"))
	close(releaseCapture)
	requireP476LifecycleError(t, <-done, http.StatusBadRequest, "project_auth_state_exists")

	var active []models.ProjectAuthState
	if err := env.db.Where("project_id = ? AND version_id = ? AND status = ?", project.ID, version.ID, "active").Find(&active).Error; err != nil || len(active) != 1 || active[0].ID != previous.ID {
		t.Fatalf("replace=false Capture replaced concurrent active state: active=%+v err=%v", active, err)
	}
}

func TestP476StopStoreFailureRetainsClaimedReceiptsForSameOperationRetry(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.stopPublishesPending = true
	runtime.stopResult = map[string]any{
		"actions": []models.ScriptAction{{Type: "click", Selector: "#confirm"}},
		"dom_snapshot": map[string]any{
			"schema_version": 1,
			"kind":           "semantic_dom_snapshot",
			"url":            "https://example.invalid/confirm",
			"title":          "Confirm",
			"elements":       []map[string]any{{"tag": "button", "selector": "#confirm"}},
		},
		"runtime_final_receipt_id":    "stop-store-failure-receipt",
		"runtime_final_sync_revision": uint64(2),
	}
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "stop-store-failure")
	runtime.recordingActive = true
	runtime.recordingSessionID = fmt.Sprint(session.ID)
	runtime.setPendingAuthStorageSnapshot(fmt.Sprint(session.ID), contractStorageState("https://example.invalid/confirm"))

	env.failNextRecordingSessionUpdate(t)
	operationID := "a89a1e66-3622-4a8c-8e21-3316a7d0e88d"
	_, err := service.Stop(t.Context(), stopRecordingLifecycleInput{OperationID: operationID, Session: session})
	requireP476LifecycleError(t, err, http.StatusInternalServerError, "recording_lifecycle_store_failed")
	runtime.requirePendingStoppedSession(t, fmt.Sprint(session.ID))
	runtime.requirePendingAuthStorageSnapshot(t, fmt.Sprint(session.ID))

	var pending models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&pending).Error; err != nil || pending.Status != "pending" {
		t.Fatalf("Stop after store failure = %+v, err=%v; want pending operation", pending, err)
	}

	result, err := service.Stop(t.Context(), stopRecordingLifecycleInput{OperationID: operationID, Session: session})
	if err != nil || result.Status != http.StatusOK {
		t.Fatalf("same Stop retry = %#v, err=%v", result, err)
	}
	runtime.requireAcknowledgedStoppedSession(t, fmt.Sprint(session.ID))
}

func TestP476StopCASRetryExhaustionKeepsPendingOperationAndReceipt(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.stopPublishesPending = true
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "stop-cas-retry")
	actions, err := json.Marshal([]models.ScriptAction{{Type: "click", Selector: "#retry"}})
	if err != nil {
		t.Fatalf("marshal actions: %v", err)
	}
	dom := json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/retry","title":"Retry","elements":[]}`)
	syncHash := canonicalRequestHash(map[string]any{"sync_revision": uint64(1), "actions": json.RawMessage(actions), "dom_snapshot": dom})
	if err := env.db.Model(&models.RecordingSession{}).Where("id = ?", session.ID).Updates(map[string]any{
		"actions_json": string(actions), "dom_snapshot": string(dom), "sync_payload_hash": syncHash,
		"sync_revision": uint64(0), "draft_hash": "",
	}).Error; err != nil {
		t.Fatalf("prepare persisted draft: %v", err)
	}
	runtime.recordingActive = true
	runtime.recordingSessionID = fmt.Sprint(session.ID)
	runtime.stopResult = map[string]any{
		"actions":                     []models.ScriptAction{{Type: "click", Selector: "#retry"}},
		"dom_snapshot":                json.RawMessage(dom),
		"runtime_final_receipt_id":    "stop-cas-retry-receipt",
		"runtime_final_sync_revision": uint64(1),
	}

	callbackName := fmt.Sprintf("p476_stop_cas_conflict_%d", time.Now().UnixNano())
	if err := env.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Name == "RecordingSession" {
			tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "1 = 0"}}})
		}
	}); err != nil {
		t.Fatalf("register CAS conflict callback: %v", err)
	}
	t.Cleanup(func() { _ = env.db.Callback().Update().Remove(callbackName) })

	operationID := "22aeb760-8360-4173-8111-a9c19b9d18bb"
	_, err = service.Stop(t.Context(), stopRecordingLifecycleInput{OperationID: operationID, Session: session})
	requireP476LifecycleError(t, err, http.StatusConflict, "recording_operation_in_progress")
	if retry, ok := err.(*recordingLifecycleError); !ok || retry.RetryAfter != 1 {
		t.Fatalf("Stop retry exhaustion = %#v, want Retry-After 1", err)
	}
	var pending models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&pending).Error; err != nil || pending.Status != "pending" {
		t.Fatalf("Stop retry exhaustion operation = %+v err=%v, want pending", pending, err)
	}
	runtime.requirePendingStoppedSession(t, fmt.Sprint(session.ID))
}

func TestP476LeaseLostForTerminalSessionOnlyAcknowledgesTombstone(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	service := NewRecordingLifecycleService(env.db, nil, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "stopped", "terminal-lease-lost")

	if err := service.RecoverRuntimeLeaseLost(t.Context(), session, "terminal-receipt"); err != nil {
		t.Fatalf("terminal lease-lost recovery: %v", err)
	}
	var operations int64
	if err := env.db.Model(&models.RecordingOperation{}).Where("recording_session_id = ?", session.ID).Count(&operations).Error; err != nil || operations != 0 {
		t.Fatalf("terminal lease-lost created operations=%d err=%v", operations, err)
	}
}

func TestP476RuntimeStoppedEventPersistsNewerSemanticDOMSnapshot(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "semantic-dom")
	runtime.recordingActive = true
	runtime.recordingSessionID = fmt.Sprint(session.ID)
	runtime.stopResult = map[string]any{"actions": []models.ScriptAction{{Type: "click", Selector: "#login"}}}
	coordinator := NewRecordingRecoveryCoordinator(service, nil)
	dom := json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/login","title":"Login","elements":[{"tag":"button","selector":"#login"}]}`)
	event := browser.RecordingRuntimeEvent{
		ID:           "semantic-dom-final",
		Kind:         "recording_stopped",
		ReceiptID:    "semantic-dom-receipt",
		SyncRevision: 2,
		Actions:      []models.ScriptAction{{Type: "click", Selector: "#login"}},
		DOMSnapshot:  dom,
		Scope:        browser.RecordingStorageScope{ProjectID: project.ID, VersionID: version.ID, PageID: page.ID, RecordingSessionID: fmt.Sprint(session.ID), BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration},
	}
	if outcome := coordinator.processRuntimeEvent(t.Context(), event); outcome.disposition != runtimeEventDispositionAcknowledge || outcome.err != nil {
		t.Fatalf("process recording_stopped event outcome = %+v", outcome)
	}
	var stopped models.RecordingSession
	if err := env.db.First(&stopped, session.ID).Error; err != nil || stopped.Status != "stopped" || stopped.SyncRevision != 2 || !strings.Contains(stopped.DOMSnapshot, "semantic_dom_snapshot") || !strings.Contains(stopped.DOMSnapshot, "#login") {
		t.Fatalf("stopped semantic DOM = %+v, err=%v", stopped, err)
	}
}

func TestP476DirectStopPersistsHigherFinalReceiptRevisionBeforeStopping(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.stopResult = map[string]any{
		"actions":                     []models.ScriptAction{{Type: "click", Selector: "#final-submit"}},
		"dom_snapshot":                map[string]any{"schema_version": 1, "kind": "semantic_dom_snapshot", "title": "Final", "elements": []map[string]any{{"tag": "button", "selector": "#final-submit"}}},
		"runtime_final_receipt_id":    "direct-final-receipt",
		"runtime_final_sync_revision": uint64(2),
	}
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "direct-final-revision")
	if _, err := service.Stop(t.Context(), stopRecordingLifecycleInput{OperationID: "c1e220be-ffef-486d-bd3f-2efcec6a2c9d", Session: session}); err != nil {
		t.Fatalf("direct Stop: %v", err)
	}
	var stopped models.RecordingSession
	if err := env.db.First(&stopped, session.ID).Error; err != nil {
		t.Fatalf("load stopped session: %v", err)
	}
	if stopped.SyncRevision != 2 || !strings.Contains(stopped.ActionsJSON, "#final-submit") || !strings.Contains(stopped.DOMSnapshot, "#final-submit") {
		t.Fatalf("direct Stop did not persist final receipt as newer Sync: %+v", stopped)
	}
}

func TestP476StaleFinalReceiptPreservesNewerDraftAndDeduplicatesArtifacts(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.stopResult = map[string]any{
		"actions": []models.ScriptAction{{Type: "click", Selector: "#stale"}},
		"artifacts": []map[string]any{{
			"artifact_type": "download", "storage_backend": "local", "storage_path": "downloads/report.csv",
			"file_name": "report.csv", "mime_type": "text/csv", "size_bytes": 12,
		}},
	}
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "stale-final")
	currentActions := `[{"type":"click","selector":"#newer"}]`
	currentDOM := `{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/newer","elements":[{"tag":"button","selector":"#newer"}]}`
	if err := env.db.Model(&models.RecordingSession{}).Where("id = ?", session.ID).Updates(map[string]any{
		"sync_revision": 3, "sync_payload_hash": "sync-newer", "draft_hash": "draft-newer", "actions_json": currentActions,
		"dom_snapshot": currentDOM, "action_count": 1,
	}).Error; err != nil {
		t.Fatalf("seed newer persisted draft: %v", err)
	}
	if err := env.db.First(&session, session.ID).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	coordinator := NewRecordingRecoveryCoordinator(service, nil)
	event := browser.RecordingRuntimeEvent{
		ID: "stale-final-event", Kind: "recording_stopped", ReceiptID: "stale-final-receipt", SyncRevision: 2,
		Actions:     []models.ScriptAction{{Type: "click", Selector: "#stale"}},
		DOMSnapshot: json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/stale"}`),
		Scope:       browser.RecordingStorageScope{ProjectID: project.ID, VersionID: version.ID, PageID: page.ID, RecordingSessionID: fmt.Sprint(session.ID), BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration},
	}
	for attempt := 0; attempt < 2; attempt++ {
		if outcome := coordinator.processRuntimeEvent(t.Context(), event); outcome.disposition != runtimeEventDispositionAcknowledge || outcome.err != nil {
			t.Fatalf("stale final outcome attempt %d = %+v", attempt, outcome)
		}
	}
	var stopped models.RecordingSession
	if err := env.db.First(&stopped, session.ID).Error; err != nil {
		t.Fatalf("load stopped session: %v", err)
	}
	expected, err := NewRecordingNormalizer().NormalizeFinal(session, nil)
	if err != nil {
		t.Fatalf("normalize expected persisted draft: %v", err)
	}
	if stopped.Status != "stopped" || stopped.SyncRevision != 3 || stopped.ActionsJSON != expected.ActionsJSON || stopped.DOMSnapshot != expected.DOMSnapshot || stopped.DraftHash != expected.DraftHash {
		t.Fatalf("stale final overwrote persisted draft: %+v", stopped)
	}
	var artifacts []models.RecordingArtifact
	if err := env.db.Where("recording_session_id = ?", session.ID).Find(&artifacts).Error; err != nil {
		t.Fatalf("query stale final artifacts: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].SourceReceiptID != event.ReceiptID || artifacts[0].ArtifactFingerprint == "" {
		t.Fatalf("stale final artifact records = %+v", artifacts)
	}
}

func TestP476CommitStopRechecksReceiptRevisionInsideSessionLock(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	service := NewRecordingLifecycleService(env.db, newContractP45Runtime(), env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "stop-lock-revision")
	sessionID := session.ID
	opID := "0b11e41f-0b0e-4d53-b1f9-9b0e0e910a79"
	p476PendingOperation(t, env, opID, recordingActionStop, session, "stop:"+fmt.Sprint(session.ID))
	var operation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", opID).First(&operation).Error; err != nil {
		t.Fatalf("load Stop operation: %v", err)
	}
	newerActions := `[{"type":"click","selector":"#newer-draft"}]`
	newerDOM := `{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/newer","elements":[{"tag":"button","selector":"#newer-draft"}]}`
	if err := env.db.Model(&models.RecordingSession{}).Where("id = ?", sessionID).Updates(map[string]any{
		"sync_revision": 3, "sync_payload_hash": "sync-newer", "draft_hash": "draft-newer", "actions_json": newerActions, "dom_snapshot": newerDOM,
	}).Error; err != nil {
		t.Fatalf("commit concurrent Sync before Stop transaction: %v", err)
	}
	if _, err := service.commitStop(t.Context(), operation, session, map[string]any{
		"actions":      []models.ScriptAction{{Type: "click", Selector: "#stale-final"}},
		"dom_snapshot": map[string]any{"schema_version": 1, "kind": "semantic_dom_snapshot", "url": "https://example.invalid/stale"},
	}, stopRecordingLifecycleInput{OperationID: opID, Session: session, FinalReceiptID: "lock-stale-receipt", FinalReceiptRevision: 2}); err != nil {
		t.Fatalf("commit Stop with stale final receipt: %v", err)
	}
	var stopped models.RecordingSession
	if err := env.db.First(&stopped, sessionID).Error; err != nil {
		t.Fatalf("load stopped session: %v", err)
	}
	expectedSession := session
	expectedSession.ActionsJSON = newerActions
	expectedSession.DOMSnapshot = newerDOM
	expected, err := NewRecordingNormalizer().NormalizeFinal(expectedSession, nil)
	if err != nil {
		t.Fatalf("normalize persisted newer draft: %v", err)
	}
	if stopped.ActionsJSON != expected.ActionsJSON || stopped.DOMSnapshot != expected.DOMSnapshot || stopped.DraftHash != expected.DraftHash || stopped.SyncRevision != 3 {
		t.Fatalf("stale final receipt overwrote current locked draft: %+v", stopped)
	}
}

func TestP476EqualFinalReceiptRevisionWithDifferentPayloadFailsDurably(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.stopPublishesPending = true
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "equal-final-conflict")
	persistedActions := json.RawMessage(`[{"type":"click","selector":"#persisted"}]`)
	persistedDOM := json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/persisted"}`)
	if err := env.db.Model(&models.RecordingSession{}).Where("id = ?", session.ID).Updates(map[string]any{
		"sync_revision":     1,
		"sync_payload_hash": canonicalRequestHash(map[string]any{"sync_revision": uint64(1), "actions": persistedActions, "dom_snapshot": persistedDOM}),
		"actions_json":      string(persistedActions),
		"dom_snapshot":      string(persistedDOM),
		"draft_hash":        "equal-final-persisted-draft",
	}).Error; err != nil {
		t.Fatalf("seed persisted sync: %v", err)
	}
	if err := env.db.First(&session, session.ID).Error; err != nil {
		t.Fatalf("reload session: %v", err)
	}
	runtime.recordingActive = true
	runtime.recordingSessionID = fmt.Sprint(session.ID)
	runtime.stopResult = map[string]any{
		"actions":                     []models.ScriptAction{{Type: "click", Selector: "#different"}},
		"dom_snapshot":                map[string]any{"schema_version": 1, "kind": "semantic_dom_snapshot", "url": "https://example.invalid/persisted"},
		"runtime_final_receipt_id":    "equal-final-conflict-receipt",
		"runtime_final_sync_revision": uint64(1),
	}

	operationID := "2831c7a0-c967-4379-b496-1c4f9b757dcb"
	_, err := service.Stop(t.Context(), stopRecordingLifecycleInput{OperationID: operationID, Session: session})
	requireP476LifecycleError(t, err, http.StatusConflict, "sync_revision_payload_conflict")

	var failedSession models.RecordingSession
	if err := env.db.First(&failedSession, session.ID).Error; err != nil || failedSession.Status != "failed" || failedSession.FailureCode != "sync_revision_payload_conflict" {
		t.Fatalf("equal-revision final session = %+v, err=%v", failedSession, err)
	}
	var failedOperation models.RecordingOperation
	if err := env.db.Where("operation_id = ?", operationID).First(&failedOperation).Error; err != nil || failedOperation.Status != "failed" || failedOperation.ErrorCode != "sync_revision_payload_conflict" {
		t.Fatalf("equal-revision final operation = %+v, err=%v", failedOperation, err)
	}
}

func TestP476EqualFinalReceiptRevisionIgnoresCapturedAtOnly(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "equal-final-captured-at")
	actions := json.RawMessage(`[{"type":"click","selector":"#persisted"}]`)
	persistedDOM := json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/persisted","title":"Persisted","captured_at":"2026-07-30T00:00:00Z","elements":[{"tag":"button","selector":"#persisted"}]}`)
	if _, err := service.Sync(t.Context(), syncRecordingLifecycleInput{
		OperationID:  "0d537579-640a-41a5-b99f-e62081c4d0d8",
		Session:      session,
		SyncRevision: 2,
		Actions:      actions,
		DOMSnapshot:  persistedDOM,
	}); err != nil {
		t.Fatalf("persist initial sync: %v", err)
	}
	if err := env.db.First(&session, session.ID).Error; err != nil {
		t.Fatalf("reload synced session: %v", err)
	}
	runtime.stopResult = map[string]any{
		"actions": []models.ScriptAction{{Type: "click", Selector: "#persisted"}},
		"dom_snapshot": map[string]any{
			"schema_version": 1, "kind": "semantic_dom_snapshot", "url": "https://example.invalid/persisted", "title": "Persisted",
			"captured_at": "2026-07-30T00:00:01Z", "elements": []map[string]any{{"tag": "button", "selector": "#persisted"}},
		},
		"runtime_final_receipt_id":               "captured-at-equivalent-final",
		"runtime_final_receipt_claim_generation": uint64(1),
		"runtime_final_sync_revision":            uint64(2),
	}
	if _, err := service.Stop(t.Context(), stopRecordingLifecycleInput{OperationID: "de0ecce9-9cd9-4549-a269-152ae68ee6df", Session: session}); err != nil {
		t.Fatalf("Stop with captured_at-only final difference: %v", err)
	}
	var stopped models.RecordingSession
	if err := env.db.First(&stopped, session.ID).Error; err != nil {
		t.Fatalf("load stopped session: %v", err)
	}
	if stopped.Status != "stopped" || stopped.FailureCode != "" || stopped.SyncRevision != 2 {
		t.Fatalf("captured_at-only final difference must preserve successful session: %+v", stopped)
	}
}

func TestP476StartRuntimeFailureReplaysItsDurable409(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	runtime.startErr = fmt.Errorf("browser target is unavailable")
	env.installProjectAuthRuntimeFake(t, runtime)
	payload := map[string]any{
		"operation_id": "2cf268e2-7a80-4ea4-a9dc-b68f23c775f4", "recording_kind": recordingKindLoginFlow,
		"auth_context": authContextClean, "browser_instance_id": "runtime-failure-instance", "runtime_page_id": "runtime-failure-page",
	}
	first := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, payload)
	env.requireStatus(t, first, http.StatusConflict)
	if got := env.decodeObject(t, first)["code"]; got != "runtime_lease_lost" {
		t.Fatalf("first runtime failure code = %v", got)
	}
	retry := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, payload)
	env.requireStatus(t, retry, http.StatusConflict)
	if got := env.decodeObject(t, retry)["code"]; got != "runtime_lease_lost" {
		t.Fatalf("replayed runtime failure code = %v", got)
	}
	var op models.RecordingOperation
	if err := env.db.Where("operation_id = ?", payload["operation_id"]).First(&op).Error; err != nil || op.Status != "failed" || op.HTTPStatus != http.StatusConflict || op.ErrorCode != "runtime_lease_lost" {
		t.Fatalf("runtime failure receipt = %+v, err=%v", op, err)
	}
}

func TestP476CompletedCaptureReplayAcknowledgesItsClaimedSnapshot(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	session := p476StoppedSession(t, env, project.ID, version.ID, page.ID, 0, "capture-replay-ack")
	session.BrowserInstanceID = "capture-replay-instance"
	session.RuntimePageID = "capture-replay-page"
	session.RuntimeGeneration = "capture-replay-runtime"
	session.LeaseGeneration = "capture-replay-lease"
	if err := env.db.Save(&session).Error; err != nil {
		t.Fatalf("persist capture session runtime scope: %v", err)
	}
	operationID := "6c958c09-59e7-423a-89a8-df8ad9538a1a"
	input := captureRecordingLifecycleInput{OperationID: operationID, ProjectID: project.ID, VersionID: version.ID, Session: session, Name: "登录态", CapturedURL: "https://example.invalid/app"}
	response, err := json.Marshal(map[string]any{"auth_state": map[string]any{"id": 1}})
	if err != nil {
		t.Fatalf("marshal completed Capture response: %v", err)
	}
	sessionID := session.ID
	op := models.RecordingOperation{
		OperationID: operationID, Action: recordingActionCapture, Scope: recordingSessionScope(session),
		RequestPayloadHash: canonicalCaptureRequestHash(input), RequestCanonicalizerVersion: requestCanonicalizerVersion,
		Status: "completed", RecordingSessionID: &sessionID, ProjectID: session.ProjectID, VersionID: session.VersionID, PageID: session.PageID,
		BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration,
		ReceiptID: "capture-replay-receipt", RuntimeReceiptClaimGeneration: 1, HTTPStatus: http.StatusOK, SanitizedResponseJSON: string(response),
	}
	if err := env.db.Create(&op).Error; err != nil {
		t.Fatalf("persist completed Capture operation: %v", err)
	}
	runtime.mu.Lock()
	runtime.pendingAuthStorageSessionID = fmt.Sprint(session.ID)
	runtime.pendingAuthStorageState = contractStorageState("https://example.invalid/app")
	runtime.mu.Unlock()
	if _, err := service.Capture(t.Context(), input); err != nil {
		t.Fatalf("replay completed Capture: %v", err)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if len(runtime.acknowledgedAuthSessions) != 1 || runtime.acknowledgedAuthSessions[0] != fmt.Sprint(session.ID) || runtime.pendingAuthStorageState != nil {
		t.Fatalf("completed Capture replay did not ACK its stored runtime claim: acked=%v pending=%v", runtime.acknowledgedAuthSessions, runtime.pendingAuthStorageState)
	}
}

func TestP476LeaseLostInvalidDraftClosesSessionBeforeReleasingTombstone(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "lease-lost-invalid-draft")
	service := NewRecordingLifecycleService(env.db, nil, env.handler.config)
	coordinator := NewRecordingRecoveryCoordinator(service, nil)
	event := browser.RecordingRuntimeEvent{
		ID: "lease-lost-invalid-draft-event", Kind: "runtime_lease_lost", ReceiptID: "lease-lost-invalid-draft-receipt", SyncRevision: session.SyncRevision + 1,
		Actions:     []models.ScriptAction{{}},
		DOMSnapshot: json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/login"}`),
		Scope:       browser.RecordingStorageScope{ProjectID: project.ID, VersionID: version.ID, PageID: page.ID, RecordingSessionID: fmt.Sprint(session.ID), BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration},
	}
	outcome := coordinator.processRuntimeEvent(t.Context(), event)
	if outcome.disposition != runtimeEventDispositionRelease {
		t.Fatalf("invalid lease-lost outcome = %+v, want durable release", outcome)
	}
	requireP476LifecycleError(t, outcome.err, http.StatusUnprocessableEntity, "recording_actions_invalid")
	var failedSession models.RecordingSession
	if err := env.db.First(&failedSession, session.ID).Error; err != nil || failedSession.Status != "failed" || failedSession.FailureCode != "recording_actions_invalid" {
		t.Fatalf("invalid lease-lost session = %+v, err=%v", failedSession, err)
	}
	var stop models.RecordingOperation
	if err := env.db.Where("recording_session_id = ? AND action = ?", session.ID, recordingActionStop).First(&stop).Error; err != nil || stop.Status != "failed" || stop.ErrorCode != "recording_actions_invalid" {
		t.Fatalf("invalid lease-lost Stop operation = %+v, err=%v", stop, err)
	}
}

func TestP476NormalizerRejectsMalformedHistoricalActionAndRecoveryRequiresSemanticDOM(t *testing.T) {
	normalizer := NewRecordingNormalizer()
	_, err := normalizer.NormalizePageScript(models.PageScript{
		ActionTrace:       `[{}]`,
		DOMSnapshot:       `{}`,
		RecordingMetaJSON: `{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean","target_url":"https://example.invalid"}`,
	})
	if fmt.Sprint(err) != "recording_actions_invalid" {
		t.Fatalf("NormalizePageScript malformed action error = %v", err)
	}

	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	draft := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "recoverable-dom")
	draft.DOMSnapshot = `{}`
	if IsRecoverableRecordingDraft(draft) {
		t.Fatal("arbitrary DOM object must not make a runtime-loss draft recoverable")
	}
	draft.DOMSnapshot = `{"unavailable":true}`
	if !IsRecoverableRecordingDraft(draft) {
		t.Fatal("explicit unavailable semantic DOM marker must remain recoverable")
	}
}

func TestP476ScopeMismatchReleasesOnlyAfterDurableFailure(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "scope-mismatch")
	runtime := newContractP45Runtime()
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	coordinator := NewRecordingRecoveryCoordinator(service, nil)
	event := browser.RecordingRuntimeEvent{
		ID: "scope-mismatch-event", Kind: "runtime_lease_lost", ReceiptID: "scope-mismatch-receipt",
		Scope: browser.RecordingStorageScope{ProjectID: project.ID, VersionID: version.ID, PageID: page.ID, RecordingSessionID: fmt.Sprint(session.ID), BrowserInstanceID: "wrong-instance", RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration},
	}
	if outcome := coordinator.processRuntimeEvent(t.Context(), event); outcome.disposition != runtimeEventDispositionRelease || lifecycleCode(outcome.err) != "runtime_receipt_scope_mismatch" {
		t.Fatalf("scope mismatch outcome = %+v", outcome)
	}
	var failed models.RecordingSession
	if err := env.db.First(&failed, session.ID).Error; err != nil || failed.Status != "failed" || failed.FailureCode != "runtime_receipt_scope_mismatch" {
		t.Fatalf("scope mismatch session = %+v err=%v", failed, err)
	}
	var operation models.RecordingOperation
	if err := env.db.Where("recording_session_id = ? AND action = ?", session.ID, recordingActionStop).First(&operation).Error; err != nil || operation.Status != "failed" || operation.ErrorCode != "runtime_receipt_scope_mismatch" {
		t.Fatalf("scope mismatch Stop operation = %+v err=%v", operation, err)
	}
	runtime.requireEvents(t, "stop_recording")
}

func TestP476StartingSessionDefersDraftAndFinalRuntimeEvents(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "starting", "starting-event")
	service := NewRecordingLifecycleService(env.db, newContractP45Runtime(), env.handler.config)
	coordinator := NewRecordingRecoveryCoordinator(service, nil)
	scope := browser.RecordingStorageScope{ProjectID: project.ID, VersionID: version.ID, PageID: page.ID, RecordingSessionID: fmt.Sprint(session.ID), BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration}
	for _, event := range []browser.RecordingRuntimeEvent{
		{ID: "starting-draft", Kind: "draft_sync", Scope: scope, SyncRevision: session.SyncRevision + 1},
		{ID: "starting-final", Kind: "recording_stopped", ReceiptID: "starting-final-receipt", Scope: scope, SyncRevision: session.SyncRevision + 1},
	} {
		outcome := coordinator.processRuntimeEvent(t.Context(), event)
		if outcome.disposition != runtimeEventDispositionRetry || lifecycleCode(outcome.err) != "recording_operation_in_progress" {
			t.Fatalf("starting event %s outcome = %+v, want retry", event.Kind, outcome)
		}
	}
}

func TestP476LeaseLostCanRecoverCompleteZeroActionDraft(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	session := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "zero-actions")
	if err := env.db.Model(&models.RecordingSession{}).Where("id = ?", session.ID).Updates(map[string]any{
		"actions_json": "[]", "action_count": 0, "sync_revision": 1, "sync_payload_hash": "zero-sync", "draft_hash": "zero-draft",
		"dom_snapshot": `{"unavailable":true}`,
	}).Error; err != nil {
		t.Fatalf("seed zero-action draft: %v", err)
	}
	if err := env.db.First(&session, session.ID).Error; err != nil {
		t.Fatalf("reload zero-action session: %v", err)
	}
	service := NewRecordingLifecycleService(env.db, nil, env.handler.config)
	coordinator := NewRecordingRecoveryCoordinator(service, nil)
	event := browser.RecordingRuntimeEvent{
		ID: "zero-actions-lease-lost", Kind: "runtime_lease_lost", ReceiptID: "zero-actions-receipt", SyncRevision: session.SyncRevision,
		Actions: nil, DOMSnapshot: json.RawMessage(`{"unavailable":true}`),
		Scope: browser.RecordingStorageScope{ProjectID: project.ID, VersionID: version.ID, PageID: page.ID, RecordingSessionID: fmt.Sprint(session.ID), BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration},
	}
	if outcome := coordinator.processRuntimeEvent(t.Context(), event); outcome.disposition != runtimeEventDispositionAcknowledge || outcome.err != nil {
		t.Fatalf("zero-action lease-lost outcome = %+v", outcome)
	}
	var stopped models.RecordingSession
	if err := env.db.First(&stopped, session.ID).Error; err != nil || stopped.Status != "stopped" || stopped.ActionCount != 0 {
		t.Fatalf("zero-action lease-lost session = %+v err=%v", stopped, err)
	}
}

func TestP476CancelStartingFailsItsPendingStartAndEffectConflictsDoNotCreateReceipts(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	runtime := newContractP45Runtime()
	service := NewRecordingLifecycleService(env.db, runtime, env.handler.config)
	starting := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "starting", "starting")
	startID := "ce2c0445-9b63-4e0f-b7ac-15d0c1964b69"
	p476PendingOperation(t, env, startID, recordingActionStart, starting, "start:"+starting.BrowserInstanceID)

	_, err := service.Cancel(t.Context(), cancelRecordingLifecycleInput{OperationID: "781e2ca2-9cfa-4b2c-9957-5dd0f93d25af", Session: starting})
	if err != nil {
		t.Fatalf("cancel starting session: %v", err)
	}
	var start models.RecordingOperation
	if err := env.db.Where("operation_id = ?", startID).First(&start).Error; err != nil || start.Status != "failed" || start.ErrorCode != "start_cancelled" {
		t.Fatalf("Start operation after Cancel = %+v, err=%v", start, err)
	}

	recording := p476RecordingSession(t, env, project.ID, version.ID, page.ID, "recording", "effect")
	p476PendingOperation(t, env, "5e8c8212-a808-49cf-9b89-67a1e96beb3b", recordingActionStop, recording, "stop:"+fmt.Sprint(recording.ID))
	_, err = service.Stop(t.Context(), stopRecordingLifecycleInput{OperationID: "2c050c38-4ec6-4d66-b57c-5b27984cc024", Session: recording})
	requireP476LifecycleError(t, err, http.StatusConflict, "recording_operation_in_progress")
	var count int64
	if err := env.db.Model(&models.RecordingOperation{}).Where("recording_session_id = ? AND action = ?", recording.ID, recordingActionStop).Count(&count).Error; err != nil {
		t.Fatalf("count Stop operations: %v", err)
	}
	if count != 1 {
		t.Fatalf("second pending-effect request persisted an operation: count=%d", count)
	}
}

func TestP476NormalizerExcludesSecretsFromPageScriptAndRecordingSource(t *testing.T) {
	normalizer := NewRecordingNormalizer()
	session := models.RecordingSession{
		ActionsJSON:       `[{"type":"click","selector":"#login","token":"secret"},{"type":"download","url":"data:text/plain,secret"}]`,
		DOMSnapshot:       `{"password":"secret","node":"safe"}`,
		RecordingMetaJSON: `{"schema_version":1,"recording_kind":"login_flow","auth_context":"clean","target_url":"https://example.invalid"}`,
	}
	normalized, err := normalizer.NormalizeFinal(session, nil)
	if err != nil {
		t.Fatalf("NormalizeFinal: %v", err)
	}
	combined := normalized.ActionsJSON + normalized.DOMSnapshot + fmt.Sprint(normalized.RecordingSource)
	if strings.Contains(combined, "secret") || strings.Contains(combined, "data:") {
		t.Fatalf("normalized recording leaked sensitive/untrusted data: %s", combined)
	}
	var actions []map[string]any
	if err := json.Unmarshal([]byte(normalized.ActionsJSON), &actions); err != nil || len(actions) != 1 || actions[0]["type"] != "click" {
		t.Fatalf("unsafe download was not dropped independently: actions=%s err=%v", normalized.ActionsJSON, err)
	}
}

func TestP476NormalizerSanitizesCredentialsFromEveryURLBearingField(t *testing.T) {
	normalizer := NewRecordingNormalizer()
	session := models.RecordingSession{
		ActionsJSON:       `[{"type":"navigate","url":"https://alice:password@example.invalid/orders?access_token=token-value&safe=1#fragment"}]`,
		DOMSnapshot:       `{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://alice:password@example.invalid/orders?signature=signed-value","title":"Orders","elements":[{"href":"https://example.invalid/download?token=secret-token"}]}`,
		RecordingMetaJSON: `{"schema_version":1,"recording_kind":"business_flow","auth_context":"clean","target_url":"https://alice:password@example.invalid/orders?api_key=key-value&safe=1#fragment"}`,
	}
	normalized, err := normalizer.NormalizeFinal(session, nil)
	if err != nil {
		t.Fatalf("NormalizeFinal: %v", err)
	}
	combined := normalized.ActionsJSON + normalized.DOMSnapshot + normalized.RecordingMetaJSON + fmt.Sprint(normalized.RecordingSource)
	for _, secret := range []string{"alice:password", "token-value", "signed-value", "secret-token", "key-value", "#fragment"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("URL credential leaked into normalized recording: %q in %s", secret, combined)
		}
	}
	if !strings.Contains(combined, "safe=1") {
		t.Fatalf("safe URL query was unexpectedly removed: %s", combined)
	}
}

func TestP476NormalizerRedactsSensitiveInputValuesWithPolicyV1(t *testing.T) {
	normalizer := NewRecordingNormalizer()
	meta := `{"schema_version":1,"recording_kind":"login_flow","auth_context":"clean","target_url":"https://example.invalid/login"}`
	cases := []struct {
		name   string
		action string
		secret string
	}{
		{"password type", `{"type":"input","value":"p4ssw0rd","attrs":{"type":"password","name":"login-password"},"accessibility":{"name":"p4ssw0rd","value":"p4ssw0rd"},"intent":{"object":"p4ssw0rd"}}`, "p4ssw0rd"},
		{"otp autocomplete", `{"type":"input","value":"839201","attrs":{"autocomplete":"one-time-code","placeholder":"验证码"},"accessibility":{"value":"839201"}}`, "839201"},
		{"api key label", `{"type":"input","value":"api-key-value","attrs":{"aria-label":"API-Key"},"accessibility":{"value":"api-key-value"}}`, "api-key-value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := models.RecordingSession{
				ActionsJSON:       `[` + tc.action + `]`,
				DOMSnapshot:       `{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/login","title":"Login","elements":[]}`,
				RecordingMetaJSON: meta,
			}
			normalized, err := normalizer.NormalizeFinal(session, nil)
			if err != nil {
				t.Fatalf("NormalizeFinal: %v", err)
			}
			combined := normalized.ActionsJSON + normalized.PageScriptContentHash + normalized.RecordingSourceHash + fmt.Sprint(normalized.RecordingSource)
			if strings.Contains(combined, tc.secret) {
				t.Fatalf("sensitive value leaked after normalization: %s", combined)
			}
			var actions []map[string]any
			if err := json.Unmarshal([]byte(normalized.ActionsJSON), &actions); err != nil || len(actions) != 1 {
				t.Fatalf("normalized actions = %s err=%v", normalized.ActionsJSON, err)
			}
			if actions[0]["sensitive_input"] != true || actions[0]["value"] != sensitiveInputPlaceholder {
				t.Fatalf("sensitive action marker/value = %#v", actions[0])
			}
			accessibility, _ := actions[0]["accessibility"].(map[string]any)
			if accessibility["value"] != sensitiveInputPlaceholder {
				t.Fatalf("sensitive accessibility value = %#v", accessibility)
			}
			if accessibility["name"] == tc.secret {
				t.Fatalf("sensitive accessibility name leaked: %#v", accessibility)
			}
			intent, _ := actions[0]["intent"].(map[string]any)
			if intent["object"] == tc.secret {
				t.Fatalf("sensitive intent object leaked: %#v", intent)
			}
		})
	}

	normalized, err := normalizer.NormalizeFinal(models.RecordingSession{
		ActionsJSON:       `[{"type":"input","value":"visible business input","attrs":{"name":"display_name"}}]`,
		DOMSnapshot:       `{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/profile","title":"Profile","elements":[]}`,
		RecordingMetaJSON: meta,
	}, nil)
	if err != nil {
		t.Fatalf("normalize ordinary input: %v", err)
	}
	if !strings.Contains(normalized.ActionsJSON, "visible business input") || strings.Contains(normalized.ActionsJSON, "sensitive_input") {
		t.Fatalf("ordinary input was unexpectedly redacted: %s", normalized.ActionsJSON)
	}
}

func TestP476SessionMetaOverrideCannotRewriteRecordingSource(t *testing.T) {
	authStateID := uint(41)
	session := models.RecordingSession{
		RecordingKind: recordingKindLoginFlow, AuthContext: authContextClean, SourceAuthStateID: &authStateID,
		TargetURL: "https://example.invalid/login", ActionsJSON: `[{"type":"click","selector":"#login"}]`, DOMSnapshot: `{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/login","title":"Login","elements":[]}`,
		RecordingMetaJSON: `{"schema_version":1,"recording_kind":"login_flow","auth_context":"clean","auth_state_id":41,"target_url":"https://example.invalid/login"}`,
	}
	normalizer := NewRecordingNormalizer()
	_, err := normalizer.NormalizeFinal(session, json.RawMessage(`{"schema_version":1,"recording_kind":"business_flow","auth_context":"project_saved","auth_state_id":41,"target_url":"https://example.invalid/orders"}`))
	if fmt.Sprint(err) != "recording_source_invalid" {
		t.Fatalf("rewritten session recording meta error = %v, want recording_source_invalid", err)
	}
	normalized, err := normalizer.NormalizeFinal(session, json.RawMessage(session.RecordingMetaJSON))
	if err != nil {
		t.Fatalf("normalize matching session meta: %v", err)
	}
	if !strings.Contains(normalized.RecordingMetaJSON, `"recording_kind":"login_flow"`) || !strings.Contains(normalized.RecordingMetaJSON, `"target_url":"https://example.invalid/login"`) {
		t.Fatalf("normalized session recording meta lost durable identity: %s", normalized.RecordingMetaJSON)
	}
}

func TestP476RecoverableDraftRequiresCompleteSemanticDOM(t *testing.T) {
	base := models.RecordingSession{
		SyncRevision:             1,
		SyncPayloadHash:          "sha256:sync",
		DraftHash:                "sha256:draft",
		DraftCompletenessVersion: recordingDraftCompletenessVersion,
		BrowserInstanceID:        "instance",
		RuntimePageID:            "page",
		RuntimeGeneration:        "runtime",
		ActionsJSON:              `[{"type":"click","selector":"#submit"}]`,
		RecordingMetaJSON:        `{"schema_version":1,"recording_kind":"login_flow","auth_context":"clean","target_url":"https://example.invalid/login"}`,
	}
	cases := []struct {
		name string
		dom  string
		want bool
	}{
		{"complete semantic snapshot", `{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/login","title":"Login","elements":[]}`, true},
		{"missing elements", `{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/login","title":"Login"}`, false},
		{"missing title", `{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/login","elements":[]}`, false},
		{"explicit unavailable", `{"unavailable":true}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := base
			session.DOMSnapshot = tc.dom
			if got := IsRecoverableRecordingDraft(session); got != tc.want {
				t.Fatalf("IsRecoverableRecordingDraft() = %v, want %v", got, tc.want)
			}
		})
	}
}

func p476StoppedSession(t *testing.T, env *generateContractEnv, projectID, versionID, pageID uint, baseFlow uint64, name string) models.RecordingSession {
	t.Helper()
	meta := `{"schema_version":1,"recording_kind":"login_flow","auth_context":"clean","target_url":"https://example.invalid/app/` + name + `"}`
	session := models.RecordingSession{
		ProjectID: projectID, VersionID: versionID, PageID: pageID, RecordingKind: recordingKindLoginFlow, AuthContext: authContextClean,
		TargetURL: "https://example.invalid/app/" + name,
		Status:    "stopped", LifecycleRevision: 1, SyncRevision: 1, SyncPayloadHash: "sync-" + name, DraftHash: "draft-" + name,
		DraftCompletenessVersion: recordingDraftCompletenessVersion, BasePageFlowRevision: baseFlow,
		ActionsJSON: `[{"type":"click","selector":"#` + name + `"}]`, ActionCount: 1,
		DOMSnapshot:       `{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/app/` + name + `","title":"P4.7.6","elements":[]}`,
		RecordingMetaJSON: meta,
	}
	if err := env.db.Create(&session).Error; err != nil {
		t.Fatalf("create stopped session %s: %v", name, err)
	}
	return session
}

func p476RecordingSession(t *testing.T, env *generateContractEnv, projectID, versionID, pageID uint, status, name string) models.RecordingSession {
	t.Helper()
	session := p476StoppedSession(t, env, projectID, versionID, pageID, 0, name)
	session.Status = status
	session.BrowserInstanceID = "p476-instance-" + name
	session.RuntimePageID = "p476-page-" + name
	session.RuntimeGeneration = "p476-runtime-" + name
	session.LeaseGeneration = "p476-lease-" + name
	if err := env.db.Save(&session).Error; err != nil {
		t.Fatalf("update P4.7.6 session %s: %v", name, err)
	}
	return session
}

func p476PendingOperation(t *testing.T, env *generateContractEnv, operationID, action string, session models.RecordingSession, effectKey string) {
	t.Helper()
	sessionID := session.ID
	op := models.RecordingOperation{
		OperationID: operationID, Action: action, Scope: recordingSessionScope(session), RequestPayloadHash: "sha256:p476-" + operationID,
		RequestCanonicalizerVersion: requestCanonicalizerVersion, Status: "pending", RuntimeEffectKey: runtimeEffectKey(effectKey),
		RecordingSessionID: &sessionID, ProjectID: session.ProjectID, VersionID: session.VersionID, PageID: session.PageID,
		BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration,
	}
	if err := env.db.Create(&op).Error; err != nil {
		t.Fatalf("create pending operation %s: %v", action, err)
	}
}

func requireP476LifecycleError(t *testing.T, err error, status int, code string) {
	t.Helper()
	lifecycle, ok := err.(*recordingLifecycleError)
	if !ok || lifecycle.Status != status || lifecycle.Code != code {
		t.Fatalf("lifecycle error = %#v, want status=%d code=%s", err, status, code)
	}
}

func requireP476ModelField(t *testing.T, model any, name string) {
	t.Helper()
	if _, ok := reflect.TypeOf(model).FieldByName(name); !ok {
		t.Fatalf("%T is missing required P4.7.6 field %s", model, name)
	}
}

func requireP476ErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v; body=%s", err, recorder.Body.String())
	}
	if got, _ := body["code"].(string); got != want {
		t.Fatalf("error code = %q, want %q; body=%s", got, want, recorder.Body.String())
	}
}

func p476FailNextRecordingOperationUpdate(t *testing.T, env *generateContractEnv) {
	t.Helper()
	callbackName := fmt.Sprintf("p476_fail_recording_operation_update_%d", time.Now().UnixNano())
	failed := false
	if err := env.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if !failed && tx.Statement != nil && tx.Statement.Table == "recording_operations" {
			failed = true
			tx.AddError(fmt.Errorf("p476 forced recording operation update failure"))
		}
	}); err != nil {
		t.Fatalf("register recording operation failure callback: %v", err)
	}
	t.Cleanup(func() { _ = env.db.Callback().Update().Remove(callbackName) })
}
