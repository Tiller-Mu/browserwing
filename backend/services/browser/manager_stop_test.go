package browser

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/models"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

func TestProjectRecordingDownloadBehaviorDeniesThenRestores(t *testing.T) {
	previous := callBrowserDownloadBehavior
	t.Cleanup(func() { callBrowserDownloadBehavior = previous })

	type downloadBehaviorCall struct {
		behavior      proto.BrowserSetDownloadBehaviorBehavior
		downloadPath  string
		eventsEnabled bool
	}
	calls := make([]downloadBehaviorCall, 0, 2)
	callBrowserDownloadBehavior = func(_ *rod.Browser, request *proto.BrowserSetDownloadBehavior) error {
		calls = append(calls, downloadBehaviorCall{
			behavior:      request.Behavior,
			downloadPath:  request.DownloadPath,
			eventsEnabled: request.EventsEnabled,
		})
		return nil
	}

	browserInstance := &rod.Browser{}
	manager := &Manager{downloadPath: "project-downloads"}
	if err := manager.setProjectDownloadBehaviorLocked(browserInstance, true); err != nil {
		t.Fatalf("deny project downloads: %v", err)
	}
	manager.restoreProjectDownloadBehaviorLocked(context.Background())

	if len(calls) != 2 {
		t.Fatalf("download behavior calls = %d, want deny then restore", len(calls))
	}
	if calls[0].behavior != proto.BrowserSetDownloadBehaviorBehaviorDeny || calls[0].downloadPath != "" || !calls[0].eventsEnabled {
		t.Fatalf("deny call = %+v, want deny with events and no path", calls[0])
	}
	if calls[1].behavior != proto.BrowserSetDownloadBehaviorBehaviorAllow || calls[1].downloadPath != "project-downloads" || !calls[1].eventsEnabled {
		t.Fatalf("restore call = %+v, want allow with configured path and events", calls[1])
	}
	if manager.projectDownloadsDenied || manager.projectDownloadBrowser != nil {
		t.Fatal("restoring ordinary download behavior left project download state active")
	}
}

func TestCurrentBrowserUserDataDirPrefersCurrentInstanceOverGlobalConfig(t *testing.T) {
	manager := &Manager{
		config: &config.Config{Browser: &config.BrowserConfig{UserDataDir: "global-profile"}},
		instances: map[string]*BrowserInstanceRuntime{
			"current": {
				instance: &models.BrowserInstance{
					ID:          "current",
					Type:        "local",
					UserDataDir: "current-profile",
				},
			},
		},
		currentInstanceID: "current",
	}

	if got := manager.currentBrowserUserDataDirLocked(); got != "current-profile" {
		t.Fatalf("current browser user-data directory = %q, want current instance profile", got)
	}
}

func TestCurrentBrowserUserDataDirUsesGlobalConfigForLegacyManager(t *testing.T) {
	manager := &Manager{
		config: &config.Config{Browser: &config.BrowserConfig{UserDataDir: "global-profile"}},
	}

	if got := manager.currentBrowserUserDataDirLocked(); got != "global-profile" {
		t.Fatalf("legacy browser user-data directory = %q, want global config profile", got)
	}
}

func TestPendingStoppedRecordingStorageScopeKeepsInPageStopOwnership(t *testing.T) {
	want := RecordingStorageScope{
		ProjectID:          1,
		VersionID:          2,
		PageID:             3,
		RecordingSessionID: "42",
	}
	manager := &Manager{
		recordingRegistry: &recordingRuntimeRegistry{finalBySession: map[string]*recordingFinalReceipt{
			want.RecordingSessionID: {scope: want, state: recordingReceiptAvailable},
		}},
	}

	got, pending := manager.PendingStoppedRecordingStorageScope()
	if !pending {
		t.Fatal("pending in-page recording stop was not reported")
	}
	if got != want {
		t.Fatalf("pending in-page recording scope = %+v, want %+v", got, want)
	}
}

func TestStopRecordingWithStorageScopeKeepsPendingResultUntilAcknowledged(t *testing.T) {
	scope := RecordingStorageScope{
		ProjectID:          1,
		VersionID:          2,
		PageID:             3,
		RecordingSessionID: "42",
	}
	manager := &Manager{recordingRegistry: &recordingRuntimeRegistry{finalBySession: map[string]*recordingFinalReceipt{
		scope.RecordingSessionID: {receiptID: "receipt-a", scope: scope, actions: []models.ScriptAction{{Type: "click", Selector: "#login"}}, state: recordingReceiptAvailable},
	}}}

	actions, _, err := manager.StopRecordingWithStorageScope(context.Background(), scope)
	if err != nil {
		t.Fatalf("StopRecordingWithStorageScope: %v", err)
	}
	if len(actions) != 1 || actions[0].Selector != "#login" {
		t.Fatalf("stopped actions = %+v, want pending action copy", actions)
	}
	if _, pending := manager.PendingStoppedRecordingStorageScope(); !pending {
		t.Fatal("stopped in-page recording was consumed before its session persisted")
	}

	receiptID, _, claimGeneration := manager.FinalRecordingReceiptInfo(scope)
	if !manager.AcknowledgeFinalRecordingReceipt(scope, receiptID, runtimeStopOperationID(scope), claimGeneration) {
		t.Fatal("matching scoped operation should acknowledge its final receipt")
	}
	if _, pending := manager.PendingStoppedRecordingStorageScope(); pending {
		t.Fatal("stopped in-page recording remained pending after acknowledgement")
	}
}

