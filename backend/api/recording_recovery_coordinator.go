package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/pkg/logger"
	"github.com/browserwing/browserwing/services/browser"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var recordingRuntimeOperationNamespace = uuid.MustParse("61b09bb2-ae4f-5cc6-a5f3-9e2f66a4b0bb")

const expiredPendingStartRecoveryInterval = time.Second

// RecordingRecoveryCoordinator is the application-level bridge from
// Manager's runtime-only observations to RecordingLifecycleService. Manager
// retains a re-drivable latest-draft/final-receipt queue until this coordinator
// receives a durable terminal outcome; PostgreSQL remains the business fact
// source after each successful operation.
type RecordingRecoveryCoordinator struct {
	service *RecordingLifecycleService
	manager *browser.Manager

	mu                              sync.Mutex
	sessionLocks                    map[string]*sync.Mutex
	retries                         map[string]runtimeEventRetry
	wake                            chan struct{}
	startOnce                       sync.Once
	nextExpiredPendingStartRecovery time.Time
}

type runtimeEventRetry struct {
	attempts int
	next     time.Time
}

type runtimeEventDisposition uint8

const (
	runtimeEventDispositionRetry runtimeEventDisposition = iota
	runtimeEventDispositionAcknowledge
	runtimeEventDispositionRelease
)

type runtimeEventOutcome struct {
	disposition runtimeEventDisposition
	err         error
}

func NewRecordingRecoveryCoordinator(service *RecordingLifecycleService, manager *browser.Manager) *RecordingRecoveryCoordinator {
	return &RecordingRecoveryCoordinator{
		service:      service,
		manager:      manager,
		sessionLocks: make(map[string]*sync.Mutex),
		retries:      make(map[string]runtimeEventRetry),
		wake:         make(chan struct{}, 1),
	}
}

// Start installs the bridge, runs one best-effort pending-operation recovery
// pass and starts a single queue reconciler. Recovery only logs infrastructure
// failures: a persisted completed/failed business outcome is converged.
func (c *RecordingRecoveryCoordinator) Start(ctx context.Context) {
	if c == nil || c.service == nil {
		return
	}
	c.startOnce.Do(func() {
		if c.manager != nil {
			c.manager.SetRecordingRuntimeEventSink(c.EnqueueRuntimeEvent)
			c.manager.StartRecordingReceiptJanitor(ctx)
		}
		if err := c.service.RecoverPendingOperations(ctx); err != nil {
			logger.Warn(ctx, "Recording pending-operation startup recovery infrastructure error: %v", err)
		}
		go c.run(ctx)
		c.signal()
	})
}

// EnqueueRuntimeEvent is only a wake-up signal. The Manager registry keeps
// the event data, so a full queue/temporary DB failure cannot lose a Stop.
func (c *RecordingRecoveryCoordinator) EnqueueRuntimeEvent(event browser.RecordingRuntimeEvent) {
	if c == nil || c.service == nil || c.manager == nil || event.Scope.RecordingSessionID == "" {
		return
	}
	c.signal()
}

func (c *RecordingRecoveryCoordinator) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *RecordingRecoveryCoordinator) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		c.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.wake:
		}
	}
}

func (c *RecordingRecoveryCoordinator) reconcile(ctx context.Context) {
	c.recoverExpiredPendingStarts(ctx, time.Now().UTC())
	if c.manager == nil {
		return
	}
	for _, event := range c.manager.PendingRecordingRuntimeEvents() {
		if !c.shouldTry(event.ID) {
			continue
		}
		lock := c.sessionLock(event.Scope.RecordingSessionID)
		lock.Lock()
		outcome := c.processRuntimeEvent(ctx, event)
		lock.Unlock()
		switch outcome.disposition {
		case runtimeEventDispositionAcknowledge:
			c.manager.AcknowledgeRecordingRuntimeEvent(event)
			c.clearRetry(event.ID)
		case runtimeEventDispositionRelease:
			// Release is only emitted by a lifecycle method after its terminal
			// database transaction has committed. Infrastructure failures remain
			// Retry even when they surface as a lifecycle-shaped error.
			c.manager.ReleaseRecordingRuntimeEvent(event)
			c.clearRetry(event.ID)
			logger.Warn(ctx, "Recording runtime event %s reached durable discard outcome: %v", event.ID, outcome.err)
		default:
			c.deferRetry(event.ID)
			logger.Warn(ctx, "Recording runtime event %s (%s) will be retried: %v", event.ID, event.Kind, outcome.err)
		}
	}
}

