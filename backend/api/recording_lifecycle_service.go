package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/pkg/logger"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	recordingActionStart   = "start"
	recordingActionSync    = "sync"
	recordingActionStop    = "stop"
	recordingActionSave    = "save"
	recordingActionCapture = "capture"
	recordingActionCancel  = "cancel"
	startDriverClaimTTL    = 30 * time.Second
)

var errSaveCandidateStale = errors.New("recording save candidate is stale")

type RecordingLifecycleService struct {
	db         *gorm.DB
	runtime    projectAuthRuntime
	normalizer *RecordingNormalizer
	cipher     *projectAuthStateCipher
}

// recordingRuntimeScopeLocator avoids the legacy global runtime status
// queries during pending-operation recovery. A receipt/lease may only be
// reused when it belongs to the exact persisted session scope.
type recordingRuntimeScopeLocator interface {
	HasActivePageRecordingScope(context.Context, map[string]any) bool
	HasPendingStoppedPageRecordingScope(context.Context, map[string]any) bool
}

type recordingLifecycleError struct {
	Status     int
	Code       string
	Detail     string
	RetryAfter int
}

func (e *recordingLifecycleError) Error() string { return e.Code }

type recordingLifecycleResult struct {
	Status int
	Body   map[string]any
}

type startRecordingLifecycleInput struct {
	OperationID       string
	ProjectID         uint
	VersionID         uint
	PageID            uint
	RecordingKind     string
	AuthContext       string
	TargetURL         string
	BrowserInstanceID string
	RuntimePageID     string
	SourceAuthStateID *uint
	CreatedBy         string
}

type syncRecordingLifecycleInput struct {
	OperationID  string
	Session      models.RecordingSession
	SyncRevision uint64
	Actions      json.RawMessage
	DOMSnapshot  json.RawMessage
}

type stopRecordingLifecycleInput struct {
	OperationID                 string
	Session                     models.RecordingSession
	DOMSnapshot                 json.RawMessage
	FinalReceiptID              string
	FinalReceiptClaimGeneration uint64
	FinalReceiptRevision        uint64
	PreservePersistedDraft      bool
	// runtimeResult is supplied only by RecordingRecoveryCoordinator after it
	// has driven the scoped runtime Stop and published a recording_stopped
	// event. Keeping the frozen receipt here prevents the coordinator from
	// issuing a second browser Stop merely to persist that same event.
	runtimeResult map[string]any
}

type saveRecordingLifecycleInput struct {
	OperationID   string
	Session       models.RecordingSession
	Name          string
	RecordingMeta json.RawMessage
}

type captureRecordingLifecycleInput struct {
	OperationID     string
	ProjectID       uint
	VersionID       uint
	Session         models.RecordingSession
	Name            string
	CapturedURL     string
	OriginAllowlist []string
	Replace         *bool
}

type cancelRecordingLifecycleInput struct {
	OperationID string
	Session     models.RecordingSession
}

func NewRecordingLifecycleService(db *gorm.DB, runtime projectAuthRuntime, cfg *config.Config) *RecordingLifecycleService {
	cipher, _ := newProjectAuthStateCipher(cfg)
	return &RecordingLifecycleService{db: db, runtime: runtime, normalizer: NewRecordingNormalizer(), cipher: cipher}
}

func (h *ProjectHandlers) recordingLifecycleService() *RecordingLifecycleService {
	if h.lifecycle != nil {
		return h.lifecycle
	}
	return NewRecordingLifecycleService(h.gormDB(), h.projectAuthRuntime(), h.config)
}

func (s *RecordingLifecycleService) Start(ctx context.Context, input startRecordingLifecycleInput) (recordingLifecycleResult, error) {
	if err := validateOperationID(input.OperationID); err != nil {
		return recordingLifecycleResult{}, err
	}
	input.BrowserInstanceID = strings.TrimSpace(input.BrowserInstanceID)
	input.RuntimePageID = strings.TrimSpace(input.RuntimePageID)
	input.TargetURL = strings.TrimSpace(input.TargetURL)
	if strings.TrimSpace(input.BrowserInstanceID) == "" {
		return recordingLifecycleResult{}, lifecycleError(http.StatusBadRequest, "recording_source_invalid", "browser_instance_id is required")
	}
	if strings.TrimSpace(input.RuntimePageID) == "" {
		// The caller owns the runtime page identity. Generating it here would
		// change the request scope/hash on a response-loss retry.
		return recordingLifecycleResult{}, lifecycleError(http.StatusBadRequest, "recording_source_invalid", "runtime_page_id is required")
	}
	meta := p45RecordingMeta{SchemaVersion: 1, RecordingKind: strings.TrimSpace(input.RecordingKind), AuthContext: strings.TrimSpace(input.AuthContext), TargetURL: strings.TrimSpace(input.TargetURL)}
	if err := validateRecordingMeta(meta, false); err != nil {
		return recordingLifecycleResult{}, lifecycleError(http.StatusBadRequest, "recording_source_invalid", err.Error())
	}
	if s.runtime == nil {
		return recordingLifecycleResult{}, lifecycleError(http.StatusInternalServerError, "runtime_lease_lost", "project auth runtime is not configured")
	}
	requestHash := canonicalRequestHash(map[string]any{
		"recording_kind": meta.RecordingKind, "auth_context": meta.AuthContext, "target_url": meta.TargetURL,
		"browser_instance_id": input.BrowserInstanceID, "runtime_page_id": input.RuntimePageID,
	})
	scope := recordingStartScope(input)
	effectKey := "start:" + input.BrowserInstanceID
	var session models.RecordingSession
	var op models.RecordingOperation
	created := false
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		op, session, created, err = s.beginStart(tx, input, scope, requestHash, effectKey)
		return err
	}); err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	if !created {
		if op.Status == "completed" || op.Status == "failed" {
			return replayRecordingOperation(op)
		}
		if session.Status == "cancelled" {
			if err := s.failOperation(ctx, op.ID, "start_cancelled", "start was cancelled"); err != nil {
				return recordingLifecycleResult{}, translateRecordingDBError(err)
			}
			return recordingLifecycleResult{}, lifecycleError(http.StatusConflict, "start_cancelled", "recording start was cancelled")
		}
	}

	claimed, owned, err := s.claimStartRuntimeDriver(ctx, op, session)
	if err != nil {
		return recordingLifecycleResult{}, err
	}
	if !owned {
		if claimed.Status == "completed" || claimed.Status == "failed" {
			return replayRecordingOperation(claimed)
		}
		return recordingLifecycleResult{}, lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "recording start is already being driven", 1)
	}
	op = claimed
	// The runtime driver is application-owned, not an HTTP-request child. A
	// client disconnect must leave the pending operation/replay identity alive;
	// only fencing, application shutdown wiring, or a durable terminal runtime
	// failure may end the driver.
	driverCtx, stopDriverHeartbeat := s.keepStartRuntimeDriver(context.Background(), op)
	defer stopDriverHeartbeat()
	// A prior driver may already have created the runtime lease before its
	// database completion failed. Adopting that lease does not need to reload a
	// mutable auth asset; transaction 2 only needs the fenced operation/session.
	if locator, ok := s.runtime.(recordingRuntimeScopeLocator); ok && locator.HasActivePageRecordingScope(ctx, recordingSessionRuntimeScope(session)) {
		return s.completeStart(ctx, op, session, nil, startDriverToken(op), op.RuntimeDriverClaimGeneration)
	}
	var sourceAuth *models.ProjectAuthState
	if meta.AuthContext == authContextProjectSaved {
		auth, authErr := s.loadPinnedAuthStateForStart(session)
		if authErr != nil {
			if failErr := s.failStart(session.ID, op.ID, startDriverToken(op), op.RuntimeDriverClaimGeneration, http.StatusConflict, "runtime_lease_lost", "saved project auth state is unavailable"); failErr != nil {
				return recordingLifecycleResult{}, translateRecordingDBError(failErr)
			}
			return recordingLifecycleResult{}, lifecycleError(http.StatusConflict, "runtime_lease_lost", "saved project auth state is unavailable")
		}
		sourceAuth = auth
	}
	runtimeInput := recordingSessionRuntimeScope(session)
	runtimeInput["browser_instance_id"] = input.BrowserInstanceID
	runtimeInput["runtime_page_id"] = input.RuntimePageID
	runtimeInput["recording_kind"] = meta.RecordingKind
	runtimeInput["auth_context"] = meta.AuthContext
	runtimeInput["target_url"] = meta.TargetURL
	runtimeInput["use_global_cookie_store"] = false
	runtimeInput["runtime_driver_token"] = startDriverToken(op)
	runtimeInput["runtime_driver_claim_generation"] = op.RuntimeDriverClaimGeneration
	if sourceAuth != nil {
		runtimeInput["auth_state"] = projectAuthStateSummary(*sourceAuth)
		runtimeInput["auth_state_json"] = sourceAuth.StateJSON
	}
	result, err := s.runtime.StartPageRecording(driverCtx, runtimeInput)
	if err != nil {
		if errors.Is(err, errProjectRecordingStartInProgress) || errors.Is(err, context.Canceled) || driverCtx.Err() != nil {
			// A runtime reservation may still be owned by a slow predecessor.
			// A fenced/cancelled driver or a transient heartbeat/store failure
			// likewise preserves the current DB fence. The same operation can
			// resume it on retry; clearing or failing it would turn response loss
			// into a false terminal runtime_lease_lost result.
			return recordingLifecycleResult{}, lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "recording start target is already reserved", 1)
		}
		if failErr := s.failStart(session.ID, op.ID, startDriverToken(op), op.RuntimeDriverClaimGeneration, http.StatusConflict, "runtime_lease_lost", safeProjectRecordingStartError(err)); failErr != nil {
			return recordingLifecycleResult{}, translateRecordingDBError(failErr)
		}
		// failStart committed the operation/session terminal outcome. Return a
		// terminal 4xx so the frontend may settle this operation ID; 5xx is
		// reserved for store/driver failures that intentionally remain pending.
		return recordingLifecycleResult{}, lifecycleError(http.StatusConflict, "runtime_lease_lost", "start page recording failed")
	}
	return s.completeStart(ctx, op, session, result, startDriverToken(op), op.RuntimeDriverClaimGeneration)
}

func (s *RecordingLifecycleService) beginStart(tx *gorm.DB, input startRecordingLifecycleInput, scope, requestHash, effectKey string) (models.RecordingOperation, models.RecordingSession, bool, error) {
	var existing models.RecordingOperation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", input.OperationID).First(&existing).Error
	if err == nil {
		if existing.Action != recordingActionStart || existing.Scope != scope || existing.RequestPayloadHash != requestHash {
			return models.RecordingOperation{}, models.RecordingSession{}, false, lifecycleError(http.StatusConflict, "operation_id_payload_conflict", "operation_id was already used for another request")
		}
		var session models.RecordingSession
		if existing.RecordingSessionID != nil {
			_ = tx.First(&session, *existing.RecordingSessionID).Error
		}
		return existing, session, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.RecordingOperation{}, models.RecordingSession{}, false, err
	}
	var pending models.RecordingOperation
	if err := tx.Where("runtime_effect_key = ? AND status = ?", effectKey, "pending").First(&pending).Error; err == nil {
		return models.RecordingOperation{}, models.RecordingSession{}, false, lifecycleError(http.StatusConflict, "recording_operation_in_progress", "a runtime operation is already in progress")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.RecordingOperation{}, models.RecordingSession{}, false, err
	}
	var page models.TestPage
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND version_id = ?", input.PageID, input.VersionID).First(&page).Error; err != nil {
		return models.RecordingOperation{}, models.RecordingSession{}, false, err
	}
	if input.AuthContext == authContextProjectSaved && input.SourceAuthStateID == nil {
		var auth models.ProjectAuthState
		if err := tx.Where("project_id = ? AND version_id = ? AND status = ?", input.ProjectID, input.VersionID, "active").Order("captured_at desc, id desc").First(&auth).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				now := time.Now().UTC()
				failed := models.RecordingOperation{
					OperationID: input.OperationID, Action: recordingActionStart, Scope: scope, RequestPayloadHash: requestHash,
					RequestCanonicalizerVersion: requestCanonicalizerVersion, Status: "failed", HTTPStatus: http.StatusConflict,
					ErrorCode: "recording_auth_state_unavailable", SanitizedErrorDetail: "saved project auth state is unavailable",
					ProjectID: input.ProjectID, VersionID: input.VersionID, PageID: input.PageID,
					BrowserInstanceID: input.BrowserInstanceID, RuntimePageID: input.RuntimePageID,
					FailedAt: &now, CreatedAt: now, UpdatedAt: now,
				}
				if err := tx.Create(&failed).Error; err != nil {
					return models.RecordingOperation{}, models.RecordingSession{}, false, err
				}
				return failed, models.RecordingSession{}, false, nil
			}
			return models.RecordingOperation{}, models.RecordingSession{}, false, err
		}
		input.SourceAuthStateID = &auth.ID
	}
	now := time.Now().UTC()
	metaJSON, _ := json.Marshal(p45RecordingMeta{SchemaVersion: 1, RecordingKind: input.RecordingKind, AuthContext: input.AuthContext, AuthStateID: input.SourceAuthStateID, TargetURL: input.TargetURL})
	session := models.RecordingSession{
		ProjectID: input.ProjectID, VersionID: input.VersionID, PageID: input.PageID,
		RecordingKind: input.RecordingKind, AuthContext: input.AuthContext, TargetURL: input.TargetURL,
		Status: "starting", BrowserInstanceID: input.BrowserInstanceID, RuntimePageID: input.RuntimePageID,
		RuntimeGeneration: uuid.NewString(), LeaseGeneration: uuid.NewString(), LifecycleRevision: 1,
		BasePageFlowRevision: page.PageFlowRevision, DraftCompletenessVersion: recordingDraftCompletenessVersion,
		SourceAuthStateID: input.SourceAuthStateID, RecordingMetaJSON: string(metaJSON), StartedAt: now, CreatedBy: input.CreatedBy, CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&session).Error; err != nil {
		return models.RecordingOperation{}, models.RecordingSession{}, false, err
	}
	op := models.RecordingOperation{
		OperationID: input.OperationID, Action: recordingActionStart, Scope: scope, RequestPayloadHash: requestHash,
		RequestCanonicalizerVersion: requestCanonicalizerVersion, Status: "pending", RuntimeEffectKey: runtimeEffectKey(effectKey),
		RecordingSessionID: &session.ID, ProjectID: input.ProjectID, VersionID: input.VersionID, PageID: input.PageID,
		BrowserInstanceID: input.BrowserInstanceID, RuntimePageID: input.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := tx.Create(&op).Error; err != nil {
		return models.RecordingOperation{}, models.RecordingSession{}, false, err
	}
	return op, session, true, nil
}

