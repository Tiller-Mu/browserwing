package browser

import (
	"context"
	"encoding/json"
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

func TestP47ScopedStopKeepsMatchingInPageStoppedRecordingUntilAcknowledged(t *testing.T) {
	scope := RecordingStorageScope{
		ProjectID:          10,
		VersionID:          20,
		PageID:             30,
		RecordingSessionID: "session-a",
	}
	manager := NewManager(&config.Config{AssetsDir: t.TempDir()}, nil, nil)
	manager.recordingRegistry.finalBySession[scope.RecordingSessionID] = &recordingFinalReceipt{receiptID: "p47-final", scope: scope, actions: []models.ScriptAction{{Type: "click", Selector: "#submit"}}, downloadedFiles: []models.DownloadedFile{{FileName: "export.csv", FilePath: filepath.Join(manager.config.AssetsDir, "downloads", "export.csv")}}, state: recordingReceiptAvailable}

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
	if _, pending := manager.PendingStoppedRecordingStorageScope(); !pending {
		t.Fatal("matching scoped stop should retain the runtime receipt until persistence acknowledgement")
	}
	receiptID, _, claimGeneration := manager.FinalRecordingReceiptInfo(scope)
	if !manager.AcknowledgeFinalRecordingReceipt(scope, receiptID, runtimeStopOperationID(scope), claimGeneration) {
		t.Fatal("matching scoped operation should acknowledge final receipt")
	}
	if _, pending := manager.PendingStoppedRecordingStorageScope(); pending {
		t.Fatal("matching scoped stop should clear the runtime receipt after acknowledgement")
	}

	manager.recordingRegistry.finalBySession[scope.RecordingSessionID] = &recordingFinalReceipt{receiptID: "p47-final-next", scope: scope, actions: []models.ScriptAction{{Type: "click", Selector: "#submit"}}, state: recordingReceiptAvailable}
	mismatch := scope
	mismatch.RecordingSessionID = "session-b"
	if _, _, err := manager.StopRecordingWithStorageScope(context.Background(), mismatch); err == nil {
		t.Fatal("mismatched scope should not consume stale in-page stopped recording")
	}
	if _, pending := manager.PendingStoppedRecordingStorageScope(); !pending {
		t.Fatal("mismatched scoped stop should leave the runtime receipt intact")
	}
}

func TestP47RecordingStorageSnapshotKeepsScopeUntilAcknowledged(t *testing.T) {
	scopeA := RecordingStorageScope{
		ProjectID:          10,
		VersionID:          20,
		PageID:             30,
		RecordingSessionID: "session-a",
	}
	scopeB := scopeA
	scopeB.PageID = 31
	scopeB.RecordingSessionID = "session-b"
	manager := NewManager(&config.Config{}, nil, nil)
	manager.mu.Lock()
	manager.publishAuthSnapshotReceiptLocked(scopeA, map[string]any{"cookies": []any{map[string]any{"name": "session-a"}}})
	manager.publishAuthSnapshotReceiptLocked(scopeB, map[string]any{"cookies": []any{map[string]any{"name": "session-b"}}})
	manager.mu.Unlock()
	if state := manager.PeekLastRecordingStorageState(scopeA); state == nil || stringFromStorageState(state, "session-a") == "" {
		t.Fatal("starting another scoped recording cleared session A snapshot")
	}
	if state := manager.PeekLastRecordingStorageState(scopeB); state == nil || stringFromStorageState(state, "session-b") == "" {
		t.Fatal("session B snapshot was not retained")
	}

	mismatch := scopeA
	mismatch.RecordingSessionID = "session-c"
	if _, receiptID, claimGeneration, err := manager.ClaimRecordingStorageState(scopeA, "snapshot-owner"); err != nil || manager.AcknowledgeClaimedRecordingStorageState(mismatch, receiptID, "snapshot-owner", claimGeneration) {
		t.Fatalf("mismatched acknowledgement must not consume snapshot: err=%v", err)
	}
	if state := manager.PeekLastRecordingStorageState(scopeA); state == nil {
		t.Fatal("mismatched acknowledgement cleared session A snapshot")
	}

	_, receiptID, claimGeneration, err := manager.ClaimRecordingStorageState(scopeA, "snapshot-owner")
	if err != nil || !manager.AcknowledgeClaimedRecordingStorageState(scopeA, receiptID, "snapshot-owner", claimGeneration) {
		t.Fatalf("matching acknowledgement failed: receipt=%q generation=%d err=%v", receiptID, claimGeneration, err)
	}
	if state := manager.PeekLastRecordingStorageState(scopeA); state != nil {
		t.Fatalf("acknowledged session A snapshot remained available: %v", state)
	}
	if state := manager.PeekLastRecordingStorageState(scopeB); state == nil {
		t.Fatal("acknowledging session A cleared session B snapshot")
	}
}

func TestP47MergeRecordingStorageSnapshotsKeepsTargetAndAllOriginsDeterministically(t *testing.T) {
	targetURL := "https://app.example.invalid/orders"
	appState := map[string]any{
		"schema_version": 1,
		"kind":           "browser_storage_state",
		"captured_url":   "https://app.example.invalid/account",
		"captured_at":    "2026-07-24T10:00:00Z",
		"cookies": []map[string]any{
			{"name": "app_session", "domain": "app.example.invalid", "path": "/", "value": "app-token"},
		},
		"origins": []map[string]any{
			{"origin": "https://app.example.invalid", "local_storage": []any{map[string]any{"name": "tenant", "value": "alpha"}}},
		},
		"extensions": map[string]any{},
	}
	idpState := map[string]any{
		"schema_version": 1,
		"kind":           "browser_storage_state",
		"captured_url":   "https://login.example.invalid/continue",
		"captured_at":    "2026-07-24T10:00:01Z",
		"cookies": []map[string]any{
			{"name": "idp_session", "domain": "login.example.invalid", "path": "/", "value": "idp-token"},
		},
		"origins": []map[string]any{
			{"origin": "https://login.example.invalid", "session_storage": []any{map[string]any{"name": "relay", "value": "one-time"}}},
		},
		"extensions": map[string]any{},
	}

	mergedFromReversedPages := mergeRecordingStorageSnapshots(targetURL, nil, idpState, appState)
	mergedFromForwardPages := mergeRecordingStorageSnapshots(targetURL, nil, appState, idpState)
	if got, want := marshalRecordingStorageState(t, mergedFromReversedPages), marshalRecordingStorageState(t, mergedFromForwardPages); got != want {
		t.Fatalf("snapshot merge depended on page iteration order:\nreversed=%s\nforward=%s", got, want)
	}
	if got := recordingStorageString(mergedFromReversedPages["captured_url"]); got != "https://app.example.invalid/account" {
		t.Fatalf("merged captured_url = %q, want target-origin page", got)
	}
	if origins := recordingStorageOrigins(mergedFromReversedPages); len(origins) != 2 || origins[0] != "https://app.example.invalid" || origins[1] != "https://login.example.invalid" {
		t.Fatalf("merged origins = %v, want app and identity-provider storage", origins)
	}
	if cookies := recordingStorageValues(mergedFromReversedPages["cookies"]); len(cookies) != 2 {
		t.Fatalf("merged cookies = %v, want both page snapshots", cookies)
	}
}

func TestP47MergeRecordingStorageSnapshotsPrefersPrimaryTabSessionStorage(t *testing.T) {
	targetURL := "https://app.example.invalid/orders"
	primary := map[string]any{
		"schema_version": 1,
		"kind":           "browser_storage_state",
		"captured_url":   "https://app.example.invalid/sso/callback",
		"captured_at":    "2026-07-24T10:00:00Z",
		"cookies":        []map[string]any{},
		"origins": []map[string]any{
			{"origin": "https://app.example.invalid", "session_storage": []any{map[string]any{"name": "tab", "value": "main"}}},
		},
		"extensions": map[string]any{},
	}
	popup := map[string]any{
		"schema_version": 1,
		"kind":           "browser_storage_state",
		"captured_url":   "https://app.example.invalid/account",
		"captured_at":    "2026-07-24T10:00:01Z",
		"cookies":        []map[string]any{},
		"origins": []map[string]any{
			{"origin": "https://app.example.invalid", "session_storage": []any{map[string]any{"name": "tab", "value": "popup"}}},
		},
		"extensions": map[string]any{},
	}

	mergedFromForwardPages := mergeRecordingStorageSnapshots(targetURL, primary, popup)
	mergedFromReversedPages := mergeRecordingStorageSnapshots(targetURL, primary, popup)
	if got, want := marshalRecordingStorageState(t, mergedFromForwardPages), marshalRecordingStorageState(t, mergedFromReversedPages); got != want {
		t.Fatalf("primary-tab merge depended on non-primary page ordering:\nforward=%s\nreversed=%s", got, want)
	}
	if got := recordingStorageString(mergedFromForwardPages["captured_url"]); got != "https://app.example.invalid/sso/callback" {
		t.Fatalf("merged captured_url = %q, want primary tab URL", got)
	}
	origins := recordingStorageObjects(mergedFromForwardPages["origins"])
	if len(origins) != 1 {
		t.Fatalf("merged origins = %v, want one same-origin state", origins)
	}
	sessionStorage := recordingStorageObjects(origins[0]["session_storage"])
	if len(sessionStorage) != 1 || recordingStorageString(sessionStorage[0]["value"]) != "main" {
		t.Fatalf("merged session storage = %v, want primary tab state", sessionStorage)
	}
}

func TestP47SanitizeRecordingDownloadURLAndRetainFileName(t *testing.T) {
	longQuery := strings.Repeat("x", 2050)
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "redacts signatures and strips credentials fragments",
			raw:  "https://alice:password@example.invalid/reports/export.csv?foo=1&X-Amz-Signature=actual&sig=again#fragment",
			want: "https://example.invalid/reports/export.csv?X-Amz-Signature=REDACTED&foo=1&sig=REDACTED",
		},
		{
			name: "retains safe query",
			raw:  "https://example.invalid/reports/export.csv?format=csv&page=1",
			want: "https://example.invalid/reports/export.csv?format=csv&page=1",
		},
		{
			name: "rejects data URL",
			raw:  "data:text/plain,secret-download-content",
			want: "",
		},
		{
			name: "rejects non HTTP URL",
			raw:  "ftp://example.invalid/export.csv",
			want: "",
		},
		{
			name: "drops oversized query rather than truncating it",
			raw:  "https://example.invalid/reports/export.csv?payload=" + longQuery,
			want: "https://example.invalid/reports/export.csv",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := models.SanitizeRecordingDownloadURL(tc.raw); got != tc.want {
				t.Fatalf("SanitizeRecordingDownloadURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}

	action := projectDownloadAnalysisAction("https://example.invalid/reports/export.csv?signature=test", "export.csv")
	if action.Type != "download" || action.URL != "https://example.invalid/reports/export.csv?signature=REDACTED" || action.Text != "export.csv" {
		t.Fatalf("download analysis action = %+v, want sanitized URL and file name", action)
	}
	if len(action.Attrs) != 0 {
		t.Fatalf("download analysis attrs = %v, want no GUID/frame metadata", action.Attrs)
	}
}

func TestP47MergedDownloadAnalysisActionSurvivesSameMillisecondClick(t *testing.T) {
	timestamp := int64(1720000000000)
	merged := mergeRecordedActions(
		[]models.ScriptAction{{Type: "click", Timestamp: timestamp, Selector: "#export"}},
		[]models.ScriptAction{{
			Type:      "download",
			Timestamp: timestamp,
			URL:       "https://example.invalid/reports/export.csv",
			Text:      "export.csv",
		}},
	)
	if len(merged) != 2 {
		t.Fatalf("merged actions = %+v, want click and download analysis actions", merged)
	}
	if merged[0].Type != "click" || merged[1].Type != "download" {
		t.Fatalf("merged action order = %+v, want click then download", merged)
	}

	distinctDownloads := mergeRecordedActions(
		[]models.ScriptAction{{
			Type:      "download",
			Timestamp: timestamp,
			URL:       "https://example.invalid/reports/export.csv",
			Text:      "first-export.csv",
		}},
		[]models.ScriptAction{{
			Type:      "download",
			Timestamp: timestamp,
			URL:       "https://example.invalid/reports/export.csv",
			Text:      "second-export.csv",
		}},
	)
	if len(distinctDownloads) != 2 {
		t.Fatalf("same-millisecond download events were deduplicated: %+v", distinctDownloads)
	}
}

func marshalRecordingStorageState(t *testing.T, state map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal recording storage state: %v", err)
	}
	return string(encoded)
}

func recordingStorageOrigins(state map[string]any) []string {
	origins := make([]string, 0)
	for _, raw := range recordingStorageValues(state["origins"]) {
		origin, _ := raw.(map[string]any)
		if value := recordingStorageString(origin["origin"]); value != "" {
			origins = append(origins, value)
		}
	}
	return origins
}

func TestP47RecorderScriptMarksOnlyExplicitDownloadLinks(t *testing.T) {
	source, err := os.ReadFile("scripts/recorder.js")
	if err != nil {
		t.Fatalf("read recorder script: %v", err)
	}
	script := string(source)
	if strings.Contains(script, "target.closest('a[href]')") {
		t.Fatal("ordinary anchors must not be classified as download links")
	}
	for _, required := range []string{
		"target.closest('a[download][href]')",
		"sanitizeRecordingDownloadURL",
		"download_url",
		"download_filename_hint",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("recorder download-link contract missing %q", required)
		}
	}
}

func stringFromStorageState(state map[string]any, wantName string) string {
	cookies, _ := state["cookies"].([]any)
	for _, item := range cookies {
		cookie, _ := item.(map[string]any)
		if cookie["name"] == wantName {
			return wantName
		}
	}
	return ""
}

func TestP476RecorderSyncLoopDoesNotWriteRecordingSession(t *testing.T) {
	source, err := os.ReadFile("recorder.go")
	if err != nil {
		t.Fatalf("read production recorder.go: %v", err)
	}
	text := string(source)
	if !strings.Contains(text, "func (r *Recorder) syncActionsFromBrowser") {
		t.Fatal("recorder.go is missing the action collection loop")
	}
	for _, forbidden := range []string{"persistRecordingDraft", "models.RecordingSession{}", ".GormDB()"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Recorder must not write recording business state; found %q", forbidden)
		}
	}
}