func TestStoppedRecordingReceiptClaimBindsToOneOperationUntilReleased(t *testing.T) {
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "42"}
	manager := &Manager{recordingRegistry: &recordingRuntimeRegistry{finalBySession: map[string]*recordingFinalReceipt{
		scope.RecordingSessionID: {receiptID: "receipt-a", scope: scope, actions: []models.ScriptAction{{Type: "click", Selector: "#login"}}, state: recordingReceiptAvailable},
	}}}

	if _, _, err := manager.StopRecordingWithClaim(context.Background(), scope, "operation-a"); err != nil {
		t.Fatalf("claim receipt for operation A: %v", err)
	}
	if _, _, err := manager.StopRecordingWithClaim(context.Background(), scope, "operation-a"); err != nil {
		t.Fatalf("reclaim receipt for operation A: %v", err)
	}
	if _, _, err := manager.StopRecordingWithClaim(context.Background(), scope, "operation-b"); !errors.Is(err, ErrRecordingStopInProgress) {
		t.Fatalf("operation B error = %v, want ErrRecordingStopInProgress", err)
	}

	receiptID, _, claimGeneration := manager.FinalRecordingReceiptInfo(scope)
	if !manager.ReleaseFinalRecordingReceipt(scope, receiptID, "operation-a", claimGeneration) {
		t.Fatal("claim owner should release its final receipt")
	}
	if _, _, err := manager.StopRecordingWithClaim(context.Background(), scope, "operation-b"); err != nil {
		t.Fatalf("released receipt was not reclaimable: %v", err)
	}
}

func TestInPageStopDoesNotDriveUnscopedRecorder(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	actions, downloadedFiles, driven, err := manager.driveInPageRecordingStop(context.Background(), RecordingStorageScope{})
	if err != nil || driven || actions != nil || downloadedFiles != nil {
		t.Fatalf("unscoped in-page Stop = actions=%v downloads=%v driven=%v err=%v, want ignored request", actions, downloadedFiles, driven, err)
	}
}

func TestRecordingRegistryKeepsRecordersAndSnapshotsIsolatedByBrowserInstance(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	scopeA := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "session-a"}
	scopeB := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 4, RecordingSessionID: "session-b"}

	manager.mu.Lock()
	recorderA := manager.recorderForInstanceLocked("browser-a")
	recorderB := manager.recorderForInstanceLocked("browser-b")
	manager.recordingRuntimeRegistryLocked().activeByInstance["browser-a"] = scopeA
	manager.recordingRuntimeRegistryLocked().activeByInstance["browser-b"] = scopeB
	manager.mu.Unlock()
	if recorderA == recorderB {
		t.Fatal("different browser instances shared one Recorder")
	}
	if !manager.IsRecordingStorageScopeActive(scopeA) || !manager.IsRecordingStorageScopeActive(scopeB) {
		t.Fatal("active runtime scope was not found for its owning browser instance")
	}
	if manager.IsRecordingStorageScopeActive(RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "session-a", RuntimePageID: "other-runtime-page"}) {
		t.Fatal("different runtime page was treated as the same active recording scope")
	}

	manager.mu.Lock()
	manager.publishAuthSnapshotReceiptLocked(scopeA, map[string]any{"cookies": []any{"A"}})
	manager.publishAuthSnapshotReceiptLocked(scopeB, map[string]any{"cookies": []any{"B"}})
	manager.mu.Unlock()
	if state := manager.PeekLastRecordingStorageState(scopeA); state == nil {
		t.Fatal("session A snapshot was hidden by session B recorder")
	}
	if state := manager.PeekLastRecordingStorageState(scopeB); state == nil {
		t.Fatal("session B snapshot was hidden by session A recorder")
	}
	_, receiptID, claimGeneration, err := manager.ClaimRecordingStorageState(scopeA, "operation-a")
	if err != nil || !manager.ReleaseClaimedRecordingStorageState(scopeA, receiptID, "operation-a", claimGeneration) {
		t.Fatalf("matching Capture release failed: receipt=%q generation=%d err=%v", receiptID, claimGeneration, err)
	}
	if state := manager.PeekLastRecordingStorageState(scopeA); state == nil {
		t.Fatal("released session A snapshot should remain available for a later operation")
	}
	if state := manager.PeekLastRecordingStorageState(scopeB); state == nil {
		t.Fatal("discarding session A snapshot affected session B")
	}
}