func (s *RecordingLifecycleService) completeStart(ctx context.Context, op models.RecordingOperation, session models.RecordingSession, runtimeResult map[string]any, driverToken string, driverGeneration uint64) (recordingLifecycleResult, error) {
	var response map[string]any
	var terminal *recordingLifecycleError
	var replay *recordingLifecycleResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var currentOp models.RecordingOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&currentOp, op.ID).Error; err != nil {
			return err
		}
		if currentOp.Status == "completed" || currentOp.Status == "failed" {
			result, err := replayRecordingOperation(currentOp)
			if err != nil {
				return err
			}
			replay = &result
			return nil
		}
		if currentOp.Status != "pending" || startDriverToken(currentOp) != driverToken || currentOp.RuntimeDriverClaimGeneration != driverGeneration {
			terminal = lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "recording start driver was fenced", 1)
			return nil
		}
		var current models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, session.ID).Error; err != nil {
			return err
		}
		if current.Status == "cancelled" {
			terminal = lifecycleError(http.StatusConflict, "start_cancelled", "recording start was cancelled")
			return s.markOperationFailed(tx, currentOp.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		if current.Status == "recording" {
			response = startRecordingResponse(current)
			return s.completeOperation(tx, currentOp.ID, http.StatusOK, response)
		}
		if current.Status != "starting" {
			return lifecycleError(http.StatusConflict, "recording_action_not_allowed", "recording start cannot be completed")
		}
		now := time.Now().UTC()
		result := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", current.ID, "starting", current.LifecycleRevision).
			Updates(map[string]any{"status": "recording", "lifecycle_revision": current.LifecycleRevision + 1, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "recording start changed concurrently")
		}
		current.Status = "recording"
		current.LifecycleRevision++
		response = startRecordingResponse(current)
		if runtimeResult != nil {
			response["runtime"] = sanitizeRecordingObject(runtimeResult)
		}
		return s.completeOperation(tx, currentOp.ID, http.StatusOK, response)
	})
	if err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	if terminal != nil {
		// The failed operation was committed in the transaction above. A fenced
		// driver must not tear down a recorder that a newer generation has already
		// adopted; only a durable cancellation owns this release.
		if terminal.Code == "start_cancelled" {
			s.releaseRuntimeRecording(ctx, session)
		}
		return recordingLifecycleResult{}, terminal
	}
	if replay != nil {
		return *replay, nil
	}
	return recordingLifecycleResult{Status: http.StatusOK, Body: response}, nil
}

func lifecycleRetryError(status int, code, detail string, retryAfter int) *recordingLifecycleError {
	return &recordingLifecycleError{Status: status, Code: code, Detail: detail, RetryAfter: retryAfter}
}

func startDriverToken(op models.RecordingOperation) string {
	if op.RuntimeDriverToken == nil {
		return ""
	}
	return strings.TrimSpace(*op.RuntimeDriverToken)
}

// claimStartRuntimeDriver serializes the runtime half of Start independently
// from operation creation. The claim token fences a slow or superseded driver:
// transaction 2 checks the token again before it can adopt a lease.
func (s *RecordingLifecycleService) claimStartRuntimeDriver(_ context.Context, source models.RecordingOperation, session models.RecordingSession) (models.RecordingOperation, bool, error) {
	var claimed models.RecordingOperation
	owned := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&claimed, source.ID).Error; err != nil {
			return err
		}
		if claimed.Action != recordingActionStart || claimed.RecordingSessionID == nil || *claimed.RecordingSessionID != session.ID {
			return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "start operation does not match recording session")
		}
		if claimed.Status != "pending" {
			return nil
		}
		now := time.Now().UTC()
		if startDriverToken(claimed) != "" && claimed.RuntimeDriverLeaseExpiresAt != nil && now.Before(*claimed.RuntimeDriverLeaseExpiresAt) {
			// The DB claim identifies the one current fence, but does not itself
			// grant a browser side effect. Concurrent response-loss retries may
			// all observe this row; Manager.AcquireRecordingStartTarget serializes
			// them so exactly one receives the cancellable runtime context while
			// the rest return in-progress. Returning the current claim here is
			// necessary after a higher-generation takeover: otherwise no caller
			// would ever drive its newly fenced reservation.
			owned = true
			return nil
		}
		token := uuid.NewString()
		expires := now.Add(startDriverClaimTTL)
		generation := claimed.RuntimeDriverClaimGeneration + 1
		result := tx.Model(&models.RecordingOperation{}).
			Where("id = ? AND status = ?", claimed.ID, "pending").
			Updates(map[string]any{"runtime_driver_token": token, "runtime_driver_claim_generation": generation, "runtime_driver_claimed_at": now, "runtime_driver_lease_expires_at": expires, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return lifecycleError(http.StatusConflict, "recording_operation_in_progress", "recording start driver changed concurrently")
		}
		claimed.RuntimeDriverToken = &token
		claimed.RuntimeDriverClaimGeneration = generation
		claimed.RuntimeDriverClaimedAt = &now
		claimed.RuntimeDriverLeaseExpiresAt = &expires
		owned = true
		return nil
	})
	if err != nil {
		return models.RecordingOperation{}, false, translateRecordingDBError(err)
	}
	return claimed, owned, nil
}

// keepStartRuntimeDriver renews the database fence while the one permitted
// runtime driver is navigating/restoring auth. A renewal failure cancels that
// driver context, but deliberately leaves the pending operation and Manager
// reservation intact for a later fenced retry; it must never manufacture a
// terminal response from an uncommitted store failure.
func (s *RecordingLifecycleService) keepStartRuntimeDriver(parent context.Context, operation models.RecordingOperation) (context.Context, func()) {
	driverCtx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(startDriverClaimTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-driverCtx.Done():
				return
			case <-ticker.C:
				if err := s.renewStartRuntimeDriver(operation.ID, startDriverToken(operation), operation.RuntimeDriverClaimGeneration); err != nil {
					logger.Warn(parent, "Recording Start driver renewal failed for operation_id=%s: %v", operation.OperationID, err)
					cancel()
					return
				}
			}
		}
	}()
	return driverCtx, func() {
		close(done)
		cancel()
	}
}

func (s *RecordingLifecycleService) renewStartRuntimeDriver(operationID uint, token string, generation uint64) error {
	if operationID == 0 || strings.TrimSpace(token) == "" || generation == 0 {
		return lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "recording start driver identity is missing", 1)
	}
	now := time.Now().UTC()
	expires := now.Add(startDriverClaimTTL)
	result := s.db.Model(&models.RecordingOperation{}).
		Where("id = ? AND status = ? AND runtime_driver_token = ? AND runtime_driver_claim_generation = ?", operationID, "pending", token, generation).
		Updates(map[string]any{"runtime_driver_claimed_at": now, "runtime_driver_lease_expires_at": expires, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "recording start driver was fenced", 1)
	}
	return nil
}

func (s *RecordingLifecycleService) Sync(ctx context.Context, input syncRecordingLifecycleInput) (recordingLifecycleResult, error) {
	if err := validateOperationID(input.OperationID); err != nil {
		return recordingLifecycleResult{}, err
	}
	if input.SyncRevision == 0 {
		return recordingLifecycleResult{}, lifecycleError(http.StatusBadRequest, "sync_revision_stale", "sync_revision must be positive")
	}
	requestHash := canonicalRequestHash(map[string]any{"sync_revision": input.SyncRevision, "actions": json.RawMessage(input.Actions), "dom_snapshot": json.RawMessage(input.DOMSnapshot)})
	scope := recordingSessionScope(input.Session)
	var response map[string]any
	var terminal *recordingLifecycleError
	err := s.db.Transaction(func(tx *gorm.DB) error {
		op, replay, err := s.beginSynchronousOperation(tx, input.OperationID, recordingActionSync, scope, requestHash, input.Session)
		if err != nil {
			return err
		}
		if replay {
			result, err := replayRecordingOperation(op)
			response = result.Body
			return err
		}
		var session models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, input.Session.ID).Error; err != nil {
			return err
		}
		if session.Status != "recording" {
			return lifecycleError(http.StatusConflict, "recording_action_not_allowed", "sync is only allowed while recording")
		}
		if input.SyncRevision < session.SyncRevision {
			terminal = lifecycleError(http.StatusConflict, "sync_revision_stale", "sync revision is stale")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		if input.SyncRevision == session.SyncRevision {
			if session.SyncPayloadHash != requestHash {
				terminal = lifecycleError(http.StatusConflict, "sync_revision_payload_conflict", "sync revision has different payload")
				return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
			}
			response = recordingSessionSummary(session)
			return s.completeOperation(tx, op.ID, http.StatusOK, response)
		}
		normalized, err := s.normalizer.NormalizeSync(input.Actions, input.DOMSnapshot, session)
		if err != nil {
			terminal = lifecycleError(http.StatusUnprocessableEntity, lifecycleCodeOr(err, "recording_actions_invalid"), "sync payload is invalid")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		now := time.Now().UTC()
		updates := map[string]any{"actions_json": normalized.ActionsJSON, "action_count": normalized.ActionCount, "dom_snapshot": normalized.DOMSnapshot,
			"recording_meta_json": normalized.RecordingMetaJSON, "sync_revision": input.SyncRevision, "sync_payload_hash": requestHash, "draft_hash": normalized.DraftHash,
			"draft_completeness_version": recordingDraftCompletenessVersion, "last_synced_at": now, "lifecycle_revision": session.LifecycleRevision + 1, "updated_at": now}
		result := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", session.ID, "recording", session.LifecycleRevision).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "sync changed concurrently")
		}
		for key, value := range updates {
			_ = key
			_ = value
		}
		if err := tx.First(&session, session.ID).Error; err != nil {
			return err
		}
		response = recordingSessionSummary(session)
		return s.completeOperation(tx, op.ID, http.StatusOK, response)
	})
	if err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	if terminal != nil {
		return recordingLifecycleResult{}, terminal
	}
	return recordingLifecycleResult{Status: http.StatusOK, Body: response}, nil
}

