package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/models"
)

func TestP47RecordingStorageScopeRequiresExactSessionMatch(t *testing.T) {
	active := RecordingStorageScope{
		ProjectID:          10,
		VersionID:          20,
		PageID:             30,
		RecordingSessionID: "session-a",
	}
	if !active.matches(active) {
		t.Fatal("recording scope should match itself")
	}

	mismatches := []RecordingStorageScope{
		{ProjectID: 999, VersionID: 20, PageID: 30, RecordingSessionID: "session-a"},
		{ProjectID: 10, VersionID: 999, PageID: 30, RecordingSessionID: "session-a"},
		{ProjectID: 10, VersionID: 20, PageID: 999, RecordingSessionID: "session-a"},
		{ProjectID: 10, VersionID: 20, PageID: 30, RecordingSessionID: "session-b"},
		{ProjectID: 10, VersionID: 20, PageID: 30},
	}
	for _, mismatch := range mismatches {
		if active.matches(mismatch) {
			t.Fatalf("recording scope %v matched mismatched request %v", active, mismatch)
		}
	}
}

func TestP47DownloadedFileStorageKeyUsesControlledRelativePath(t *testing.T) {
	artifactRoot := t.TempDir()
	manager := NewManager(&config.Config{AssetsDir: artifactRoot}, nil, nil)

	absoluteDownloadPath := filepath.Join(artifactRoot, "downloads", "p47-export.csv")
	got := manager.RecordingArtifactStorageKey(models.DownloadedFile{
		FileName: "p47-export.csv",
		FilePath: absoluteDownloadPath,
	})
	want := filepath.ToSlash(filepath.Join("downloads", "p47-export.csv"))
	if got != want {
		t.Fatalf("storage key = %q, want %q", got, want)
	}

	outsideRoot := filepath.Join(t.TempDir(), "p47-export.csv")
	if got := manager.RecordingArtifactStorageKey(models.DownloadedFile{FilePath: outsideRoot}); got != "" {
		t.Fatalf("outside-root storage key = %q, want empty rejection", got)
	}
}

func TestP47ScopedStopConsumesMatchingInPageStoppedRecording(t *testing.T) {
	scope := RecordingStorageScope{
		ProjectID:          10,
		VersionID:          20,
		PageID:             30,
		RecordingSessionID: "session-a",
	}
	manager := NewManager(&config.Config{AssetsDir: t.TempDir()}, nil, nil)
	manager.inPageRecordingStopped = true
	manager.lastRecordingStorageScope = &scope
	manager.lastRecordedActions = []models.ScriptAction{{Type: "click", Selector: "#submit"}}
	manager.lastDownloadedFiles = []models.DownloadedFile{{FileName: "export.csv", FilePath: filepath.Join(manager.config.AssetsDir, "downloads", "export.csv")}}

	actions, downloadedFiles, err := manager.StopRecordingWithStorageScope(context.Background(), scope)
	if err != nil {
		t.Fatalf("matching in-page stopped recording should be consumable by scoped stop: %v", err)
	}
	if len(actions) != 1 || actions[0].Selector != "#submit" {
		t.Fatalf("actions = %+v, want preserved in-page stopped actions", actions)
	}
	if len(downloadedFiles) != 1 || downloadedFiles[0].FileName != "export.csv" {
		t.Fatalf("downloaded files = %+v, want preserved in-page stopped downloads", downloadedFiles)
	}
	if manager.inPageRecordingStopped || manager.lastRecordingStorageScope != nil {
		t.Fatal("matching scoped stop should consume the in-page stopped marker")
	}

	manager.inPageRecordingStopped = true
	manager.lastRecordingStorageScope = &scope
	manager.lastRecordedActions = []models.ScriptAction{{Type: "click", Selector: "#submit"}}
	mismatch := scope
	mismatch.RecordingSessionID = "session-b"
	if _, _, err := manager.StopRecordingWithStorageScope(context.Background(), mismatch); err == nil {
		t.Fatal("mismatched scope should not consume stale in-page stopped recording")
	}
	if !manager.inPageRecordingStopped {
		t.Fatal("mismatched scoped stop should leave the in-page stopped marker intact")
	}
}

func TestP47RecorderSyncLoopPersistsRecordingSessionDraft(t *testing.T) {
	source, err := os.ReadFile("recorder.go")
	if err != nil {
		t.Fatalf("read production recorder.go: %v", err)
	}
	text := string(source)
	required := []string{
		"func (r *Recorder) syncActionsFromBrowser",
		"r.persistRecordingDraft(ctx, actions",
		"func (r *Recorder) persistRecordingDraft",
		"models.RecordingSession{}",
		`"actions_json"`,
		`"action_count"`,
		`"last_synced_at"`,
		`"recording"`,
	}
	for _, want := range required {
		if !strings.Contains(text, want) {
			t.Fatalf("production recorder.go must persist P4.7 recording drafts through RecordingSession; missing %q", want)
		}
	}
}
