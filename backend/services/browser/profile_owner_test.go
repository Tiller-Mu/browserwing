package browser

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseBrowserProfileOwnerAcceptsOnlyLocalDevtoolsEndpoints(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantPID int
		wantErr bool
	}{
		{
			name:    "local websocket endpoint",
			raw:     `{"pid":45048,"control_url":"ws://127.0.0.1:59642/devtools/browser/owner"}`,
			wantPID: 45048,
		},
		{
			name:    "localhost http endpoint",
			raw:     `{"pid":45048,"control_url":"http://localhost:59642"}`,
			wantPID: 45048,
		},
		{
			name:    "remote endpoint is rejected",
			raw:     `{"pid":45048,"control_url":"ws://example.invalid:59642/devtools/browser/owner"}`,
			wantErr: true,
		},
		{
			name:    "missing pid is rejected",
			raw:     `{"pid":0,"control_url":"ws://127.0.0.1:59642/devtools/browser/owner"}`,
			wantErr: true,
		},
		{
			name:    "missing port is rejected",
			raw:     `{"pid":45048,"control_url":"ws://127.0.0.1/devtools/browser/owner"}`,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner, err := parseBrowserProfileOwner([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseBrowserProfileOwner(%s) unexpectedly succeeded: %+v", tc.raw, owner)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseBrowserProfileOwner(%s): %v", tc.raw, err)
			}
			if owner.PID != tc.wantPID {
				t.Fatalf("owner.PID = %d, want %d", owner.PID, tc.wantPID)
			}
		})
	}
}

func TestWriteBrowserProfileOwnerPreservesExistingMarkerWhenAtomicReplaceFails(t *testing.T) {
	profileDir := t.TempDir()
	original := browserProfileOwner{
		PID:        45048,
		ControlURL: "ws://127.0.0.1:59642/devtools/browser/original",
	}
	if err := writeBrowserProfileOwner(profileDir, original); err != nil {
		t.Fatalf("write original browser profile owner: %v", err)
	}

	replacement := browserProfileOwner{
		PID:        45049,
		ControlURL: "ws://127.0.0.1:59643/devtools/browser/replacement",
	}
	err := writeBrowserProfileOwnerWithReplace(profileDir, replacement, func(_, _ string) error {
		return errors.New("replace marker failed")
	})
	if err == nil {
		t.Fatal("writeBrowserProfileOwnerWithReplace unexpectedly succeeded")
	}
	got, err := readBrowserProfileOwner(profileDir)
	if err != nil {
		t.Fatalf("read existing browser profile owner after failed replacement: %v", err)
	}
	if got != original {
		t.Fatalf("browser profile owner after failed replacement = %+v, want original %+v", got, original)
	}
	temporaryMarkers, err := filepath.Glob(filepath.Join(profileDir, browserProfileOwnerFileName+".*.tmp"))
	if err != nil {
		t.Fatalf("glob temporary markers: %v", err)
	}
	if len(temporaryMarkers) != 0 {
		t.Fatalf("temporary marker files remain after failed replacement: %v", temporaryMarkers)
	}
	if _, err := os.Stat(browserProfileOwnerMarkerPath(profileDir)); err != nil {
		t.Fatalf("existing marker disappeared after failed replacement: %v", err)
	}
}

func TestBrowserProfileOwnerDevtoolsRespondingAcceptsMicrosoftEdge(t *testing.T) {
	var controlURL string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/version" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"Browser":"Edg/140.0.0.0","webSocketDebuggerUrl":"` + controlURL + `"}`))
	}))
	defer server.Close()

	controlURL = strings.Replace(server.URL, "http://", "ws://", 1) + "/devtools/browser/edge-owner"
	owner := browserProfileOwner{
		PID:        45048,
		ControlURL: controlURL,
	}
	if !browserProfileOwnerDevtoolsResponding(context.Background(), owner) {
		t.Fatal("browserProfileOwnerDevtoolsResponding rejected a valid local Microsoft Edge DevTools endpoint")
	}
}

func TestBrowserProfileOwnerDevtoolsRespondingRequiresExactBrowserWebSocketIdentity(t *testing.T) {
	var reportedWebSocketURL string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/json/version" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"Browser":"Chrome/140.0.0.0","webSocketDebuggerUrl":"` + reportedWebSocketURL + `"}`))
	}))
	defer server.Close()

	controlURL := strings.Replace(server.URL, "http://", "ws://", 1)
	owner := browserProfileOwner{
		PID:        45048,
		ControlURL: controlURL + "/devtools/browser/original-owner",
	}

	reportedWebSocketURL = controlURL + "/devtools/browser/reused-port-different-browser"
	if browserProfileOwnerDevtoolsResponding(context.Background(), owner) {
		t.Fatal("browserProfileOwnerDevtoolsResponding accepted a different browser WebSocket path on the same host and port")
	}

	reportedWebSocketURL = owner.ControlURL
	if !browserProfileOwnerDevtoolsResponding(context.Background(), owner) {
		t.Fatal("browserProfileOwnerDevtoolsResponding rejected an exact browser WebSocket identity")
	}

	legacyHTTPOwner := browserProfileOwner{PID: owner.PID, ControlURL: server.URL}
	if browserProfileOwnerDevtoolsResponding(context.Background(), legacyHTTPOwner) {
		t.Fatal("browserProfileOwnerDevtoolsResponding accepted a legacy HTTP marker without browser identity")
	}
}