func (s *RecordingLifecycleService) Stop(ctx context.Context, input stopRecordingLifecycleInput) (recordingLifecycleResult, error) {
	if err := validateOperationID(input.OperationID); err != nil {
		return recordingLifecycleResult{}, err
	}
	if s.runtime == nil {
		return recordingLifecycleResult{}, lifecycleError(http.StatusInternalServerError, "runtime_lease_lost", "runtime is not configured")
	}
	requestHash := canonicalRequestHash(map[string]any{"dom_snapshot": json.RawMessage(input.DOMSnapshot)})
	scope := recordingSessionScope(input.Session)
	if input.runtimeResult == nil {
		if recovered, handled, err := s.RecoverPendingOperationForRequest(ctx, pendingOperationExpectation{OperationID: input.OperationID, Action: recordingActionStop, Scope: scope, RequestPayloadHash: requestHash}); handled || err != nil {
			return recovered, err
		}
	}
	effectKey := "stop:" + strconv.FormatUint(uint64(input.Session.ID), 10)
	op, session, replay, err := s.beginRuntimeOperation(input.OperationID, recordingActionStop, scope, requestHash, effectKey, input.Session)
	if err != nil {
		return recordingLifecycleResult{}, err
	}
	if replay {
		return replayRecordingOperation(op)
	}
	if session.Status == "stopped" {
		return s.completeExistingOperation(op, recordingSessionSummary(session))
	}
	if session.Status != "recording" {
		return s.failOperationAndReturn(ctx, op, "recording_action_not_allowed", "stop is not allowed in current status", http.StatusConflict)
	}

	runtimeResult := input.runtimeResult
	for attempts := 0; attempts < 3; attempts++ {
		current, err := s.loadSession(session.ID)
		if err != nil {
			return recordingLifecycleResult{}, translateRecordingDBError(err)
		}
		if current.Status == "cancelled" {
			result, failErr := s.failOperationAndReturn(ctx, op, "recording_action_not_allowed", "recording was cancelled", http.StatusConflict)
			if failErr != nil && lifecycleCode(failErr) == "recording_action_not_allowed" {
				s.releaseRuntimeReceipts(ctx, current)
			}
			return result, failErr
		}
		if current.Status == "stopped" {
			return s.completeExistingOperation(op, recordingSessionSummary(current))
		}
		if current.Status != "recording" {
			return s.failOperationAndReturn(ctx, op, "recording_action_not_allowed", "stop is not allowed", http.StatusConflict)
		}
		if runtimeResult == nil {
			runtimeInput := recordingSessionRuntimeScope(current)
			runtimeInput["operation_id"] = input.OperationID
			result, stopErr := s.runtime.StopPageRecording(ctx, runtimeInput)
			if stopErr != nil {
				if errors.Is(stopErr, errProjectRecordingStopInProgress) {
					// This operation never obtained the Manager reservation or a
					// receipt claim. Release only its pending DB effect before
					// returning retryable in-progress, so an in-page driver that won
					// the race can still create the one durable Stop receipt.
					if releaseErr := s.releaseUnclaimedRuntimeOperation(op); releaseErr != nil {
						return recordingLifecycleResult{}, translateRecordingDBError(releaseErr)
					}
					return recordingLifecycleResult{}, lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "recording stop is already being driven", 1)
				}
				if IsRecoverableRecordingDraft(current) {
					runtimeResult = map[string]any{}
				} else {
					err := s.failRecordingSessionAndOperation(ctx, op, current, "runtime_lease_lost", "runtime stop receipt is unavailable", http.StatusConflict)
					if err != nil {
						return recordingLifecycleResult{}, translateRecordingDBError(err)
					}
					s.releaseRuntimeReceipts(ctx, current)
					return recordingLifecycleResult{}, lifecycleError(http.StatusConflict, "runtime_lease_lost", "runtime stop receipt is unavailable")
				}
			} else {
				runtimeResult = result
			}
		}
		current, err = s.persistFinalReceiptSync(ctx, current, runtimeResult, input)
		if err != nil {
			// The final receipt has already been claimed, but its pre-Stop Sync
			// raced a normal Sync/CAS update. Reload and retry the same pending
			// Stop so retry exhaustion preserves both the operation and receipt.
			if lifecycleCode(err) == "recording_lifecycle_conflict" {
				continue
			}
			if terminal, ok := finalReceiptSemanticFailure(err); ok {
				if closeErr := s.finalizeInvalidFinalReceipt(ctx, op, current, terminal); closeErr != nil {
					// The semantic verdict is terminal only after the failure
					// transaction commits. A CAS miss leaves the operation/receipt
					// pending and must follow the same bounded Stop retry path.
					if lifecycleCode(closeErr) == "recording_lifecycle_conflict" {
						continue
					}
					return recordingLifecycleResult{}, translateRecordingDBError(closeErr)
				}
				// The invalid final receipt has a committed terminal outcome. It
				// must not remain claimable by a later Stop/Capture retry.
				s.releaseRuntimeReceipts(ctx, current)
				return recordingLifecycleResult{}, terminal
			}
			return recordingLifecycleResult{}, err
		}
		result, err := s.commitStop(ctx, op, current, runtimeResult, input)
		if err == nil {
			ackInput := recordingSessionRuntimeScope(current)
			receiptID := strings.TrimSpace(input.FinalReceiptID)
			if receiptID == "" {
				receiptID = strings.TrimSpace(stringFromAny(runtimeResult["runtime_final_receipt_id"]))
			}
			if receiptID != "" {
				ackInput["runtime_final_receipt_id"] = receiptID
			}
			claimGeneration := input.FinalReceiptClaimGeneration
			if claimGeneration == 0 {
				claimGeneration = uint64(uintFromAny(runtimeResult["runtime_final_receipt_claim_generation"]))
			}
			ackInput["operation_id"] = input.OperationID
			ackInput["runtime_final_receipt_claim_generation"] = claimGeneration
			s.runtime.AcknowledgeStoppedPageRecording(ctx, ackInput)
			return result, nil
		}
		if lifecycleCode(err) == "recording_lifecycle_conflict" {
			continue
		}
		if lifecycleCode(err) != "" {
			// A lifecycle-coded Stop failure is returned only after the same
			// transaction persisted the operation/session terminal outcome.
			s.releaseRuntimeReceipts(ctx, current)
			return recordingLifecycleResult{}, err
		}
		// The transaction itself failed. The operation is still pending and its
		// claimed receipts must remain available for the same operation retry.
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	// The operation and runtime receipt are still pending at this point. Treat
	// retry exhaustion as a retryable ownership observation, never as a durable
	// lifecycle terminal; otherwise a client would discard its operation_id
	// while the pending stop effect continues to own the partial unique key.
	return recordingLifecycleResult{}, lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "stop is still converging", 1)
}

// driveStopRuntime establishes the durable pending Stop operation before it
// asks the runtime to stop. It deliberately does not write RecordingSession
// status: the caller must hand the frozen result to RecordingRecoveryCoordinator
// as a recording_stopped event, which is the only path that commits stopped.
//
// handled is true when an existing durable operation already provides the HTTP
// response, or when a business failure was durably recorded. In that case no
// new runtime event must be created.
func (s *RecordingLifecycleService) driveStopRuntime(ctx context.Context, input stopRecordingLifecycleInput) (result recordingLifecycleResult, handled bool, session models.RecordingSession, runtimeResult map[string]any, err error) {
	if err := validateOperationID(input.OperationID); err != nil {
		return recordingLifecycleResult{}, true, models.RecordingSession{}, nil, err
	}
	if s.runtime == nil {
		return recordingLifecycleResult{}, true, models.RecordingSession{}, nil, lifecycleError(http.StatusInternalServerError, "runtime_lease_lost", "runtime is not configured")
	}
	requestHash := canonicalRequestHash(map[string]any{"dom_snapshot": json.RawMessage(input.DOMSnapshot)})
	scope := recordingSessionScope(input.Session)
	if recovered, recoveredHandled, recoverErr := s.RecoverPendingOperationForRequest(ctx, pendingOperationExpectation{OperationID: input.OperationID, Action: recordingActionStop, Scope: scope, RequestPayloadHash: requestHash}); recoveredHandled || recoverErr != nil {
		return recovered, true, input.Session, nil, recoverErr
	}
	effectKey := "stop:" + strconv.FormatUint(uint64(input.Session.ID), 10)
	op, session, replay, err := s.beginRuntimeOperation(input.OperationID, recordingActionStop, scope, requestHash, effectKey, input.Session)
	if err != nil {
		return recordingLifecycleResult{}, true, session, nil, err
	}
	if replay {
		replayed, replayErr := replayRecordingOperation(op)
		return replayed, true, session, nil, replayErr
	}
	if session.Status == "stopped" {
		completed, completeErr := s.completeExistingOperation(op, recordingSessionSummary(session))
		return completed, true, session, nil, completeErr
	}
	if session.Status != "recording" {
		failed, failErr := s.failOperationAndReturn(ctx, op, "recording_action_not_allowed", "stop is not allowed in current status", http.StatusConflict)
		return failed, true, session, nil, failErr
	}

	current, loadErr := s.loadSession(session.ID)
	if loadErr != nil {
		return recordingLifecycleResult{}, true, session, nil, translateRecordingDBError(loadErr)
	}
	if current.Status == "cancelled" {
		failed, failErr := s.failOperationAndReturn(ctx, op, "recording_action_not_allowed", "recording was cancelled", http.StatusConflict)
		if failErr != nil && lifecycleCode(failErr) == "recording_action_not_allowed" {
			s.releaseRuntimeReceipts(ctx, current)
		}
		return failed, true, current, nil, failErr
	}
	if current.Status == "stopped" {
		completed, completeErr := s.completeExistingOperation(op, recordingSessionSummary(current))
		return completed, true, current, nil, completeErr
	}
	if current.Status != "recording" {
		failed, failErr := s.failOperationAndReturn(ctx, op, "recording_action_not_allowed", "stop is not allowed", http.StatusConflict)
		return failed, true, current, nil, failErr
	}

	runtimeInput := recordingSessionRuntimeScope(current)
	runtimeInput["operation_id"] = input.OperationID
	runtimeResult, err = s.runtime.StopPageRecording(ctx, runtimeInput)
	if err == nil {
		return recordingLifecycleResult{}, false, current, runtimeResult, nil
	}
	if errors.Is(err, errProjectRecordingStopInProgress) {
		// This operation never obtained the Manager reservation or a receipt
		// claim. Release only its pending DB effect so the winner remains the
		// sole Stop driver.
		if releaseErr := s.releaseUnclaimedRuntimeOperation(op); releaseErr != nil {
			return recordingLifecycleResult{}, true, current, nil, translateRecordingDBError(releaseErr)
		}
		return recordingLifecycleResult{}, true, current, nil, lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "recording stop is already being driven", 1)
	}
	if IsRecoverableRecordingDraft(current) {
		// Preserve the established fallback for a runtime result that was not
		// available after the pending effect committed. Coordinator will still
		// converge it through the recording_stopped path using the durable draft.
		return recordingLifecycleResult{}, false, current, map[string]any{}, nil
	}
	failErr := s.failRecordingSessionAndOperation(ctx, op, current, "runtime_lease_lost", "runtime stop receipt is unavailable", http.StatusConflict)
	if failErr != nil {
		return recordingLifecycleResult{}, true, current, nil, translateRecordingDBError(failErr)
	}
	s.releaseRuntimeReceipts(ctx, current)
	return recordingLifecycleResult{}, true, current, nil, lifecycleError(http.StatusConflict, "runtime_lease_lost", "runtime stop receipt is unavailable")
}

// persistFinalReceiptSync makes a runtime final receipt participate in the
// same monotonic Sync stream before Stop changes the lifecycle state. It is a
// no-op for an equal/stale receipt, which commitStop will subsequently treat
// as Stop evidence only. Coordinator uses the same Sync operation for events;
// this direct Stop path closes the equivalent HTTP/runtime adapter gap.
func (s *RecordingLifecycleService) persistFinalReceiptSync(ctx context.Context, session models.RecordingSession, runtimeResult map[string]any, input stopRecordingLifecycleInput) (models.RecordingSession, error) {
	revision := input.FinalReceiptRevision
	if revision == 0 {
		revision = uint64(uintFromAny(runtimeResult["runtime_final_sync_revision"]))
	}
	if revision == 0 || revision < session.SyncRevision {
		return session, nil
	}
	actionsJSON, dom, err := finalReceiptDraftPayload(runtimeResult, input)
	if err != nil {
		return session, err
	}
	receiptID := strings.TrimSpace(input.FinalReceiptID)
	if receiptID == "" {
		receiptID = strings.TrimSpace(stringFromAny(runtimeResult["runtime_final_receipt_id"]))
	}
	// Sync request hashes include transport-level JSON.  Final receipt identity
	// is semantic instead: Recorder deliberately ignores DOM captured_at.  Do
	// not send an equal revision back through Sync, whose raw hash would turn a
	// timestamp-only refresh into a false lifecycle conflict.
	if revision == session.SyncRevision {
		candidate := session
		candidate.ActionsJSON = string(actionsJSON)
		candidate.DOMSnapshot = string(dom)
		normalized, normalizeErr := s.normalizer.NormalizeFinal(candidate, nil)
		if normalizeErr != nil || normalized.DraftHash != session.DraftHash {
			return session, lifecycleError(http.StatusConflict, "sync_revision_payload_conflict", "final receipt has different semantic payload for the persisted sync revision")
		}
		return session, nil
	}
	opID := deterministicRuntimeOperationID("final-sync", session, receiptID, revision)
	_, syncErr := s.Sync(ctx, syncRecordingLifecycleInput{
		OperationID:  opID,
		Session:      session,
		SyncRevision: revision,
		Actions:      actionsJSON,
		DOMSnapshot:  dom,
	})
	if syncErr != nil {
		var current models.RecordingSession
		if loadErr := s.db.First(&current, session.ID).Error; loadErr == nil && current.SyncRevision > revision {
			return current, nil
		}
		return session, syncErr
	}
	var current models.RecordingSession
	if err := s.db.First(&current, session.ID).Error; err != nil {
		return session, err
	}
	return current, nil
}