func (c *RecordingRecoveryCoordinator) recoverExpiredPendingStarts(ctx context.Context, now time.Time) {
	if c == nil || c.service == nil {
		return
	}
	c.mu.Lock()
	if !c.nextExpiredPendingStartRecovery.IsZero() && now.Before(c.nextExpiredPendingStartRecovery) {
		c.mu.Unlock()
		return
	}
	c.nextExpiredPendingStartRecovery = now.Add(expiredPendingStartRecoveryInterval)
	c.mu.Unlock()
	if err := c.service.RecoverExpiredPendingStarts(ctx, now); err != nil {
		logger.Warn(ctx, "Recording expired Start recovery infrastructure error: %v", err)
	}
}

func (c *RecordingRecoveryCoordinator) shouldTry(eventID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	retry := c.retries[eventID]
	return retry.next.IsZero() || !time.Now().UTC().Before(retry.next)
}

func (c *RecordingRecoveryCoordinator) deferRetry(eventID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	retry := c.retries[eventID]
	retry.attempts++
	delay := time.Second << min(retry.attempts-1, 6)
	if delay > time.Minute {
		delay = time.Minute
	}
	retry.next = time.Now().UTC().Add(delay)
	c.retries[eventID] = retry
}

func (c *RecordingRecoveryCoordinator) clearRetry(eventID string) {
	c.mu.Lock()
	delete(c.retries, eventID)
	c.mu.Unlock()
}

func (c *RecordingRecoveryCoordinator) sessionLock(sessionID string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	lock := c.sessionLocks[sessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		c.sessionLocks[sessionID] = lock
	}
	return lock
}

// StopRecordingSession is the HTTP Stop entry point. It records the pending
// runtime effect before driving the scoped runtime, then converges the frozen
// final receipt through the same recording_stopped event path used by Manager
// and restart recovery. RecordingLifecycleService is therefore never called
// directly by an HTTP handler to write a session as stopped.
func (c *RecordingRecoveryCoordinator) StopRecordingSession(ctx context.Context, input stopRecordingLifecycleInput) (recordingLifecycleResult, error) {
	if c == nil || c.service == nil {
		return recordingLifecycleResult{}, lifecycleError(http.StatusInternalServerError, "runtime_lease_lost", "recording lifecycle coordinator is not configured")
	}
	lock := c.sessionLock(strconv.FormatUint(uint64(input.Session.ID), 10))
	lock.Lock()
	defer lock.Unlock()

	result, handled, session, runtimeResult, err := c.service.driveStopRuntime(ctx, input)
	if handled || err != nil {
		return result, err
	}
	event := recordingStoppedHTTPEvent(input, session, runtimeResult)
	outcome, eventResult := c.processStoppedRuntimeEvent(ctx, event, runtimeResult)
	return eventResult, outcome.err
}