func TestRecordingAuthSnapshotClaimHonorsTTLAndOwnership(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "snapshot-session"}
	manager.mu.Lock()
	manager.publishAuthSnapshotReceiptLocked(scope, map[string]any{"cookies": []any{"secret"}})
	manager.mu.Unlock()

	state, receiptID, claimGeneration, err := manager.ClaimRecordingStorageState(scope, "operation-a")
	if err != nil || receiptID == "" || claimGeneration == 0 || state == nil {
		t.Fatalf("claim snapshot: state=%v receipt=%q generation=%d err=%v", state, receiptID, claimGeneration, err)
	}
	if _, _, _, err := manager.ClaimRecordingStorageState(scope, "operation-b"); err == nil {
		t.Fatal("different operation consumed an active snapshot claim")
	}
	if _, replayReceiptID, replayGeneration, err := manager.ClaimRecordingStorageState(scope, "operation-a"); err != nil || replayReceiptID != receiptID || replayGeneration != claimGeneration {
		t.Fatalf("same operation did not replay its snapshot claim: receipt=%q err=%v", replayReceiptID, err)
	}

	manager.mu.Lock()
	receipt := manager.recordingRegistry.authBySession[scope.RecordingSessionID]
	receipt.claimedAt = time.Now().UTC().Add(-recordingReceiptClaimTTL - time.Second)
	manager.mu.Unlock()
	if _, _, nextGeneration, err := manager.ClaimRecordingStorageState(scope, "operation-b"); err != nil || nextGeneration <= claimGeneration {
		t.Fatalf("expired claim was not reclaimable: %v", err)
	}

	manager.mu.Lock()
	receipt = manager.recordingRegistry.authBySession[scope.RecordingSessionID]
	receipt.state = recordingReceiptAvailable
	receipt.frozenAt = time.Now().UTC().Add(-recordingReceiptTTL - time.Second)
	manager.cleanupRuntimeReceiptsLocked(time.Now().UTC())
	manager.mu.Unlock()
	if state := manager.PeekLastRecordingStorageState(scope); state != nil {
		t.Fatal("expired snapshot remained readable")
	}
}

func TestRecordingAuthSnapshotLateClaimantCannotAcknowledgeOrReleaseTakeover(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "snapshot-takeover"}
	manager.mu.Lock()
	manager.publishAuthSnapshotReceiptLocked(scope, map[string]any{"cookies": []any{"secret"}})
	manager.mu.Unlock()

	_, receiptID, generationA, err := manager.ClaimRecordingStorageState(scope, "operation-a")
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}
	manager.mu.Lock()
	manager.recordingRegistry.authBySession[scope.RecordingSessionID].claimedAt = time.Now().UTC().Add(-recordingReceiptClaimTTL - time.Second)
	manager.mu.Unlock()
	_, receiptIDB, generationB, err := manager.ClaimRecordingStorageState(scope, "operation-b")
	if err != nil || receiptIDB != receiptID || generationB <= generationA {
		t.Fatalf("claim B takeover = receipt=%q generation=%d err=%v", receiptIDB, generationB, err)
	}
	if manager.AcknowledgeClaimedRecordingStorageState(scope, receiptID, "operation-a", generationA) {
		t.Fatal("expired claimant A acknowledged claimant B's snapshot")
	}
	if manager.ReleaseClaimedRecordingStorageState(scope, receiptID, "operation-a", generationA) {
		t.Fatal("expired claimant A released claimant B's snapshot")
	}
	if state := manager.PeekLastRecordingStorageState(scope); state == nil {
		t.Fatal("late claimant changed the new claim's snapshot")
	}
	if !manager.AcknowledgeClaimedRecordingStorageState(scope, receiptID, "operation-b", generationB) {
		t.Fatal("current claimant B could not acknowledge its snapshot")
	}
	if state := manager.PeekLastRecordingStorageState(scope); state != nil {
		t.Fatal("acknowledged current snapshot remained available")
	}
}

func TestFinalReceiptCarriesRuntimeDriverOperationIdentity(t *testing.T) {
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "in-page-driver", BrowserInstanceID: "instance", RuntimePageID: "page", RuntimeGeneration: "generation"}
	manager := NewManager(&config.Config{}, nil, nil)
	operationID := runtimeStopOperationID(scope)
	manager.mu.Lock()
	manager.publishFinalRecordingReceiptLocked(scope, "https://example.invalid", []models.ScriptAction{{Type: "click", Selector: "#save"}}, nil, json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot"}`), 3, operationID)
	manager.mu.Unlock()
	events := manager.PendingRecordingRuntimeEvents()
	if len(events) != 1 || events[0].OperationID != operationID || events[0].ClaimGeneration != 1 {
		t.Fatalf("final runtime event did not carry its scoped driver identity: %+v", events)
	}
}