// finalReceiptDraftPayload obtains the exact final draft represented by a
// runtime receipt. The result is shared by pre-Stop Sync and the transaction
// locked equal-revision check, so an equal revision cannot hide a different
// ActionTrace or semantic DOM.
func finalReceiptDraftPayload(runtimeResult map[string]any, input stopRecordingLifecycleInput) (json.RawMessage, json.RawMessage, error) {
	actions, ok := runtimeResult["actions"]
	if !ok {
		return nil, nil, lifecycleError(http.StatusUnprocessableEntity, "recording_actions_invalid", "runtime final receipt has no action trace")
	}
	actionsJSON, err := json.Marshal(actions)
	if err != nil {
		return nil, nil, lifecycleError(http.StatusUnprocessableEntity, "recording_actions_invalid", "runtime final receipt action trace is invalid")
	}
	dom := append(json.RawMessage(nil), input.DOMSnapshot...)
	if runtimeDOM, ok := runtimeResult["dom_snapshot"]; ok {
		encoded, marshalErr := json.Marshal(runtimeDOM)
		if marshalErr != nil {
			return nil, nil, lifecycleError(http.StatusUnprocessableEntity, "recording_source_invalid", "runtime final receipt DOM snapshot is invalid")
		}
		dom = encoded
	}
	return actionsJSON, dom, nil
}

func (s *RecordingLifecycleService) commitStop(ctx context.Context, op models.RecordingOperation, session models.RecordingSession, runtimeResult map[string]any, input stopRecordingLifecycleInput) (recordingLifecycleResult, error) {
	var response map[string]any
	var terminal *recordingLifecycleError
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, session.ID).Error; err != nil {
			return err
		}
		if current.Status == "cancelled" {
			terminal = lifecycleError(http.StatusConflict, "recording_action_not_allowed", "recording was cancelled")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		if current.Status == "stopped" {
			response = recordingSessionSummary(current)
			return s.completeOperation(tx, op.ID, http.StatusOK, response)
		}
		if current.Status != "recording" {
			terminal = lifecycleError(http.StatusConflict, "recording_action_not_allowed", "stop is not allowed")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		// Revision precedence is decided only after the session row is locked.
		// Coordinator may have observed an older revision before a concurrent
		// Sync committed; an equal or stale final receipt is Stop evidence only
		// and must never overwrite the newly persisted draft.
		receiptRevision := input.FinalReceiptRevision
		if receiptRevision == 0 {
			receiptRevision = uint64(uintFromAny(runtimeResult["runtime_final_sync_revision"]))
		}
		if receiptRevision != 0 && receiptRevision == current.SyncRevision {
			actionsJSON, dom, payloadErr := finalReceiptDraftPayload(runtimeResult, input)
			if payloadErr != nil {
				terminal = lifecycleError(http.StatusUnprocessableEntity, lifecycleCodeOr(payloadErr, "recording_source_invalid"), "final recording data is invalid")
				return s.failSessionAndOperationTx(tx, op.ID, current, terminal.Code, terminal.Detail, terminal.Status)
			}
			receiptCandidate := current
			receiptCandidate.ActionsJSON = string(actionsJSON)
			receiptCandidate.DOMSnapshot = string(dom)
			normalizedReceipt, normalizeErr := s.normalizer.NormalizeFinal(receiptCandidate, nil)
			if normalizeErr != nil || normalizedReceipt.DraftHash != current.DraftHash {
				terminal = lifecycleError(http.StatusConflict, "sync_revision_payload_conflict", "final receipt has different payload for the persisted sync revision")
				return s.failSessionAndOperationTx(tx, op.ID, current, terminal.Code, terminal.Detail, terminal.Status)
			}
		}
		useReceiptDraft := !input.PreservePersistedDraft && receiptRevision > current.SyncRevision
		candidate := current
		if useReceiptDraft {
			if actions, ok := runtimeResult["actions"]; ok {
				if data, err := json.Marshal(actions); err == nil && string(data) != "null" {
					candidate.ActionsJSON = string(data)
				}
			}
			if len(input.DOMSnapshot) > 0 && strings.TrimSpace(string(input.DOMSnapshot)) != "null" {
				candidate.DOMSnapshot = string(input.DOMSnapshot)
			} else if dom, ok := runtimeResult["dom_snapshot"]; ok {
				if data, err := json.Marshal(dom); err == nil {
					candidate.DOMSnapshot = string(data)
				}
			}
		}
		normalized, err := s.normalizer.NormalizeFinal(candidate, nil)
		if err != nil {
			terminal = lifecycleError(http.StatusUnprocessableEntity, lifecycleCodeOr(err, "recording_source_invalid"), "final recording data is invalid")
			return s.failSessionAndOperationTx(tx, op.ID, current, terminal.Code, terminal.Detail, terminal.Status)
		}
		now := time.Now().UTC()
		updates := map[string]any{"status": "stopped", "actions_json": normalized.ActionsJSON, "action_count": normalized.ActionCount, "dom_snapshot": normalized.DOMSnapshot, "recording_meta_json": normalized.RecordingMetaJSON, "draft_hash": normalized.DraftHash, "stopped_at": now, "last_synced_at": now, "lifecycle_revision": current.LifecycleRevision + 1, "updated_at": now}
		result := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", current.ID, "recording", current.LifecycleRevision).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "stop changed concurrently")
		}
		receiptID := strings.TrimSpace(input.FinalReceiptID)
		if receiptID == "" {
			receiptID = strings.TrimSpace(stringFromAny(runtimeResult["runtime_final_receipt_id"]))
		}
		artifacts := s.normalizer.NormalizeArtifacts(current, runtimeResult["artifacts"], receiptID)
		if artifacts.Dropped > 0 {
			logger.Warn(ctx, "Dropped %d unsafe runtime recording artifacts for session_id=%d", artifacts.Dropped, current.ID)
		}
		for _, artifact := range artifacts.Artifacts {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&artifact).Error; err != nil {
				return err
			}
		}
		if err := tx.First(&current, current.ID).Error; err != nil {
			return err
		}
		if receiptID != "" {
			claimGeneration := input.FinalReceiptClaimGeneration
			if claimGeneration == 0 {
				claimGeneration = uint64(uintFromAny(runtimeResult["runtime_final_receipt_claim_generation"]))
			}
			if err := tx.Model(&models.RecordingOperation{}).Where("id = ? AND status = ?", op.ID, "pending").Updates(map[string]any{"receipt_id": receiptID, "runtime_receipt_claim_generation": claimGeneration}).Error; err != nil {
				return err
			}
		}
		response = recordingSessionSummary(current)
		return s.completeOperation(tx, op.ID, http.StatusOK, response)
	})
	if err != nil {
		return recordingLifecycleResult{}, err
	}
	if terminal != nil {
		return recordingLifecycleResult{}, terminal
	}
	return recordingLifecycleResult{Status: http.StatusOK, Body: response}, nil
}

func (s *RecordingLifecycleService) Save(ctx context.Context, input saveRecordingLifecycleInput) (recordingLifecycleResult, error) {
	if err := validateOperationID(input.OperationID); err != nil {
		return recordingLifecycleResult{}, err
	}
	requestHash := canonicalRequestHash(map[string]any{"name": strings.TrimSpace(input.Name), "recording_meta": json.RawMessage(input.RecordingMeta)})
	scope := recordingSessionScope(input.Session)
	if replay, found, err := s.replayTerminalOperation(input.OperationID, recordingActionSave, scope, requestHash); found || err != nil {
		return replay, err
	}
	// Candidate construction may parse a large DOM/action trace. It is always
	// outside page/session locks; a stale candidate aborts its transaction and
	// is rebuilt from a new immutable draft identity.
	for attempt := 0; attempt < 3; attempt++ {
		session, err := s.loadSession(input.Session.ID)
		if err != nil {
			return recordingLifecycleResult{}, translateRecordingDBError(err)
		}
		var candidate normalizedRecording
		var candidateErr error
		candidateReady := session.Status == "stopped"
		if candidateReady {
			candidate, candidateErr = s.normalizer.NormalizeFinal(session, input.RecordingMeta)
		}
		candidateRevision := session.LifecycleRevision
		candidateDraftHash := session.DraftHash
		var response map[string]any
		var terminal *recordingLifecycleError
		err = s.db.Transaction(func(tx *gorm.DB) error {
			var current models.RecordingSession
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, session.ID).Error; err != nil {
				return err
			}
			// Stop may commit between the lock-free read above and this lock. A
			// zero-value candidate must never be allowed to publish a PageScript;
			// rebuild from the newly stopped draft outside the transaction instead.
			if current.Status == "stopped" && !candidateReady {
				return errSaveCandidateStale
			}
			if candidateReady && current.Status == "stopped" && (current.LifecycleRevision != candidateRevision || current.DraftHash != candidateDraftHash) {
				return errSaveCandidateStale
			}
			op, replay, err := s.beginSynchronousOperation(tx, input.OperationID, recordingActionSave, scope, requestHash, session)
			if err != nil {
				return err
			}
			if replay {
				result, err := replayRecordingOperation(op)
				response = result.Body
				return err
			}
			if current.Status == "saved" {
				var script models.PageScript
				if err := tx.Where("page_id = ? AND source_recording_session_id = ?", current.PageID, current.ID).First(&script).Error; err == nil {
					response = pageScriptResponse(script)
					return s.completeOperation(tx, op.ID, http.StatusOK, response)
				}
				terminal = lifecycleError(http.StatusConflict, "page_script_superseded", "page script was replaced by another recording")
				return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
			}
			if current.Status != "stopped" {
				terminal = lifecycleError(http.StatusConflict, "recording_action_not_allowed", "save is only allowed for stopped sessions")
				return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
			}
			if candidateErr != nil {
				terminal = lifecycleError(http.StatusUnprocessableEntity, lifecycleCodeOr(candidateErr, "recording_source_invalid"), "recording source is invalid")
				return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
			}
			var page models.TestPage
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", current.PageID).First(&page).Error; err != nil {
				return err
			}
			if page.PageFlowRevision != current.BasePageFlowRevision {
				terminal = lifecycleError(http.StatusConflict, "page_script_replaced_conflict", "page main flow changed after recording started")
				return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
			}
			name := strings.TrimSpace(input.Name)
			if name == "" {
				name = "Recorded main flow"
			}
			newScript := models.PageScript{PageID: current.PageID, SourceRecordingSessionID: &current.ID, PageScriptContentHash: candidate.PageScriptContentHash, NormalizerVersion: candidate.NormalizerVersion, Name: name, ActionTrace: candidate.ActionsJSON, DOMSnapshot: candidate.DOMSnapshot, RecordingMetaJSON: candidate.RecordingMetaJSON}
			if err := tx.Where("page_id = ?", current.PageID).Delete(&models.PageScript{}).Error; err != nil {
				return err
			}
			if err := tx.Create(&newScript).Error; err != nil {
				return err
			}
			now := time.Now().UTC()
			result := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", current.ID, "stopped", current.LifecycleRevision).Updates(map[string]any{"status": "saved", "recording_meta_json": candidate.RecordingMetaJSON, "saved_at": now, "lifecycle_revision": current.LifecycleRevision + 1, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "save changed concurrently")
			}
			if err := tx.Model(&models.TestPage{}).Where("id = ? AND page_flow_revision = ?", page.ID, page.PageFlowRevision).Updates(map[string]any{"page_flow_revision": page.PageFlowRevision + 1, "updated_at": now}).Error; err != nil {
				return err
			}
			response = pageScriptResponse(newScript)
			response["recording_source_hash"] = candidate.RecordingSourceHash
			return s.completeOperation(tx, op.ID, http.StatusOK, response)
		})
		if errors.Is(err, errSaveCandidateStale) {
			continue
		}
		if err != nil {
			return recordingLifecycleResult{}, translateRecordingDBError(err)
		}
		if terminal != nil {
			return recordingLifecycleResult{}, terminal
		}
		return recordingLifecycleResult{Status: http.StatusOK, Body: response}, nil
	}
	return recordingLifecycleResult{}, lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "recording draft changed while saving")
}