// recordingStoppedHTTPEvent converts an HTTP-driven final receipt into the
// exact event shape consumed by Coordinator. The actual payload remains in
// runtimeResult so sparse/Go-serialized action JSON reaches the normalizer
// unchanged instead of being lossy-converted through RecordingRuntimeEvent.
func recordingStoppedHTTPEvent(input stopRecordingLifecycleInput, session models.RecordingSession, runtimeResult map[string]any) browser.RecordingRuntimeEvent {
	receiptID := strings.TrimSpace(stringFromAny(runtimeResult["runtime_final_receipt_id"]))
	claimGeneration := uint64(uintFromAny(runtimeResult["runtime_final_receipt_claim_generation"]))
	revision := uint64(uintFromAny(runtimeResult["runtime_final_sync_revision"]))
	return browser.RecordingRuntimeEvent{
		ID:              "http-stop:" + input.OperationID,
		Kind:            "recording_stopped",
		Scope:           browser.RecordingStorageScope{ProjectID: session.ProjectID, VersionID: session.VersionID, PageID: session.PageID, RecordingSessionID: strconv.FormatUint(uint64(session.ID), 10), BrowserInstanceID: session.BrowserInstanceID, RuntimePageID: session.RuntimePageID, RuntimeGeneration: session.RuntimeGeneration, LeaseGeneration: session.LeaseGeneration},
		ReceiptID:       receiptID,
		OperationID:     input.OperationID,
		ClaimGeneration: claimGeneration,
		SyncRevision:    revision,
		DOMSnapshot:     append(json.RawMessage(nil), input.DOMSnapshot...),
	}
}

func (c *RecordingRecoveryCoordinator) processRuntimeEvent(ctx context.Context, event browser.RecordingRuntimeEvent) runtimeEventOutcome {
	sessionID, err := strconv.ParseUint(event.Scope.RecordingSessionID, 10, 32)
	if err != nil {
		return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: fmt.Errorf("runtime event recording session id: %w", err)}
	}
	var session models.RecordingSession
	if err := c.service.db.First(&session, uint(sessionID)).Error; err != nil {
		return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}
	}
	if !runtimeEventMatchesSession(event, session) {
		if recordingSessionTerminal(session.Status) {
			// A stale tombstone for an already-final session is only an event
			// acknowledgement; do not discard retained saved-session auth state.
			return runtimeEventOutcome{disposition: runtimeEventDispositionAcknowledge}
		}
		err := c.service.FailRuntimeEventScopeMismatch(ctx, session.ID, event.ID, runtimeScopeFromEvent(event))
		if err != nil {
			return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}
		}
		return runtimeEventOutcome{disposition: runtimeEventDispositionRelease, err: lifecycleError(409, "runtime_receipt_scope_mismatch", "runtime event scope does not match recording session")}
	}

	switch event.Kind {
	case "draft_sync":
		if session.Status == "starting" {
			// Recorder may be live before Start transaction 2 publishes
			// `recording`. Keep its draft receipt until that transition commits.
			return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "recording start has not committed", 1)}
		}
		if session.Status != "recording" {
			return runtimeEventOutcome{disposition: runtimeEventDispositionAcknowledge}
		}
		if event.SyncRevision <= session.SyncRevision {
			return runtimeEventOutcome{disposition: runtimeEventDispositionAcknowledge}
		}
		opID := deterministicRuntimeOperationID("sync", session, event.ReceiptID, event.SyncRevision)
		return c.operationOutcome(opID, c.persistRuntimeSync(ctx, session, event, event.SyncRevision), runtimeEventDispositionRelease)
	case "recording_stopped":
		outcome, _ := c.processStoppedRuntimeEvent(ctx, event, nil)
		return outcome
	case "recording_receipt_expired":
		opID := deterministicRuntimeOperationID("stop", session, event.ReceiptID, 0)
		return c.operationOutcome(opID, c.service.RecoverExpiredRecordingReceipt(ctx, session, event.ReceiptID, event.SyncRevision), runtimeEventDispositionAcknowledge)
	case "runtime_lease_lost":
		if recordingSessionTerminal(session.Status) {
			// A tombstone for an already-final session is only an acknowledgement
			// obligation. Creating another internal Stop would pollute operation
			// history and can re-open a retired runtime effect scope.
			return runtimeEventOutcome{disposition: runtimeEventDispositionAcknowledge}
		}
		if session.Status == "recording" && event.SyncRevision > session.SyncRevision {
			opID := deterministicRuntimeOperationID("sync", session, event.ReceiptID, event.SyncRevision)
			if syncErr := c.persistRuntimeSync(ctx, session, event, event.SyncRevision); syncErr != nil {
				if terminal, ok := finalReceiptSemanticFailure(syncErr); ok {
					if closeErr := c.service.FailRuntimeLeaseLostInvalidDraft(ctx, session, event.ReceiptID, terminal); closeErr != nil {
						return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: closeErr}
					}
					return runtimeEventOutcome{disposition: runtimeEventDispositionRelease, err: terminal}
				}
				var current models.RecordingSession
				if loadErr := c.service.db.First(&current, session.ID).Error; loadErr != nil {
					return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: loadErr}
				}
				// A concurrent Sync may have advanced past this tombstone. Its
				// deterministic failed Sync receipt is not a reason to lose the
				// lease-lost event; continue recovery from the newer durable draft.
				if current.SyncRevision < event.SyncRevision && !recordingSessionTerminal(current.Status) {
					return c.operationOutcome(opID, syncErr, runtimeEventDispositionRetry)
				}
				session = current
			}
			if err := c.service.db.First(&session, session.ID).Error; err != nil {
				return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}
			}
		}
		err := c.service.RecoverRuntimeLeaseLost(ctx, session, event.ReceiptID)
		if err == nil {
			return runtimeEventOutcome{disposition: runtimeEventDispositionAcknowledge}
		}
		return c.sessionOutcome(session.ID, err, runtimeEventDispositionAcknowledge)
	default:
		return runtimeEventOutcome{disposition: runtimeEventDispositionAcknowledge}
	}
}

