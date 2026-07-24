package browser

import (
	"context"
	"errors"
	"testing"

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
		inPageRecordingStopped:    true,
		lastRecordingStorageScope: &want,
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
	manager := &Manager{
		inPageRecordingStopped:    true,
		lastRecordingStorageScope: &scope,
		lastRecordedActions:       []models.ScriptAction{{Type: "click", Selector: "#login"}},
	}

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

	manager.AcknowledgeInPageStoppedRecording(scope)
	if _, pending := manager.PendingStoppedRecordingStorageScope(); pending {
		t.Fatal("stopped in-page recording remained pending after acknowledgement")
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

	manager.AcknowledgeInPageStoppedRecording(scope)
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