func (s *RecordingLifecycleService) Capture(ctx context.Context, input captureRecordingLifecycleInput) (recordingLifecycleResult, error) {
	if err := validateOperationID(input.OperationID); err != nil {
		return recordingLifecycleResult{}, err
	}
	requestHash := canonicalCaptureRequestHash(input)
	scope := recordingSessionScope(input.Session)
	if replay, found, err := s.replayTerminalOperation(input.OperationID, recordingActionCapture, scope, requestHash); found || err != nil {
		if err == nil {
			s.finalizeCaptureRuntimeReceipt(ctx, input.Session, input.OperationID)
		}
		return replay, err
	}
	if recovered, handled, err := s.RecoverPendingOperationForRequest(ctx, pendingOperationExpectation{OperationID: input.OperationID, Action: recordingActionCapture, Scope: scope, RequestPayloadHash: requestHash}); handled || err != nil {
		return recovered, err
	}
	effectKey := "capture:" + strconv.FormatUint(uint64(input.Session.ID), 10)
	op, session, replay, err := s.beginRuntimeOperation(input.OperationID, recordingActionCapture, scope, requestHash, effectKey, input.Session)
	if err != nil {
		return recordingLifecycleResult{}, err
	}
	if replay {
		return replayRecordingOperation(op)
	}
	// Eligibility is a business outcome, so it must be attached to the pending
	// operation before it is returned.  Otherwise a lost 409 response could be
	// retried after a later session transition and unexpectedly become Capture.
	if session.RecordingKind != recordingKindLoginFlow || session.AuthContext != authContextClean {
		return s.failOperationAndReturn(ctx, op, "recording_session_auth_capture_not_allowed", "recording session cannot capture project auth state", http.StatusConflict)
	}
	if session.Status != "stopped" && session.Status != "saved" {
		return s.failOperationAndReturn(ctx, op, "recording_session_auth_capture_not_ready", "recording session is not ready to capture project auth state", http.StatusConflict)
	}
	// Infrastructure loss is deliberately not a durable Capture result: keep
	// this pending effect so the caller can reuse the same operation ID after
	// runtime/cipher recovery.  Business eligibility above has already been
	// durably settled before this check.
	if s.runtime == nil {
		return recordingLifecycleResult{}, lifecycleError(http.StatusInternalServerError, "runtime_lease_lost", "runtime is not configured")
	}
	if s.cipher == nil {
		return recordingLifecycleResult{}, lifecycleError(http.StatusInternalServerError, "auth_state_encryption_unavailable", "project auth state encryption is not configured")
	}
	if existing, ok, err := s.findCapturedAuthState(session.ID); err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	} else if ok {
		result, replayErr := s.completePendingCapturedAuthReplay(op, session, existing)
		if replayErr == nil && s.runtime != nil {
			replayScope := recordingSessionRuntimeScope(session)
			replayScope["operation_id"] = input.OperationID
			replayScope["captured_page_id"] = session.PageID
			s.runtime.AcknowledgeProjectAuthStateCapture(ctx, replayScope)
		}
		return result, replayErr
	}
	captureInput := recordingSessionRuntimeScope(session)
	captureInput["operation_id"] = input.OperationID
	captureInput["captured_page_id"] = session.PageID
	captureInput["captured_url"] = input.CapturedURL
	captureInput["origin_allowlist"] = input.OriginAllowlist
	version, err := s.loadVersion(input.ProjectID, input.VersionID)
	if err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	captureInput["base_url"] = version.BaseURL
	state, err := s.runtime.CaptureProjectAuthState(ctx, captureInput)
	if err != nil {
		code := "auth_snapshot_unavailable"
		if !errors.Is(err, errRecordingSessionStorageSnapshotUnavailable) {
			code = "runtime_receipt_unavailable"
		}
		return s.failOperationAndReturn(ctx, op, code, "frozen auth snapshot is unavailable", http.StatusConflict)
	}
	snapshotReceiptID := strings.TrimSpace(stringFromAny(state["runtime_snapshot_receipt_id"]))
	snapshotClaimGeneration := uint64(uintFromAny(state["runtime_snapshot_claim_generation"]))
	if snapshotReceiptID == "" || snapshotClaimGeneration == 0 {
		return s.failCaptureOperationAndFinalize(ctx, op, session, "", 0, "runtime_receipt_unavailable", "frozen auth snapshot receipt identity is unavailable", http.StatusConflict)
	}
	captureInput["runtime_snapshot_receipt_id"] = snapshotReceiptID
	captureInput["runtime_snapshot_claim_generation"] = snapshotClaimGeneration
	state = filterProjectAuthStorageState(state, version.BaseURL, input.OriginAllowlist, input.CapturedURL)
	req := captureProjectAuthStateRequest{Name: input.Name, CapturedPageID: session.PageID, CapturedURL: input.CapturedURL, OriginAllowlist: input.OriginAllowlist, RecordingSessionID: strconv.FormatUint(uint64(session.ID), 10)}
	row, err := buildProjectAuthStateRow(input.ProjectID, input.VersionID, req, state)
	if err != nil {
		return s.failCaptureOperationAndFinalize(ctx, op, session, snapshotReceiptID, snapshotClaimGeneration, "recording_capture_not_allowed", "captured auth state is invalid", http.StatusUnprocessableEntity)
	}
	sourceSessionID := session.ID
	row.SourceRecordingSessionID = &sourceSessionID
	row.SourceSnapshotReceiptID = snapshotReceiptID
	if err := s.runtime.SaveProjectAuthState(ctx, map[string]any{"project_id": input.ProjectID, "version_id": input.VersionID, "state_digest": row.StateDigest}); err != nil {
		return recordingLifecycleResult{}, lifecycleError(http.StatusInternalServerError, "runtime_receipt_unavailable", "save project auth state failed")
	}
	var response map[string]any
	var terminal *recordingLifecycleError
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.bindRuntimeReceiptToOperationTx(tx, op.ID, snapshotReceiptID, snapshotClaimGeneration); err != nil {
			return err
		}
		// Every Capture for this project/version obtains the same durable scope
		// lock before examining or replacing its active auth state. Session
		// locking alone cannot serialize two independent stopped sessions.
		var lockedVersion models.ProjectVersion
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND project_id = ?", input.VersionID, input.ProjectID).First(&lockedVersion).Error; err != nil {
			return err
		}
		var current models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, session.ID).Error; err != nil {
			return err
		}
		if current.Status != "stopped" && current.Status != "saved" {
			terminal = lifecycleError(http.StatusConflict, "recording_capture_not_allowed", "recording was cancelled")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		var existing models.ProjectAuthState
		if err := tx.Where("source_snapshot_receipt_id = ?", row.SourceSnapshotReceiptID).First(&existing).Error; err == nil {
			response = map[string]any{"auth_state": projectAuthStateSummary(existing)}
			return s.completeOperation(tx, op.ID, http.StatusOK, response)
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if input.Replace != nil && !*input.Replace {
			var active models.ProjectAuthState
			if err := tx.Where("project_id = ? AND version_id = ? AND status = ?", row.ProjectID, row.VersionID, "active").First(&active).Error; err == nil {
				terminal = lifecycleError(http.StatusBadRequest, "project_auth_state_exists", "project auth state already exists")
				return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if err := tx.Where("project_id = ? AND version_id = ? AND status = ?", row.ProjectID, row.VersionID, "active").Delete(&models.ProjectAuthState{}).Error; err != nil {
			return err
		}
		plaintext := row.StateJSON
		row.StateJSON = ""
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		ciphertext, nonce, err := s.cipher.encrypt(row.ID, row.ProjectID, plaintext)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.ProjectAuthState{}).Where("id = ?", row.ID).Updates(map[string]any{"state_ciphertext": ciphertext, "state_nonce": nonce, "encryption_version": projectAuthStateEncryptionVersion, "encryption_key_id": s.cipher.keyID, "state_json": ""}).Error; err != nil {
			return err
		}
		row.StateCiphertext, row.StateNonce, row.EncryptionVersion, row.EncryptionKeyID = ciphertext, nonce, projectAuthStateEncryptionVersion, s.cipher.keyID
		response = map[string]any{"auth_state": projectAuthStateSummary(row)}
		return s.completeOperation(tx, op.ID, http.StatusOK, response)
	})
	if err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	if terminal != nil {
		// This terminal response was durably written with the operation. Release
		// only this claim; a later owner must remain untouched.
		s.finalizeCaptureRuntimeReceipt(ctx, session, input.OperationID)
		return recordingLifecycleResult{}, terminal
	}
	s.finalizeCaptureRuntimeReceipt(ctx, session, input.OperationID)
	return recordingLifecycleResult{Status: http.StatusOK, Body: response}, nil
}

func (s *RecordingLifecycleService) Cancel(ctx context.Context, input cancelRecordingLifecycleInput) (recordingLifecycleResult, error) {
	if err := validateOperationID(input.OperationID); err != nil {
		return recordingLifecycleResult{}, err
	}
	requestHash := canonicalRequestHash(map[string]any{})
	scope := recordingSessionScope(input.Session)
	var response map[string]any
	var terminal *recordingLifecycleError
	cleanupRuntime := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		op, replay, err := s.beginSynchronousOperation(tx, input.OperationID, recordingActionCancel, scope, requestHash, input.Session)
		if err != nil {
			return err
		}
		if replay {
			result, err := replayRecordingOperation(op)
			response = result.Body
			return err
		}
		var session models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, input.Session.ID).Error; err != nil {
			return err
		}
		if session.Status == "saved" {
			terminal = lifecycleError(http.StatusConflict, "recording_action_not_allowed", "saved recording cannot be cancelled")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		if session.Status == "cancelled" {
			response = recordingSessionSummary(session)
			return s.completeOperation(tx, op.ID, http.StatusOK, response)
		}
		if session.Status != "starting" && session.Status != "recording" && session.Status != "stopped" && session.Status != "failed" {
			terminal = lifecycleError(http.StatusConflict, "recording_action_not_allowed", "recording cannot be cancelled")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		now := time.Now().UTC()
		result := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", session.ID, session.Status, session.LifecycleRevision).Updates(map[string]any{"status": "cancelled", "lifecycle_revision": session.LifecycleRevision + 1, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "cancel changed concurrently")
		}
		if session.Status == "starting" {
			if err := tx.Model(&models.RecordingOperation{}).Where("recording_session_id = ? AND action = ? AND status = ?", session.ID, recordingActionStart, "pending").Updates(map[string]any{"status": "failed", "error_code": "start_cancelled", "sanitized_error_detail": "start was cancelled", "http_status": http.StatusConflict, "failed_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		session.Status = "cancelled"
		session.LifecycleRevision++
		cleanupRuntime = true
		response = recordingSessionSummary(session)
		return s.completeOperation(tx, op.ID, http.StatusOK, response)
	})
	if err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	if terminal != nil {
		return recordingLifecycleResult{}, terminal
	}
	// Only the Cancel that won the session transition owns runtime cleanup.
	// A later business-idempotent Cancel must never invoke Stop again.
	if cleanupRuntime {
		s.releaseRuntimeRecording(ctx, input.Session)
	}
	return recordingLifecycleResult{Status: http.StatusOK, Body: response}, nil
}

func (s *RecordingLifecycleService) beginRuntimeOperation(operationID, action, scope, requestHash, effectKey string, source models.RecordingSession) (models.RecordingOperation, models.RecordingSession, bool, error) {
	var op models.RecordingOperation
	var session models.RecordingSession
	replay := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing models.RecordingOperation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", operationID).First(&existing).Error
		if err == nil {
			if existing.Action != action || existing.Scope != scope || existing.RequestPayloadHash != requestHash {
				return lifecycleError(http.StatusConflict, "operation_id_payload_conflict", "operation_id was already used for another request")
			}
			op = existing
			replay = existing.Status != "pending"
			if existing.RecordingSessionID != nil {
				return tx.First(&session, *existing.RecordingSessionID).Error
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var pending models.RecordingOperation
		if err := tx.Where("runtime_effect_key = ? AND status = ?", effectKey, "pending").First(&pending).Error; err == nil {
			return lifecycleError(http.StatusConflict, "recording_operation_in_progress", "a runtime operation is already in progress")
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, source.ID).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		sessionID := session.ID
		op = models.RecordingOperation{OperationID: operationID, Action: action, Scope: scope, RequestPayloadHash: requestHash, RequestCanonicalizerVersion: requestCanonicalizerVersion, Status: "pending", RuntimeEffectKey: runtimeEffectKey(effectKey), RecordingSessionID: &sessionID, ProjectID: session.ProjectID, VersionID: session.VersionID, PageID: session.PageID, BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration, ReceiptID: effectKey, CreatedAt: now, UpdatedAt: now}
		return tx.Create(&op).Error
	})
	if err != nil && isPendingRuntimeEffectConstraint(err) {
		// A concurrent request may pass the preflight lookup before the first
		// transaction commits.  The partial unique index is the final arbiter;
		// expose that expected race as retryable in-progress, never a 500 or a
		// durable failed operation for the losing id.
		return models.RecordingOperation{}, models.RecordingSession{}, false, lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "a runtime operation is already in progress", 1)
	}
	return op, session, replay, err
}

func (s *RecordingLifecycleService) beginSynchronousOperation(tx *gorm.DB, operationID, action, scope, requestHash string, session models.RecordingSession) (models.RecordingOperation, bool, error) {
	var existing models.RecordingOperation
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", operationID).First(&existing).Error
	if err == nil {
		if existing.Action != action || existing.Scope != scope || existing.RequestPayloadHash != requestHash {
			return models.RecordingOperation{}, false, lifecycleError(http.StatusConflict, "operation_id_payload_conflict", "operation_id was already used for another request")
		}
		return existing, true, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.RecordingOperation{}, false, err
	}
	now := time.Now().UTC()
	sessionID := session.ID
	op := models.RecordingOperation{OperationID: operationID, Action: action, Scope: scope, RequestPayloadHash: requestHash, RequestCanonicalizerVersion: requestCanonicalizerVersion, Status: "pending", RecordingSessionID: &sessionID, ProjectID: session.ProjectID, VersionID: session.VersionID, PageID: session.PageID, BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration, CreatedAt: now, UpdatedAt: now}
	if err := tx.Create(&op).Error; err != nil {
		return models.RecordingOperation{}, false, err
	}
	return op, false, nil
}