// processStoppedRuntimeEvent is the single stopped-session convergence path.
// runtimeResult is non-nil only for a synchronous HTTP request that has just
// frozen a receipt; Manager/recovery events leave it nil and are re-driven by
// the scoped runtime adapter as before.
func (c *RecordingRecoveryCoordinator) processStoppedRuntimeEvent(ctx context.Context, event browser.RecordingRuntimeEvent, runtimeResult map[string]any) (runtimeEventOutcome, recordingLifecycleResult) {
	sessionID, err := strconv.ParseUint(event.Scope.RecordingSessionID, 10, 32)
	if err != nil {
		return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: fmt.Errorf("runtime event recording session id: %w", err)}, recordingLifecycleResult{}
	}
	var session models.RecordingSession
	if err := c.service.db.First(&session, uint(sessionID)).Error; err != nil {
		return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}, recordingLifecycleResult{}
	}
	if !runtimeEventMatchesSession(event, session) {
		if recordingSessionTerminal(session.Status) {
			return runtimeEventOutcome{disposition: runtimeEventDispositionAcknowledge}, recordingLifecycleResult{}
		}
		err := c.service.FailRuntimeEventScopeMismatch(ctx, session.ID, event.ID, runtimeScopeFromEvent(event))
		if err != nil {
			return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}, recordingLifecycleResult{}
		}
		return runtimeEventOutcome{disposition: runtimeEventDispositionRelease, err: lifecycleError(409, "runtime_receipt_scope_mismatch", "runtime event scope does not match recording session")}, recordingLifecycleResult{}
	}
	if session.Status == "starting" {
		// A final receipt is stronger than a speculative draft, but it still
		// cannot be consumed before the Start operation has chosen recording
		// or a durable terminal. Releasing it here would strand a stopped
		// runtime behind a later `starting -> recording` commit.
		return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: lifecycleRetryError(http.StatusConflict, "recording_operation_in_progress", "recording start has not committed", 1)}, recordingLifecycleResult{}
	}
	if session.Status == "cancelled" || session.Status == "failed" || session.Status == "saved" {
		if err := c.service.ClosePendingStopForTerminalSession(ctx, session.ID); err != nil {
			return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}, recordingLifecycleResult{}
		}
		if session.Status == "saved" {
			// Save is compatible with a later Capture, so discard only the
			// unadopted final receipt and retain the auth snapshot.
			c.service.releaseRuntimeFinalReceipt(ctx, session)
		} else {
			c.service.releaseRuntimeReceipts(ctx, session)
		}
		return runtimeEventOutcome{disposition: runtimeEventDispositionRelease, err: lifecycleError(409, "recording_action_not_allowed", "stopped runtime receipt lost lifecycle race")}, recordingLifecycleResult{}
	}
	// An equal revision still has to prove that its payload is identical.
	// Only a strictly older receipt is stale evidence that may skip Sync.
	preservePersistedDraft := event.SyncRevision < session.SyncRevision
	if session.Status == "recording" {
		if !preservePersistedDraft && runtimeResult == nil {
			opID := deterministicRuntimeOperationID("sync", session, event.ReceiptID, event.SyncRevision)
			if syncErr := c.persistRuntimeSync(ctx, session, event, event.SyncRevision); syncErr != nil {
				if terminal, ok := finalReceiptSemanticFailure(syncErr); ok {
					stopOp, ensureErr := c.service.ensureFinalReceiptStopOperation(ctx, session, event.ReceiptID)
					if ensureErr != nil {
						return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: ensureErr}, recordingLifecycleResult{}
					}
					if closeErr := c.service.finalizeInvalidFinalReceipt(ctx, stopOp, session, terminal); closeErr != nil {
						return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: closeErr}, recordingLifecycleResult{}
					}
					c.service.releaseRuntimeReceipts(ctx, session)
					return runtimeEventOutcome{disposition: runtimeEventDispositionRelease, err: terminal}, recordingLifecycleResult{}
				}
				if outcome := c.operationOutcome(opID, syncErr, runtimeEventDispositionRelease); outcome.disposition != runtimeEventDispositionAcknowledge {
					return outcome, recordingLifecycleResult{}
				}
			}
		}
		if err := c.service.db.First(&session, session.ID).Error; err != nil {
			return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}, recordingLifecycleResult{}
		}
	}
	if session.Status == "stopped" {
		return c.acknowledgeCommittedStopReceipt(ctx, session, event), recordingLifecycleResult{Status: http.StatusOK, Body: recordingSessionSummary(session)}
	}
	opID := strings.TrimSpace(event.OperationID)
	if opID == "" {
		opID = deterministicRuntimeOperationID("stop", session, event.ReceiptID, 0)
	}
	result, err := c.service.Stop(ctx, stopRecordingLifecycleInput{
		OperationID:                 opID,
		Session:                     session,
		DOMSnapshot:                 append(json.RawMessage(nil), event.DOMSnapshot...),
		FinalReceiptID:              event.ReceiptID,
		FinalReceiptClaimGeneration: event.ClaimGeneration,
		FinalReceiptRevision:        event.SyncRevision,
		PreservePersistedDraft:      preservePersistedDraft,
		runtimeResult:               runtimeResult,
	})
	return c.operationOutcome(opID, err, runtimeEventDispositionRelease), result
}