func TestRecordingRuntimeEventQueueRetainsSemanticDOMAndExpiredFinalTombstone(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "runtime-event-session", RuntimePageID: "runtime-page", RuntimeGeneration: "runtime-gen", LeaseGeneration: "lease-gen"}
	dom := json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/login","elements":[{"tag":"button","selector":"#login"}]}`)

	manager.mu.Lock()
	manager.recordingRuntimeRegistryLocked().activeByInstance["instance"] = scope
	manager.mu.Unlock()
	manager.publishRecorderDraft(scope, 3, []models.ScriptAction{{Type: "click", Selector: "#login"}}, dom)

	events := manager.PendingRecordingRuntimeEvents()
	if len(events) != 1 || events[0].Kind != "draft_sync" || string(events[0].DOMSnapshot) != string(dom) {
		t.Fatalf("queued draft event = %+v, want semantic DOM snapshot", events)
	}
	manager.AcknowledgeRecordingRuntimeEvent(events[0])
	if events = manager.PendingRecordingRuntimeEvents(); len(events) != 0 {
		t.Fatalf("acknowledged latest draft remained queued: %+v", events)
	}

	manager.mu.Lock()
	manager.publishFinalRecordingReceiptLocked(scope, "https://example.invalid/login", []models.ScriptAction{{Type: "click", Selector: "#login"}}, nil, dom, 4)
	final := manager.recordingRegistry.finalBySession[scope.RecordingSessionID]
	final.frozenAt = time.Now().UTC().Add(-recordingReceiptTTL - time.Second)
	manager.cleanupRuntimeReceiptsLocked(time.Now().UTC())
	manager.mu.Unlock()

	events = manager.PendingRecordingRuntimeEvents()
	if len(events) != 1 || events[0].Kind != "recording_receipt_expired" || events[0].ReceiptID == "" {
		t.Fatalf("expired final receipt was not retained as a tombstone: %+v", events)
	}
	manager.AcknowledgeRecordingRuntimeEvent(events[0])
	if events = manager.PendingRecordingRuntimeEvents(); len(events) != 0 {
		t.Fatalf("expired tombstone remained queued after lifecycle acknowledgement: %+v", events)
	}
}

func TestReleaseDraftRuntimeEventPreservesOtherSessionReceipts(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "precise-release", BrowserInstanceID: "instance-a", RuntimePageID: "page-a", RuntimeGeneration: "runtime-a", LeaseGeneration: "lease-a"}
	draft := RecordingRuntimeEvent{ID: "draft-release-only", Kind: "draft_sync", Scope: scope, SyncRevision: 1}
	manager.mu.Lock()
	registry := manager.recordingRuntimeRegistryLocked()
	registry.draftBySession[scope.RecordingSessionID] = &recordingDraftReceipt{event: draft}
	manager.publishFinalRecordingReceiptLocked(scope, "https://example.invalid", []models.ScriptAction{{Type: "click", Selector: "#save"}}, nil, json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot"}`), 2)
	manager.publishAuthSnapshotReceiptLocked(scope, map[string]any{"cookies": []any{"secret"}})
	registry.startBySession[scope.RecordingSessionID] = &recordingStartReservation{scope: scope, token: "start-token", generation: 1, state: "reserved"}
	manager.mu.Unlock()

	manager.ReleaseRecordingRuntimeEvent(draft)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	registry = manager.recordingRuntimeRegistryLocked()
	if registry.draftBySession[scope.RecordingSessionID] != nil {
		t.Fatal("released draft event remained queued")
	}
	if registry.finalBySession[scope.RecordingSessionID] == nil || registry.authBySession[scope.RecordingSessionID] == nil || registry.startBySession[scope.RecordingSessionID] == nil {
		t.Fatal("releasing a draft event must not discard final, auth, or start runtime state")
	}
}