// releaseUnclaimedRuntimeOperation removes only a pending operation that lost
// the Manager reservation before it could claim any receipt. Retrying its
// operation_id can then observe the winning Stop's durable result instead of
// permanently blocking the runtime-effect key with a loser.
func (s *RecordingLifecycleService) releaseUnclaimedRuntimeOperation(op models.RecordingOperation) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("id = ? AND status = ?", op.ID, "pending").Delete(&models.RecordingOperation{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "recording operation changed concurrently")
		}
		return nil
	})
}

func (s *RecordingLifecycleService) completeOperation(tx *gorm.DB, id uint, status int, response map[string]any) error {
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	result := tx.Model(&models.RecordingOperation{}).Where("id = ? AND status = ?", id, "pending").Updates(map[string]any{"status": "completed", "sanitized_response_json": string(data), "http_status": status, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "operation changed concurrently")
	}
	return nil
}

func (s *RecordingLifecycleService) markOperationFailed(tx *gorm.DB, id uint, status int, code, detail string) error {
	now := time.Now().UTC()
	result := tx.Model(&models.RecordingOperation{}).Where("id = ? AND status = ?", id, "pending").Updates(map[string]any{"status": "failed", "http_status": status, "error_code": code, "sanitized_error_detail": detail, "failed_at": now, "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "operation changed concurrently")
	}
	return nil
}

// failSessionAndOperationTx closes a final-recorder failure atomically. It is
// intentionally used only for failures that prove the runtime result cannot
// form a trustworthy PageScript; transient database errors leave both facts
// pending so the same receipt can be retried.
func (s *RecordingLifecycleService) failSessionAndOperationTx(tx *gorm.DB, operationID uint, session models.RecordingSession, code, detail string, status int) error {
	if session.Status == "recording" || session.Status == "starting" {
		now := time.Now().UTC()
		result := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", session.ID, session.Status, session.LifecycleRevision).Updates(map[string]any{
			"status":                   "failed",
			"failure_code":             code,
			"failure_detail_sanitized": detail,
			"error_message":            detail,
			"failed_at":                now,
			"updated_at":               now,
			"lifecycle_revision":       session.LifecycleRevision + 1,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "recording changed concurrently")
		}
	}
	return s.markOperationFailed(tx, operationID, status, code, detail)
}

func (s *RecordingLifecycleService) failRecordingSessionAndOperation(ctx context.Context, op models.RecordingOperation, source models.RecordingSession, code, detail string, status int) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var current models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, source.ID).Error; err != nil {
			return err
		}
		return s.failSessionAndOperationTx(tx, op.ID, current, code, detail, status)
	})
}

func (s *RecordingLifecycleService) failOperation(ctx context.Context, id uint, code, detail string) error {
	return s.db.Transaction(func(tx *gorm.DB) error { return s.markOperationFailed(tx, id, http.StatusConflict, code, detail) })
}

func (s *RecordingLifecycleService) failOperationAndReturn(ctx context.Context, op models.RecordingOperation, code, detail string, status int) (recordingLifecycleResult, error) {
	if err := s.db.Transaction(func(tx *gorm.DB) error { return s.markOperationFailed(tx, op.ID, status, code, detail) }); err != nil {
		// A terminal response is only truthful after its receipt commits. Leave
		// the operation pending when the store is unavailable so retry recovery
		// can converge on the same runtime receipt.
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	return recordingLifecycleResult{}, lifecycleError(status, code, detail)
}

// bindRuntimeReceiptToOperationTx persists the only identity allowed to
// consume a runtime receipt. It is deliberately part of the same transaction
// that completes/fails Capture or Stop, so a replay after commit can ACK the
// exact prior claim without sampling mutable Manager state.
func (s *RecordingLifecycleService) bindRuntimeReceiptToOperationTx(tx *gorm.DB, operationID uint, receiptID string, claimGeneration uint64) error {
	if strings.TrimSpace(receiptID) == "" || claimGeneration == 0 {
		return lifecycleError(http.StatusConflict, "runtime_receipt_unavailable", "runtime receipt claim identity is unavailable")
	}
	result := tx.Model(&models.RecordingOperation{}).Where("id = ? AND status = ?", operationID, "pending").Updates(map[string]any{
		"receipt_id":                       strings.TrimSpace(receiptID),
		"runtime_receipt_claim_generation": claimGeneration,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "recording operation changed concurrently")
	}
	return nil
}

func (s *RecordingLifecycleService) failCaptureOperationAndFinalize(ctx context.Context, op models.RecordingOperation, session models.RecordingSession, receiptID string, claimGeneration uint64, code, detail string, status int) (recordingLifecycleResult, error) {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if strings.TrimSpace(receiptID) != "" && claimGeneration != 0 {
			if err := s.bindRuntimeReceiptToOperationTx(tx, op.ID, receiptID, claimGeneration); err != nil {
				return err
			}
		}
		return s.markOperationFailed(tx, op.ID, status, code, detail)
	})
	if err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	s.finalizeCaptureRuntimeReceipt(ctx, session, op.OperationID)
	return recordingLifecycleResult{}, lifecycleError(status, code, detail)
}

// finalizeCaptureRuntimeReceipt is the receipt-finalize boundary for Capture.
// It runs strictly after a durable operation outcome and forwards the complete
// scope plus operation/generation to Manager. Completed operations ACK;
// durable failures Release only their own temporary claim.
func (s *RecordingLifecycleService) finalizeCaptureRuntimeReceipt(ctx context.Context, session models.RecordingSession, operationID string) {
	if s.runtime == nil || strings.TrimSpace(operationID) == "" {
		return
	}
	var op models.RecordingOperation
	if err := s.db.Where("operation_id = ? AND action = ?", operationID, recordingActionCapture).First(&op).Error; err != nil || (op.Status != "completed" && op.Status != "failed") || strings.TrimSpace(op.ReceiptID) == "" || op.RuntimeReceiptClaimGeneration == 0 {
		return
	}
	input := recordingSessionRuntimeScope(session)
	input["operation_id"] = op.OperationID
	input["runtime_snapshot_receipt_id"] = op.ReceiptID
	input["runtime_snapshot_claim_generation"] = op.RuntimeReceiptClaimGeneration
	if op.Status == "completed" {
		s.runtime.AcknowledgeProjectAuthStateCapture(ctx, input)
		return
	}
	s.runtime.DiscardProjectAuthStateCapture(ctx, input)
}

func (s *RecordingLifecycleService) completeExistingOperation(op models.RecordingOperation, response map[string]any) (recordingLifecycleResult, error) {
	if err := s.db.Transaction(func(tx *gorm.DB) error { return s.completeOperation(tx, op.ID, http.StatusOK, response) }); err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	return recordingLifecycleResult{Status: http.StatusOK, Body: response}, nil
}

func (s *RecordingLifecycleService) failStart(sessionID, opID uint, driverToken string, driverGeneration uint64, status int, code, detail string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var op models.RecordingOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&op, opID).Error; err != nil {
			return err
		}
		if op.Status != "pending" || startDriverToken(op) != driverToken || op.RuntimeDriverClaimGeneration != driverGeneration {
			return lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "recording start driver was fenced", 1)
		}
		now := time.Now().UTC()
		if err := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ?", sessionID, "starting").Updates(map[string]any{"status": "failed", "failure_code": code, "failure_detail_sanitized": detail, "failed_at": now, "error_message": detail, "updated_at": now, "lifecycle_revision": gorm.Expr("lifecycle_revision + 1")}).Error; err != nil {
			return err
		}
		return s.markOperationFailed(tx, opID, status, code, detail)
	})
}

func (s *RecordingLifecycleService) loadSession(id uint) (models.RecordingSession, error) {
	var row models.RecordingSession
	return row, s.db.First(&row, id).Error
}

type pendingOperationExpectation struct {
	OperationID        string
	Action             string
	Scope              string
	RequestPayloadHash string
}

// RecoverPendingOperationForRequest validates the request's complete idempotent
// identity before it can resume a pending runtime operation. In particular, an
// operation UUID cannot be reused for a different action/scope after restart.
func (s *RecordingLifecycleService) RecoverPendingOperationForRequest(ctx context.Context, expected pendingOperationExpectation) (recordingLifecycleResult, bool, error) {
	return s.recoverPendingOperation(ctx, expected.OperationID, &expected)
}

// RecoverPendingOperation is retained for internal callers that already own an
// operation row (the startup coordinator). HTTP requests must use
// RecoverPendingOperationForRequest instead.
func (s *RecordingLifecycleService) RecoverPendingOperation(ctx context.Context, operationID string) (recordingLifecycleResult, bool, error) {
	return s.recoverPendingOperation(ctx, operationID, nil)
}

func (s *RecordingLifecycleService) recoverPendingOperation(ctx context.Context, operationID string, expected *pendingOperationExpectation) (recordingLifecycleResult, bool, error) {
	var response map[string]any
	var terminal *recordingLifecycleError
	handled := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var op models.RecordingOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", operationID).First(&op).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if expected != nil && (op.Action != expected.Action || op.Scope != expected.Scope || op.RequestPayloadHash != expected.RequestPayloadHash) {
			return lifecycleError(http.StatusConflict, "operation_id_payload_conflict", "operation_id was already used for another request")
		}
		if op.Status != "pending" || (op.Action != recordingActionStart && op.Action != recordingActionStop && op.Action != recordingActionCapture) {
			return nil
		}
		handled = true
		if op.RecordingSessionID == nil {
			terminal = lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "pending operation has no recording session")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		var session models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, *op.RecordingSessionID).Error; err != nil {
			return err
		}
		switch op.Action {
		case recordingActionStart:
			if session.Status == "starting" {
				now := time.Now().UTC()
				result := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", session.ID, "starting", session.LifecycleRevision).Updates(map[string]any{"status": "failed", "failure_code": "runtime_lease_lost", "failure_detail_sanitized": "runtime lease was lost before start completed", "failed_at": now, "updated_at": now, "lifecycle_revision": session.LifecycleRevision + 1})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "starting recording changed concurrently")
				}
			}
			terminal = lifecycleError(http.StatusConflict, "runtime_lease_lost", "runtime lease was lost before start completed")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		case recordingActionStop:
			if locator, ok := s.runtime.(recordingRuntimeScopeLocator); ok {
				runtimeScope := recordingSessionRuntimeScope(session)
				if locator.HasActivePageRecordingScope(ctx, runtimeScope) || locator.HasPendingStoppedPageRecordingScope(ctx, runtimeScope) {
					handled = false
					return nil
				}
			}
			if session.Status == "stopped" {
				response = recordingSessionSummary(session)
				return s.completeOperation(tx, op.ID, http.StatusOK, response)
			}
			if session.Status == "cancelled" {
				terminal = lifecycleError(http.StatusConflict, "recording_action_not_allowed", "recording was cancelled before Stop committed")
				return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
			}
			if session.Status != "recording" || !IsRecoverableRecordingDraft(session) {
				code := "runtime_lease_lost"
				if session.Status == "recording" {
					code = "recording_draft_incomplete"
				}
				terminal = lifecycleError(http.StatusConflict, code, "runtime stop receipt is unavailable")
				now := time.Now().UTC()
				if session.Status == "recording" {
					if err := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", session.ID, "recording", session.LifecycleRevision).Updates(map[string]any{"status": "failed", "failure_code": code, "failure_detail_sanitized": terminal.Detail, "failed_at": now, "lifecycle_revision": session.LifecycleRevision + 1, "updated_at": now}).Error; err != nil {
						return err
					}
				}
				return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
			}
			normalized, err := s.normalizer.NormalizeFinal(session, nil)
			if err != nil {
				terminal = lifecycleError(http.StatusUnprocessableEntity, "recording_draft_incomplete", "recording draft cannot be recovered")
				return s.failSessionAndOperationTx(tx, op.ID, session, terminal.Code, terminal.Detail, terminal.Status)
			}
			now := time.Now().UTC()
			result := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", session.ID, "recording", session.LifecycleRevision).Updates(map[string]any{"status": "stopped", "actions_json": normalized.ActionsJSON, "action_count": normalized.ActionCount, "dom_snapshot": normalized.DOMSnapshot, "recording_meta_json": normalized.RecordingMetaJSON, "draft_hash": normalized.DraftHash, "stopped_at": now, "lifecycle_revision": session.LifecycleRevision + 1, "updated_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "recording changed concurrently")
			}
			if err := tx.First(&session, session.ID).Error; err != nil {
				return err
			}
			response = recordingSessionSummary(session)
			return s.completeOperation(tx, op.ID, http.StatusOK, response)
		case recordingActionCapture:
			if s.runtime != nil && s.runtime.HasProjectAuthStateCapture(ctx, recordingSessionRuntimeScope(session)) {
				handled = false
				return nil
			}
			terminal = lifecycleError(http.StatusConflict, "auth_snapshot_unavailable", "frozen auth snapshot is unavailable")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		return nil
	})
	if err != nil {
		return recordingLifecycleResult{}, handled, translateRecordingDBError(err)
	}
	if terminal != nil {
		return recordingLifecycleResult{}, true, terminal
	}
	if handled {
		return recordingLifecycleResult{Status: http.StatusOK, Body: response}, true, nil
	}
	return recordingLifecycleResult{}, false, nil
}