func runtimeScopeFromEvent(event browser.RecordingRuntimeEvent) map[string]any {
	return map[string]any{
		"project_id": event.Scope.ProjectID, "version_id": event.Scope.VersionID, "page_id": event.Scope.PageID,
		"recording_session_id": event.Scope.RecordingSessionID, "browser_instance_id": event.Scope.BrowserInstanceID,
		"runtime_page_id": event.Scope.RuntimePageID, "runtime_generation": event.Scope.RuntimeGeneration,
		"lease_generation": event.Scope.LeaseGeneration,
	}
}

// acknowledgeCommittedStopReceipt closes the crash window after Stop's DB
// transaction commits but before its runtime ACK. A completed Stop receipt is
// the durable proof that this exact final receipt was adopted; other stale
// receipts are released rather than allowed to survive until TTL.
func (c *RecordingRecoveryCoordinator) acknowledgeCommittedStopReceipt(ctx context.Context, session models.RecordingSession, event browser.RecordingRuntimeEvent) runtimeEventOutcome {
	if strings.TrimSpace(event.ReceiptID) == "" {
		return runtimeEventOutcome{disposition: runtimeEventDispositionRelease, err: lifecycleError(http.StatusConflict, "runtime_receipt_unavailable", "stopped event has no final receipt identity")}
	}
	var operation models.RecordingOperation
	err := c.service.db.Where("recording_session_id = ? AND action = ? AND status = ? AND receipt_id = ?", session.ID, recordingActionStop, "completed", event.ReceiptID).Order("id desc").First(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return runtimeEventOutcome{disposition: runtimeEventDispositionRelease, err: lifecycleError(http.StatusConflict, "runtime_receipt_unavailable", "final receipt was not adopted by the stopped session")}
	}
	if err != nil {
		return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}
	}
	if c.service.runtime != nil {
		ackInput := recordingSessionRuntimeScope(session)
		ackInput["runtime_final_receipt_id"] = event.ReceiptID
		ackInput["operation_id"] = operation.OperationID
		ackInput["runtime_final_receipt_claim_generation"] = operation.RuntimeReceiptClaimGeneration
		c.service.runtime.AcknowledgeStoppedPageRecording(ctx, ackInput)
	}
	return runtimeEventOutcome{disposition: runtimeEventDispositionAcknowledge}
}