func TestReleaseStoppedRuntimeEventConsumesOnlyItsFinalReceipt(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "stale-final-release", BrowserInstanceID: "instance-a", RuntimePageID: "page-a", RuntimeGeneration: "runtime-a", LeaseGeneration: "lease-a"}
	manager.mu.Lock()
	registry := manager.recordingRuntimeRegistryLocked()
	manager.publishFinalRecordingReceiptLocked(scope, "https://example.invalid", []models.ScriptAction{{Type: "click", Selector: "#save"}}, nil, json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid","title":"Example","elements":[]}`), 2, "operation-a")
	manager.publishAuthSnapshotReceiptLocked(scope, map[string]any{"cookies": []any{"secret"}})
	receipt := registry.finalBySession[scope.RecordingSessionID]
	if receipt == nil {
		manager.mu.Unlock()
		t.Fatal("final receipt was not created")
	}
	event := RecordingRuntimeEvent{ID: "stale-final-event", Kind: "recording_stopped", Scope: scope, ReceiptID: receipt.receiptID, OperationID: "operation-a", ClaimGeneration: receipt.claimGeneration}
	manager.mu.Unlock()

	manager.ReleaseRecordingRuntimeEvent(event)
	manager.mu.Lock()
	registry = manager.recordingRuntimeRegistryLocked()
	if registry.finalBySession[scope.RecordingSessionID] != nil {
		manager.mu.Unlock()
		t.Fatal("released recording_stopped event retained its final receipt")
	}
	if registry.authBySession[scope.RecordingSessionID] == nil {
		manager.mu.Unlock()
		t.Fatal("event-local final release discarded auth snapshot")
	}
	manager.mu.Unlock()

	manager.ReleaseRecordingRuntimeEvent(RecordingRuntimeEvent{ID: "wrong-final", Kind: "recording_stopped", Scope: scope, ReceiptID: "other-receipt", OperationID: "operation-a", ClaimGeneration: event.ClaimGeneration})
	if pending := manager.PendingRecordingRuntimeEvents(); len(pending) != 0 {
		t.Fatalf("released final receipt reappeared as runtime event: %+v", pending)
	}
}

func TestPendingStoppedRuntimeEventKeepsIdentityAcrossClaimHandoff(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "stable-final-event", BrowserInstanceID: "instance-a", RuntimePageID: "page-a", RuntimeGeneration: "runtime-a", LeaseGeneration: "lease-a"}
	manager.mu.Lock()
	manager.publishFinalRecordingReceiptLocked(scope, "https://example.invalid", []models.ScriptAction{{Type: "click", Selector: "#save"}}, nil, json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid","title":"Example","elements":[]}`), 7)
	manager.mu.Unlock()
	first := manager.PendingRecordingRuntimeEvents()
	if len(first) != 1 || first[0].Kind != "recording_stopped" {
		t.Fatalf("first pending final event = %+v", first)
	}
	manager.mu.Lock()
	manager.recordingRuntimeRegistryLocked().finalBySession[scope.RecordingSessionID].claimGeneration++
	manager.mu.Unlock()
	second := manager.PendingRecordingRuntimeEvents()
	if len(second) != 1 || second[0].ID != first[0].ID {
		t.Fatalf("final event identity changed across claim handoff: first=%+v second=%+v", first, second)
	}
}

func TestReleaseRecordingSessionResourcesClearsOnlyExactScopeAfterDurableTerminal(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "session-cleanup", BrowserInstanceID: "instance-a", RuntimePageID: "page-a", RuntimeGeneration: "runtime-a", LeaseGeneration: "lease-a"}
	other := scope
	other.RecordingSessionID = "other-session"
	other.RuntimePageID = "page-b"
	manager.mu.Lock()
	registry := manager.recordingRuntimeRegistryLocked()
	registry.activeByInstance[scope.BrowserInstanceID] = scope
	registry.draftBySession[scope.RecordingSessionID] = &recordingDraftReceipt{event: RecordingRuntimeEvent{ID: "draft", Kind: "draft_sync", Scope: scope}}
	manager.publishFinalRecordingReceiptLocked(scope, "https://example.invalid", nil, nil, json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot"}`), 1)
	manager.publishAuthSnapshotReceiptLocked(scope, map[string]any{"cookies": []any{"secret"}})
	registry.startBySession[scope.RecordingSessionID] = &recordingStartReservation{scope: scope, token: "token", generation: 1, state: "reserved", done: make(chan struct{})}
	registry.finalBySession[other.RecordingSessionID] = &recordingFinalReceipt{scope: other, state: recordingReceiptAvailable}
	manager.mu.Unlock()

	manager.ReleaseRecordingSessionResources(scope)
	manager.mu.Lock()
	defer manager.mu.Unlock()
	registry = manager.recordingRuntimeRegistryLocked()
	if registry.draftBySession[scope.RecordingSessionID] != nil || registry.finalBySession[scope.RecordingSessionID] != nil || registry.authBySession[scope.RecordingSessionID] != nil || registry.startBySession[scope.RecordingSessionID] != nil {
		t.Fatal("durable session cleanup left same-session runtime state behind")
	}
	if _, active := registry.activeByInstance[scope.BrowserInstanceID]; active {
		t.Fatal("durable session cleanup left active instance lease behind")
	}
	if registry.finalBySession[other.RecordingSessionID] == nil {
		t.Fatal("durable session cleanup crossed into another session")
	}
}