func (s *RecordingLifecycleService) RecoverPendingOperations(ctx context.Context) error {
	var operations []models.RecordingOperation
	if err := s.db.Where("status = ? AND action IN ?", "pending", []string{recordingActionStart, recordingActionStop, recordingActionCapture}).Find(&operations).Error; err != nil {
		return err
	}
	var firstErr error
	for _, operation := range operations {
		if _, _, err := s.RecoverPendingOperation(ctx, operation.OperationID); err != nil {
			// A completed/failed receipt is already a converged business outcome.
			// Only leave an error for startup when the operation is still pending
			// (typically store/lock infrastructure failed before its transaction).
			var current models.RecordingOperation
			if reloadErr := s.db.Where("operation_id = ?", operation.OperationID).First(&current).Error; reloadErr != nil {
				if firstErr == nil {
					firstErr = reloadErr
				}
				continue
			}
			if current.Status == "pending" && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// RecoverExpiredRecordingReceipt closes a final receipt that reached the
// Manager TTL before it could be committed. It never treats expiration as a
// successful runtime acknowledgement: a structurally complete draft becomes
// stopped; otherwise the session is failed so it cannot block new recording.
func (s *RecordingLifecycleService) RecoverExpiredRecordingReceipt(ctx context.Context, session models.RecordingSession, receiptID string, finalRevision uint64) error {
	operationID := deterministicRuntimeOperationID("stop", session, receiptID, 0)
	if _, handled, err := s.RecoverPendingOperation(ctx, operationID); handled {
		return err
	}
	scope := recordingSessionScope(session)
	requestHash := canonicalRequestHash(map[string]any{"dom_snapshot": nil})
	var terminal *recordingLifecycleError
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, session.ID).Error; err != nil {
			return err
		}
		op, replay, err := s.beginSynchronousOperation(tx, operationID, recordingActionStop, scope, requestHash, current)
		if err != nil {
			return err
		}
		if replay {
			_, err := replayRecordingOperation(op)
			return err
		}
		if current.Status == "stopped" {
			return s.completeOperation(tx, op.ID, http.StatusOK, recordingSessionSummary(current))
		}
		if finalRevision > current.SyncRevision {
			terminal = lifecycleError(http.StatusConflict, "runtime_lease_lost", "final recording revision expired before it was persisted")
			return s.failSessionAndOperationTx(tx, op.ID, current, terminal.Code, terminal.Detail, terminal.Status)
		}
		if current.Status != "recording" || !IsRecoverableRecordingDraft(current) {
			code := "runtime_lease_lost"
			if current.Status == "recording" {
				code = "recording_draft_incomplete"
			}
			terminal = lifecycleError(http.StatusConflict, code, "runtime final receipt expired before it was committed")
			return s.failSessionAndOperationTx(tx, op.ID, current, terminal.Code, terminal.Detail, terminal.Status)
		}
		normalized, err := s.normalizer.NormalizeFinal(current, nil)
		if err != nil {
			terminal = lifecycleError(http.StatusUnprocessableEntity, "recording_draft_incomplete", "recording draft cannot be recovered")
			return s.failSessionAndOperationTx(tx, op.ID, current, terminal.Code, terminal.Detail, terminal.Status)
		}
		now := time.Now().UTC()
		result := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", current.ID, "recording", current.LifecycleRevision).Updates(map[string]any{"status": "stopped", "actions_json": normalized.ActionsJSON, "action_count": normalized.ActionCount, "dom_snapshot": normalized.DOMSnapshot, "recording_meta_json": normalized.RecordingMetaJSON, "draft_hash": normalized.DraftHash, "stopped_at": now, "last_synced_at": now, "lifecycle_revision": current.LifecycleRevision + 1, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "expired receipt changed concurrently")
		}
		if err := tx.First(&current, current.ID).Error; err != nil {
			return err
		}
		return s.completeOperation(tx, op.ID, http.StatusOK, recordingSessionSummary(current))
	})
	if err != nil {
		return translateRecordingDBError(err)
	}
	if terminal != nil {
		return terminal
	}
	return nil
}

// FailRuntimeEventScopeMismatch is the durable closure for an internal
// Manager event whose session identity is valid but whose runtime scope is
// not. A coordinator must never drop such an event based on an error code: it
// first records the failure and only then releases the untrusted receipt.
func (s *RecordingLifecycleService) FailRuntimeEventScopeMismatch(ctx context.Context, sessionID uint, eventID string, runtimeScope map[string]any) error {
	var released models.RecordingSession
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var session models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, sessionID).Error; err != nil {
			return err
		}
		if recordingSessionTerminal(session.Status) {
			released = session
			return nil
		}

		var pendingStop models.RecordingOperation
		err := tx.Where("recording_session_id = ? AND action = ? AND status = ?", session.ID, recordingActionStop, "pending").Order("id asc").First(&pendingStop).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			operationID := deterministicRuntimeOperationID("scope-mismatch", session, eventID, 0)
			sessionRef := session.ID
			pendingStop = models.RecordingOperation{
				OperationID:                 operationID,
				Action:                      recordingActionStop,
				Scope:                       recordingSessionScope(session),
				RequestPayloadHash:          canonicalRequestHash(map[string]any{"runtime_event_id": eventID}),
				RequestCanonicalizerVersion: requestCanonicalizerVersion,
				Status:                      "pending",
				RecordingSessionID:          &sessionRef,
				ProjectID:                   session.ProjectID,
				VersionID:                   session.VersionID,
				PageID:                      session.PageID,
				BrowserInstanceID:           session.BrowserInstanceID,
				RuntimePageID:               session.RuntimePageID,
				RuntimeGeneration:           session.RuntimeGeneration,
				LeaseGeneration:             session.LeaseGeneration,
				ReceiptID:                   eventID,
				CreatedAt:                   time.Now().UTC(),
				UpdatedAt:                   time.Now().UTC(),
			}
			if err := tx.Create(&pendingStop).Error; err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		result := tx.Model(&models.RecordingSession{}).Where("id = ? AND status = ? AND lifecycle_revision = ?", session.ID, session.Status, session.LifecycleRevision).Updates(map[string]any{
			"status":                   "failed",
			"failure_code":             "runtime_receipt_scope_mismatch",
			"failure_detail_sanitized": "runtime event scope does not match recording session",
			"error_message":            "runtime event scope does not match recording session",
			"failed_at":                now,
			"updated_at":               now,
			"lifecycle_revision":       session.LifecycleRevision + 1,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "recording changed while closing runtime scope mismatch")
		}
		if err := tx.Model(&models.RecordingOperation{}).Where("recording_session_id = ? AND action IN ? AND status = ?", session.ID, []string{recordingActionStart, recordingActionStop}, "pending").Updates(map[string]any{
			"status":                 "failed",
			"http_status":            http.StatusConflict,
			"error_code":             "runtime_receipt_scope_mismatch",
			"sanitized_error_detail": "runtime event scope does not match recording session",
			"failed_at":              now,
			"updated_at":             now,
		}).Error; err != nil {
			return err
		}
		released = session
		return nil
	})
	if err == nil && released.ID != 0 {
		// A scope mismatch is an incompatible runtime terminal.  A recorder may
		// still be running even though its event cannot be trusted, so receipt
		// cleanup alone is insufficient: stop and delete the exact event scope.
		// Rebuilding it from the database session would necessarily fail the
		// Manager's full-scope fence in this mismatch path.
		s.releaseRuntimeScope(ctx, runtimeScope)
	}
	return err
}

// ClosePendingStopForTerminalSession closes a Stop receipt that lost a race to
// Cancel (or another incompatible terminal state). Coordinator calls it before
// releasing a final runtime event, so the partial runtime-effect index cannot
// remain blocked by a pending operation whose session is already final.
func (s *RecordingLifecycleService) ClosePendingStopForTerminalSession(ctx context.Context, sessionID uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var session models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, sessionID).Error; err != nil {
			return err
		}
		if !recordingSessionTerminal(session.Status) {
			return lifecycleRetryError(http.StatusConflict, "recording_lifecycle_conflict", "recording session is not terminal", 1)
		}
		var pending []models.RecordingOperation
		if err := tx.Where("recording_session_id = ? AND action = ? AND status = ?", session.ID, recordingActionStop, "pending").Find(&pending).Error; err != nil {
			return err
		}
		for _, operation := range pending {
			if err := s.markOperationFailed(tx, operation.ID, http.StatusConflict, "recording_action_not_allowed", "recording session reached a terminal state before Stop committed"); err != nil {
				return err
			}
		}
		return nil
	})
}

// RecoverRuntimeLeaseLost is the only database closure for a Manager instance
// teardown. It deliberately does not invoke runtime Stop: the runtime has
// already been detached. A real pending Stop keeps its original receipt and
// idempotency identity; the deterministic internal Stop is only a fallback.
func (s *RecordingLifecycleService) RecoverRuntimeLeaseLost(ctx context.Context, session models.RecordingSession, receiptID string) error {
	if recordingSessionTerminal(session.Status) {
		// The Manager tombstone only needs acknowledgement once the business
		// session is already final. Do not manufacture an internal Stop receipt.
		return nil
	}
	var pending models.RecordingOperation
	err := s.db.Where("recording_session_id = ? AND action = ? AND status = ?", session.ID, recordingActionStop, "pending").Order("id asc").First(&pending).Error
	if err == nil {
		_, _, recoverErr := s.RecoverPendingOperation(ctx, pending.OperationID)
		return recoverErr
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if session.Status == "starting" {
		var start models.RecordingOperation
		if err := s.db.Where("recording_session_id = ? AND action = ? AND status = ?", session.ID, recordingActionStart, "pending").First(&start).Error; err == nil {
			_, _, recoverErr := s.RecoverPendingOperation(ctx, start.OperationID)
			return recoverErr
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
	}
	return s.RecoverExpiredRecordingReceipt(ctx, session, "lease-lost:"+strings.TrimSpace(receiptID), 0)
}

// FailRuntimeLeaseLostInvalidDraft is the durable closure for a tombstone
// whose newer runtime draft was structurally rejected. The failed Sync receipt
// alone is not a lifecycle outcome: this method records the session/Stop
// terminal state before Coordinator may release the tombstone event.
func (s *RecordingLifecycleService) FailRuntimeLeaseLostInvalidDraft(ctx context.Context, source models.RecordingSession, receiptID string, terminal *recordingLifecycleError) error {
	if terminal == nil {
		terminal = lifecycleError(http.StatusUnprocessableEntity, "recording_draft_incomplete", "runtime lease-lost draft is invalid")
	}
	stopOp, err := s.ensureFinalReceiptStopOperation(ctx, source, "lease-lost:"+strings.TrimSpace(receiptID))
	if err != nil {
		return err
	}
	return s.finalizeInvalidFinalReceipt(ctx, stopOp, source, terminal)
}

func recordingSessionTerminal(status string) bool {
	switch status {
	case "stopped", "saved", "cancelled", "failed":
		return true
	default:
		return false
	}
}

func (s *RecordingLifecycleService) findCapturedAuthState(sessionID uint) (models.ProjectAuthState, bool, error) {
	var row models.ProjectAuthState
	err := s.db.Where("source_recording_session_id = ?", sessionID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return models.ProjectAuthState{}, false, nil
	}
	if err != nil {
		return models.ProjectAuthState{}, false, err
	}
	return row, true, nil
}

func (s *RecordingLifecycleService) completeCapturedAuthReplay(operationID, scope, requestHash string, session models.RecordingSession, auth models.ProjectAuthState) (recordingLifecycleResult, error) {
	var response map[string]any
	var terminal *recordingLifecycleError
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, session.ID).Error; err != nil {
			return err
		}
		op, replay, err := s.beginSynchronousOperation(tx, operationID, recordingActionCapture, scope, requestHash, current)
		if err != nil {
			return err
		}
		if replay {
			result, err := replayRecordingOperation(op)
			response = result.Body
			return err
		}
		if current.Status != "stopped" && current.Status != "saved" {
			terminal = lifecycleError(http.StatusConflict, "recording_capture_not_allowed", "recording was cancelled")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		response = map[string]any{"auth_state": projectAuthStateSummary(auth)}
		return s.completeOperation(tx, op.ID, http.StatusOK, response)
	})
	if err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	if terminal != nil {
		return recordingLifecycleResult{}, terminal
	}
	return recordingLifecycleResult{Status: http.StatusOK, Body: response}, nil
}