func (c *RecordingRecoveryCoordinator) operationOutcome(operationID string, err error, durableFailure runtimeEventDisposition) runtimeEventOutcome {
	if err == nil {
		return runtimeEventOutcome{disposition: runtimeEventDispositionAcknowledge}
	}
	var operation models.RecordingOperation
	if loadErr := c.service.db.Where("operation_id = ?", operationID).First(&operation).Error; loadErr != nil {
		return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}
	}
	if operation.Status == "completed" || operation.Status == "failed" {
		return runtimeEventOutcome{disposition: durableFailure, err: err}
	}
	return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}
}

func (c *RecordingRecoveryCoordinator) sessionOutcome(sessionID uint, err error, durableOutcome runtimeEventDisposition) runtimeEventOutcome {
	var session models.RecordingSession
	if loadErr := c.service.db.First(&session, sessionID).Error; loadErr != nil {
		return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}
	}
	if recordingSessionTerminal(session.Status) {
		return runtimeEventOutcome{disposition: durableOutcome, err: err}
	}
	return runtimeEventOutcome{disposition: runtimeEventDispositionRetry, err: err}
}

func (c *RecordingRecoveryCoordinator) persistRuntimeSync(ctx context.Context, session models.RecordingSession, event browser.RecordingRuntimeEvent, revision uint64) error {
	actions, err := json.Marshal(event.Actions)
	if err != nil {
		return err
	}
	_, err = c.service.Sync(ctx, syncRecordingLifecycleInput{
		OperationID:  deterministicRuntimeOperationID("sync", session, event.ReceiptID, revision),
		Session:      session,
		SyncRevision: revision,
		Actions:      actions,
		DOMSnapshot:  append(json.RawMessage(nil), event.DOMSnapshot...),
	})
	return err
}

func runtimeEventMatchesSession(event browser.RecordingRuntimeEvent, session models.RecordingSession) bool {
	return event.Scope.ProjectID == session.ProjectID &&
		event.Scope.VersionID == session.VersionID &&
		event.Scope.PageID == session.PageID &&
		event.Scope.RecordingSessionID == strconv.FormatUint(uint64(session.ID), 10) &&
		event.Scope.BrowserInstanceID == session.BrowserInstanceID &&
		event.Scope.RuntimePageID == session.RuntimePageID &&
		event.Scope.RuntimeGeneration == session.RuntimeGeneration &&
		event.Scope.LeaseGeneration == session.LeaseGeneration
}

func deterministicRuntimeOperationID(kind string, session models.RecordingSession, receiptID string, revision uint64) string {
	name := fmt.Sprintf("%s|%d|%s|%s|%d", kind, session.ID, session.RuntimeGeneration, receiptID, revision)
	return uuid.NewSHA1(recordingRuntimeOperationNamespace, []byte(name)).String()
}