func TestFinalDraftRevisionTracksSemanticContentRatherThanCaptureTime(t *testing.T) {
	recorder := NewRecorder()
	actions := []models.ScriptAction{}
	firstDOM := json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","title":"Login","captured_at":"2026-07-29T00:00:00Z","elements":[]}`)
	secondDOM := json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","title":"Login","captured_at":"2026-07-29T00:00:01Z","elements":[]}`)

	recorder.mu.Lock()
	firstRevision, changed, err := recorder.finalizeRecordingDraftLocked(actions, firstDOM)
	if err != nil || !changed || firstRevision != 1 {
		recorder.mu.Unlock()
		t.Fatalf("initial final draft = revision:%d changed:%v err:%v, want revision 1 changed", firstRevision, changed, err)
	}
	secondRevision, changed, err := recorder.finalizeRecordingDraftLocked(actions, secondDOM)
	if err != nil || changed || secondRevision != firstRevision {
		recorder.mu.Unlock()
		t.Fatalf("capture-time-only final draft = revision:%d changed:%v err:%v, want unchanged", secondRevision, changed, err)
	}
	thirdRevision, changed, err := recorder.finalizeRecordingDraftLocked([]models.ScriptAction{{Type: "click", Selector: "#submit"}}, secondDOM)
	recorder.mu.Unlock()
	if err != nil || !changed || thirdRevision != secondRevision+1 {
		t.Fatalf("semantic final draft = revision:%d changed:%v err:%v, want increment", thirdRevision, changed, err)
	}
}

func TestRecordingLoopOwnershipUsesInstanceRegistryInsteadOfLegacyActivePage(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	pageA := &rod.Page{}
	pageB := &rod.Page{}
	scopeA := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "loop-a", BrowserInstanceID: "instance-a", RuntimePageID: "page-a", RuntimeGeneration: "runtime-a", LeaseGeneration: "lease-a"}
	manager.mu.Lock()
	manager.instances["instance-a"] = &BrowserInstanceRuntime{activePage: pageA}
	manager.instances["instance-b"] = &BrowserInstanceRuntime{activePage: pageB}
	manager.activePage = pageB // Legacy mirror points at another instance.
	manager.recordingRuntimeRegistryLocked().activeByInstance["instance-a"] = scopeA
	stillOwned := manager.recordingLoopOwnsScopeLocked("instance-a", pageA, scopeA)
	manager.mu.Unlock()
	if !stillOwned {
		t.Fatal("instance A recording loop was terminated by instance B legacy activePage")
	}
}

func TestRecordingStartReservationFencesConcurrentDrivers(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "start-session", BrowserInstanceID: "instance-a", RuntimePageID: "page-a", RuntimeGeneration: "runtime-a", LeaseGeneration: "lease-a"}
	firstCtx, running, err := manager.AcquireRecordingStartTarget(t.Context(), scope, "token-a", 1)
	if err != nil || running || firstCtx == nil {
		t.Fatalf("first reservation = ctx:%v running:%v err:%v", firstCtx, running, err)
	}
	if _, _, err := manager.AcquireRecordingStartTarget(t.Context(), scope, "token-b", 2); !errors.Is(err, errRecordingStartReservationInProgress) {
		t.Fatalf("higher-generation reservation should cancel-and-wait, err=%v", err)
	}
	select {
	case <-firstCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("higher generation did not cancel the old driver context")
	}
	manager.ReleaseRecordingStartTarget(scope, "token-a", 1)
	secondCtx, running, err := manager.AcquireRecordingStartTarget(t.Context(), scope, "token-b", 2)
	if err != nil || running || secondCtx == nil {
		t.Fatalf("fenced retry did not acquire cleared reservation: ctx:%v running:%v err:%v", secondCtx, running, err)
	}
	manager.mu.Lock()
	if err := manager.finishRecordingStartTargetLocked(scope, "token-b", 2, true); err != nil {
		manager.mu.Unlock()
		t.Fatalf("mark running reservation: %v", err)
	}
	manager.mu.Unlock()
	if running, err := manager.ReserveRecordingStartTarget(scope, "token-c", 3); err != nil || !running {
		t.Fatalf("running reservation takeover = running:%v err:%v", running, err)
	}
	if err := manager.HeartbeatRecordingStartTarget(scope, "token-b", 2); !errors.Is(err, errRecordingStartReservationInProgress) {
		t.Fatalf("old driver heartbeat should be fenced, err=%v", err)
	}
}