// completePendingCapturedAuthReplay completes an already-reserved Capture.
// Unlike completeCapturedAuthReplay it must not create a second synchronous
// operation, because Capture has already claimed its runtime effect key.
func (s *RecordingLifecycleService) completePendingCapturedAuthReplay(op models.RecordingOperation, session models.RecordingSession, auth models.ProjectAuthState) (recordingLifecycleResult, error) {
	var response map[string]any
	var terminal *recordingLifecycleError
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var current models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, session.ID).Error; err != nil {
			return err
		}
		if current.RecordingKind != recordingKindLoginFlow || current.AuthContext != authContextClean || (current.Status != "stopped" && current.Status != "saved") {
			terminal = lifecycleError(http.StatusConflict, "recording_capture_not_allowed", "recording session cannot publish project auth state")
			return s.markOperationFailed(tx, op.ID, terminal.Status, terminal.Code, terminal.Detail)
		}
		response = map[string]any{"auth_state": projectAuthStateSummary(auth)}
		return s.completeOperation(tx, op.ID, http.StatusOK, response)
	})
	if err != nil {
		return recordingLifecycleResult{}, translateRecordingDBError(err)
	}
	if terminal != nil {
		return recordingLifecycleResult{}, terminal
	}
	return recordingLifecycleResult{Status: http.StatusOK, Body: response}, nil
}
func (s *RecordingLifecycleService) loadVersion(projectID, versionID uint) (models.ProjectVersion, error) {
	var row models.ProjectVersion
	return row, s.db.Where("id = ? AND project_id = ?", versionID, projectID).First(&row).Error
}

// loadPinnedAuthStateForStart deliberately follows the auth-state identity
// persisted with the starting session. Retrying a pending Start must never
// select a newer project auth state, and replaying a terminal Start never calls
// this helper at all.
func (s *RecordingLifecycleService) loadPinnedAuthStateForStart(session models.RecordingSession) (*models.ProjectAuthState, error) {
	if session.AuthContext != authContextProjectSaved || session.SourceAuthStateID == nil {
		return nil, fmt.Errorf("pinned saved project auth state is unavailable")
	}
	var auth models.ProjectAuthState
	if err := s.db.Where("id = ? AND project_id = ? AND version_id = ? AND status = ?", *session.SourceAuthStateID, session.ProjectID, session.VersionID, "active").First(&auth).Error; err != nil {
		return nil, err
	}
	if s.cipher == nil || auth.StateCiphertext == "" {
		return nil, fmt.Errorf("encrypted project auth state is unavailable")
	}
	plain, err := s.cipher.decrypt(auth)
	if err != nil {
		return nil, err
	}
	auth.StateJSON = plain
	return &auth, nil
}

func (s *RecordingLifecycleService) releaseRuntimeRecording(ctx context.Context, session models.RecordingSession) {
	s.releaseRuntimeScope(ctx, recordingSessionRuntimeScope(session))
}

func (s *RecordingLifecycleService) releaseRuntimeScope(ctx context.Context, scope map[string]any) {
	if s.runtime == nil {
		return
	}
	_, _ = s.runtime.StopPageRecording(ctx, scope)
	s.runtime.ReleaseRecordingSessionResources(ctx, scope)
}

// releaseRuntimeReceipts releases already-produced runtime artifacts without
// invoking Stop again. It is used after a durable Stop failure/cancel race.
func (s *RecordingLifecycleService) releaseRuntimeReceipts(ctx context.Context, session models.RecordingSession) {
	if s.runtime == nil {
		return
	}
	scope := recordingSessionRuntimeScope(session)
	s.runtime.ReleaseRecordingSessionResources(ctx, scope)
}

func (s *RecordingLifecycleService) releaseRuntimeFinalReceipt(ctx context.Context, session models.RecordingSession) {
	if s.runtime == nil {
		return
	}
	s.runtime.DiscardStoppedPageRecording(ctx, recordingSessionRuntimeScope(session))
}

func replayRecordingOperation(op models.RecordingOperation) (recordingLifecycleResult, error) {
	if op.Status == "failed" {
		return recordingLifecycleResult{}, lifecycleError(op.HTTPStatus, op.ErrorCode, op.SanitizedErrorDetail)
	}
	if op.Status != "completed" {
		return recordingLifecycleResult{}, lifecycleError(http.StatusConflict, "recording_operation_in_progress", "recording operation is still pending")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(op.SanitizedResponseJSON), &body); err != nil {
		return recordingLifecycleResult{}, err
	}
	return recordingLifecycleResult{Status: op.HTTPStatus, Body: body}, nil
}

// replayTerminalOperation runs before state validation for actions whose
// success may have been followed by another lifecycle transition. A completed
// Capture, for example, must still replay after a later Cancel removed the
// session's eligibility for a *new* Capture request.
func (s *RecordingLifecycleService) replayTerminalOperation(operationID, action, scope, requestHash string) (recordingLifecycleResult, bool, error) {
	var op models.RecordingOperation
	err := s.db.Where("operation_id = ?", operationID).First(&op).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return recordingLifecycleResult{}, false, nil
	}
	if err != nil {
		return recordingLifecycleResult{}, true, translateRecordingDBError(err)
	}
	if op.Action != action || op.Scope != scope || op.RequestPayloadHash != requestHash {
		return recordingLifecycleResult{}, true, lifecycleError(http.StatusConflict, "operation_id_payload_conflict", "operation_id was already used for another request")
	}
	if op.Status != "completed" && op.Status != "failed" {
		return recordingLifecycleResult{}, false, nil
	}
	result, err := replayRecordingOperation(op)
	return result, true, err
}

func startRecordingResponse(session models.RecordingSession) map[string]any {
	response := recordingSessionSummary(session)
	response["recording_meta"] = recordingSessionMeta(session)
	return response
}
func pageScriptResponse(script models.PageScript) map[string]any {
	return map[string]any{"message": "主流程录制保存成功", "script": map[string]any{"id": script.ID, "page_id": script.PageID, "name": script.Name, "source_recording_session_id": script.SourceRecordingSessionID, "page_script_content_hash": script.PageScriptContentHash, "normalizer_version": script.NormalizerVersion}, "recording_session_id": script.SourceRecordingSessionID}
}
func recordingSessionScope(session models.RecordingSession) string {
	return fmt.Sprintf("recording_session:%d", session.ID)
}
func recordingStartScope(input startRecordingLifecycleInput) string {
	return fmt.Sprintf("project:%d/version:%d/page:%d/instance:%s/runtime_page:%s", input.ProjectID, input.VersionID, input.PageID, input.BrowserInstanceID, input.RuntimePageID)
}

func validateOperationID(value string) error {
	if _, err := uuid.Parse(strings.TrimSpace(value)); err != nil {
		return lifecycleError(http.StatusBadRequest, "operation_id_invalid", "operation_id must be a UUID")
	}
	return nil
}
func canonicalRequestHash(value any) string {
	data, _ := json.Marshal(canonicalRequestValue(value))
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func canonicalCaptureRequestHash(input captureRecordingLifecycleInput) string {
	replace := true
	if input.Replace != nil {
		replace = *input.Replace
	}
	return canonicalRequestHash(map[string]any{
		"name":             strings.TrimSpace(input.Name),
		"captured_url":     strings.TrimSpace(input.CapturedURL),
		"origin_allowlist": canonicalOriginAllowlist(input.OriginAllowlist),
		"replace":          replace,
	})
}

func canonicalOriginAllowlist(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// canonicalRequestValue recursively parses embedded JSON before marshalling.
// json.RawMessage otherwise keeps its original object key order, which makes
// a semantic response-loss retry look like an operation payload conflict.
// Arrays deliberately retain their order because action order is business
// significant.
func canonicalRequestValue(value any) any {
	switch current := value.(type) {
	case json.RawMessage:
		return canonicalRawJSON(current)
	case *json.RawMessage:
		if current == nil {
			return nil
		}
		return canonicalRawJSON(*current)
	case map[string]any:
		out := make(map[string]any, len(current))
		for key, item := range current {
			out[key] = canonicalRequestValue(item)
		}
		return out
	case []any:
		out := make([]any, len(current))
		for index, item := range current {
			out[index] = canonicalRequestValue(item)
		}
		return out
	default:
		return current
	}
}

func canonicalRawJSON(raw json.RawMessage) any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		// Invalid JSON is rejected by the normalizer before it can become a
		// business fact. Keeping it as a string here still makes the hash stable
		// for exact malformed retry payloads without pretending it was valid.
		return map[string]any{"invalid_json": trimmed}
	}
	// A decoder accepts a valid first value and leaves trailing bytes unread.
	// Request identity must distinguish that malformed payload from the valid
	// JSON prefix, otherwise a retry can incorrectly replay another request.
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return map[string]any{"invalid_json": trimmed}
	}
	return canonicalRequestValue(parsed)
}

func finalReceiptSemanticFailure(err error) (*recordingLifecycleError, bool) {
	var terminal *recordingLifecycleError
	if !errors.As(err, &terminal) {
		return nil, false
	}
	switch terminal.Code {
	case "recording_actions_invalid", "recording_source_invalid", "sync_revision_payload_conflict":
		return terminal, true
	default:
		return nil, false
	}
}

// finalizeInvalidFinalReceipt is the single durable failure closure for an
// unusable final receipt. Both HTTP Stop and Coordinator event recovery use
// it so a semantic failure cannot strand a recording session, pending Stop
// effect, or receipt claim in different states.
func (s *RecordingLifecycleService) finalizeInvalidFinalReceipt(ctx context.Context, source models.RecordingOperation, sourceSession models.RecordingSession, terminal *recordingLifecycleError) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var op models.RecordingOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&op, source.ID).Error; err != nil {
			return err
		}
		var session models.RecordingSession
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&session, sourceSession.ID).Error; err != nil {
			return err
		}
		if op.Status == "failed" && session.Status == "failed" && op.ErrorCode == terminal.Code {
			return nil
		}
		if op.Status != "pending" {
			return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "Stop operation already reached another terminal state")
		}
		return s.failSessionAndOperationTx(tx, op.ID, session, terminal.Code, terminal.Detail, terminal.Status)
	})
}

// ensureFinalReceiptStopOperation gives Coordinator a Stop receipt before it
// closes a final event rejected by the pre-Stop Sync. The runtime receipt
// already exists, so this internal operation deliberately reserves no new
// runtime effect.
func (s *RecordingLifecycleService) ensureFinalReceiptStopOperation(ctx context.Context, session models.RecordingSession, receiptID string) (models.RecordingOperation, error) {
	operationID := deterministicRuntimeOperationID("stop", session, receiptID, 0)
	var op models.RecordingOperation
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var existing models.RecordingOperation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("operation_id = ?", operationID).First(&existing).Error
		if err == nil {
			if existing.Action != recordingActionStop || existing.Scope != recordingSessionScope(session) {
				return lifecycleError(http.StatusConflict, "operation_id_payload_conflict", "final receipt Stop identity conflicts with an existing operation")
			}
			op = existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", session.ID).First(&models.RecordingSession{}).Error; err != nil {
			return err
		}
		var pending models.RecordingOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("recording_session_id = ? AND action = ? AND status = ?", session.ID, recordingActionStop, "pending").Order("id asc").First(&pending).Error; err == nil {
			op = pending
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		sessionID := session.ID
		now := time.Now().UTC()
		op = models.RecordingOperation{
			OperationID: operationID, Action: recordingActionStop, Scope: recordingSessionScope(session),
			RequestPayloadHash: canonicalRequestHash(map[string]any{"runtime_final_receipt_id": receiptID}), RequestCanonicalizerVersion: requestCanonicalizerVersion,
			Status: "pending", RecordingSessionID: &sessionID, ProjectID: session.ProjectID, VersionID: session.VersionID, PageID: session.PageID,
			BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration,
			ReceiptID: receiptID, CreatedAt: now, UpdatedAt: now,
		}
		return tx.Create(&op).Error
	})
	return op, err
}

func runtimeEffectKey(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func isPendingRuntimeEffectConstraint(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "recording_operations_pending_effect_uniq") ||
		(strings.Contains(lower, "unique constraint") && strings.Contains(lower, "runtime_effect_key"))
}
func lifecycleError(status int, code, detail string) *recordingLifecycleError {
	return &recordingLifecycleError{Status: status, Code: code, Detail: detail}
}
func lifecycleCode(err error) string {
	var typed *recordingLifecycleError
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}
func lifecycleCodeOr(err error, fallback string) string {
	if code := lifecycleCode(err); code != "" {
		return code
	}
	// RecordingNormalizer exposes stable internal classification strings, not
	// user text. Preserve those classifications in the persisted failure code
	// so an invalid action trace is distinguishable from a malformed source.
	switch strings.TrimSpace(fmt.Sprint(err)) {
	case "recording_actions_invalid", "recording_source_invalid", "recording_draft_incomplete":
		return strings.TrimSpace(fmt.Sprint(err))
	}
	return fallback
}
func translateRecordingDBError(err error) error {
	if err == nil {
		return nil
	}
	var typed *recordingLifecycleError
	if errors.As(err, &typed) {
		return typed
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "recording_sessions_active_instance_uniq") || strings.Contains(lower, "unique constraint") && strings.Contains(lower, "browser_instance_id") {
		return lifecycleError(http.StatusConflict, "recording_lifecycle_conflict", "browser instance already records a page")
	}
	return lifecycleError(http.StatusInternalServerError, "recording_lifecycle_store_failed", "recording lifecycle transaction failed")
}