func TestInstanceReleaseCreatesLeaseLostTombstoneBeforeDroppingRuntimeFacts(t *testing.T) {
	manager := NewManager(&config.Config{}, nil, nil)
	scope := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "lease-lost-session", BrowserInstanceID: "instance-a", RuntimePageID: "page-a", RuntimeGeneration: "runtime-a", LeaseGeneration: "lease-a"}
	dom := json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","title":"Orders"}`)
	manager.mu.Lock()
	registry := manager.recordingRuntimeRegistryLocked()
	registry.activeByInstance[scope.BrowserInstanceID] = scope
	registry.draftBySession[scope.RecordingSessionID] = &recordingDraftReceipt{event: RecordingRuntimeEvent{ID: "draft", Kind: "draft_sync", Scope: scope, SyncRevision: 7, Actions: []models.ScriptAction{{Type: "click", Selector: "#save"}}, DOMSnapshot: dom}}
	registry.finalBySession[scope.RecordingSessionID] = &recordingFinalReceipt{scope: scope, state: recordingReceiptAvailable}
	registry.authBySession[scope.RecordingSessionID] = &recordingAuthSnapshotReceipt{scope: scope, state: recordingReceiptAvailable}
	manager.releaseRuntimeReceiptsForInstanceLocked(scope.BrowserInstanceID)
	manager.mu.Unlock()

	events := manager.PendingRecordingRuntimeEvents()
	if len(events) != 1 || events[0].Kind != "runtime_lease_lost" || events[0].SyncRevision != 7 || string(events[0].DOMSnapshot) != string(dom) {
		t.Fatalf("lease-lost tombstone = %+v, want latest draft", events)
	}
	if manager.IsRecordingStorageScopeActive(scope) || manager.PeekLastRecordingStorageState(scope) != nil {
		t.Fatal("instance release retained active runtime data")
	}
	manager.AcknowledgeRecordingRuntimeEvent(events[0])
	if pending := manager.PendingRecordingRuntimeEvents(); len(pending) != 0 {
		t.Fatalf("acknowledged lease-lost tombstone remained queued: %+v", pending)
	}
}

func TestRecordingStorageScopeRejectsDifferentBrowserInstance(t *testing.T) {
	left := RecordingStorageScope{ProjectID: 1, VersionID: 2, PageID: 3, RecordingSessionID: "scope", BrowserInstanceID: "instance-a", RuntimePageID: "page", RuntimeGeneration: "runtime", LeaseGeneration: "lease"}
	right := left
	right.BrowserInstanceID = "instance-b"
	if left.matches(right) {
		t.Fatal("scope match accepted another browser instance")
	}
}

func TestStopRecordingWithStorageScopePublishesActiveRecorderResultUntilAcknowledged(t *testing.T) {
	scope := RecordingStorageScope{
		ProjectID:          1,
		VersionID:          2,
		PageID:             3,
		RecordingSessionID: "42",
	}
	manager := &Manager{
		recorder: &Recorder{
			isRecording:     true,
			startURL:        "https://example.invalid/login",
			actions:         []models.ScriptAction{{Type: "click", Selector: "#submit"}},
			downloadedFiles: []models.DownloadedFile{{FileName: "export.csv"}},
			pages:           map[string]*rod.Page{},
			storageScope:    &scope,
			lastSyncedCount: 1,
		},
	}

	actions, downloadedFiles, err := manager.StopRecordingWithStorageScope(context.Background(), scope)
	if err != nil {
		t.Fatalf("StopRecordingWithStorageScope: %v", err)
	}
	if len(actions) != 1 || actions[0].Selector != "#submit" || len(downloadedFiles) != 1 {
		t.Fatalf("stopped active recorder result = actions:%+v downloads:%+v", actions, downloadedFiles)
	}
	if pendingScope, pending := manager.PendingStoppedRecordingStorageScope(); !pending || pendingScope != scope {
		t.Fatalf("active recorder result was not published as pending: scope=%+v pending=%v", pendingScope, pending)
	}

	retryActions, retryDownloads, err := manager.StopRecordingWithStorageScope(context.Background(), scope)
	if err != nil {
		t.Fatalf("retry StopRecordingWithStorageScope: %v", err)
	}
	if len(retryActions) != 1 || retryActions[0].Selector != "#submit" || len(retryDownloads) != 1 {
		t.Fatalf("retry result = actions:%+v downloads:%+v", retryActions, retryDownloads)
	}

	receiptID, _, claimGeneration := manager.FinalRecordingReceiptInfo(scope)
	if !manager.AcknowledgeFinalRecordingReceipt(scope, receiptID, runtimeStopOperationID(scope), claimGeneration) {
		t.Fatal("matching scoped operation should acknowledge active recorder receipt")
	}
	if _, pending := manager.PendingStoppedRecordingStorageScope(); pending {
		t.Fatal("active recorder result remained pending after acknowledgement")
	}
}

func TestFinalizeStoppedCurrentInstanceRuntimeSwitchesOrClearsLegacyState(t *testing.T) {
	t.Run("switches to another running instance", func(t *testing.T) {
		currentBrowser := &rod.Browser{}
		remainingBrowser := &rod.Browser{}
		manager := &Manager{
			instances: map[string]*BrowserInstanceRuntime{
				"current": {
					instance: &models.BrowserInstance{ID: "current", IsActive: true},
					browser:  currentBrowser,
				},
				"remaining": {
					instance: &models.BrowserInstance{ID: "remaining", IsActive: true},
					browser:  remainingBrowser,
				},
			},
			currentInstanceID: "current",
			browser:           currentBrowser,
			isRunning:         true,
		}

		manager.finalizeStoppedCurrentInstanceRuntimeLocked(context.Background(), "current")

		if _, exists := manager.instances["current"]; exists {
			t.Fatal("stopped current runtime remained in instances")
		}
		if manager.currentInstanceID != "remaining" || manager.browser != remainingBrowser || !manager.isRunning {
			t.Fatalf("current runtime was not switched to remaining instance: id=%q browser=%p running=%v", manager.currentInstanceID, manager.browser, manager.isRunning)
		}
		if manager.instances["current"] != nil {
			t.Fatal("stopped current instance runtime was retained")
		}
	})

	t.Run("clears legacy state when no instance remains", func(t *testing.T) {
		currentBrowser := &rod.Browser{}
		manager := &Manager{
			instances: map[string]*BrowserInstanceRuntime{
				"current": {
					instance: &models.BrowserInstance{ID: "current", IsActive: true},
					browser:  currentBrowser,
				},
			},
			currentInstanceID: "current",
			browser:           currentBrowser,
			isRunning:         true,
		}

		manager.finalizeStoppedCurrentInstanceRuntimeLocked(context.Background(), "current")

		if manager.currentInstanceID != "" || manager.browser != nil || manager.launcher != nil || manager.isRunning || manager.activePage != nil {
			t.Fatalf("legacy state remained after stopping final instance: id=%q browser=%p launcher=%p running=%v page=%p", manager.currentInstanceID, manager.browser, manager.launcher, manager.isRunning, manager.activePage)
		}
		if manager.IsRunning() {
			t.Fatal("IsRunning reported true after final runtime was removed")
		}
	})
}

func TestPersistLaunchedBrowserProfileOwnerRetriesThenStopsOnFailure(t *testing.T) {
	previousWriter := writeBrowserProfileOwnerAfterLaunch
	previousKiller := killLauncherAfterProfileOwnerFailure
	previousDelay := browserProfileOwnerRetryDelay
	t.Cleanup(func() {
		writeBrowserProfileOwnerAfterLaunch = previousWriter
		killLauncherAfterProfileOwnerFailure = previousKiller
		browserProfileOwnerRetryDelay = previousDelay
	})
	browserProfileOwnerRetryDelay = 0

	t.Run("transient write failure retries without stopping the launcher", func(t *testing.T) {
		writeCalls := 0
		launcherKilled := false
		writeBrowserProfileOwnerAfterLaunch = func(string, browserProfileOwner) error {
			writeCalls++
			if writeCalls == 1 {
				return errors.New("marker temporarily locked")
			}
			return nil
		}
		killLauncherAfterProfileOwnerFailure = func(*launcher.Launcher) {
			launcherKilled = true
		}

		if err := persistLaunchedBrowserProfileOwner(context.Background(), launcher.New(), "profile", "ws://127.0.0.1:45678/devtools/browser/owner"); err != nil {
			t.Fatalf("persistLaunchedBrowserProfileOwner: %v", err)
		}
		if writeCalls != 2 {
			t.Fatalf("marker write calls = %d, want 2", writeCalls)
		}
		if launcherKilled {
			t.Fatal("launcher was stopped after a successful retry")
		}
	})

	t.Run("persistent write failure stops the launched browser", func(t *testing.T) {
		writeCalls := 0
		launcherKilled := false
		writeBrowserProfileOwnerAfterLaunch = func(string, browserProfileOwner) error {
			writeCalls++
			return errors.New("marker write denied")
		}
		killLauncherAfterProfileOwnerFailure = func(*launcher.Launcher) {
			launcherKilled = true
		}

		err := persistLaunchedBrowserProfileOwner(context.Background(), launcher.New(), "profile", "ws://127.0.0.1:45678/devtools/browser/owner")
		if err == nil {
			t.Fatal("persistLaunchedBrowserProfileOwner unexpectedly succeeded")
		}
		if writeCalls != browserProfileOwnerWriteAttempts {
			t.Fatalf("marker write calls = %d, want %d", writeCalls, browserProfileOwnerWriteAttempts)
		}
		if !launcherKilled {
			t.Fatal("launcher was not stopped after persistent marker write failure")
		}
	})
}
