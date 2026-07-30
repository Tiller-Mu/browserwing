package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeRecordingActionsDropsUntrustedDownloadURLs(t *testing.T) {
	rawSignedURL := "https://user:password@example.invalid/export.csv?signature=raw-secret&format=csv#fragment"
	actions := NormalizeRecordingActions([]ScriptAction{
		{
			Type: "download",
			URL:  rawSignedURL,
			Text: "export.csv",
			Attrs: map[string]string{
				"download_url": rawSignedURL,
			},
		},
		{
			Type: "click",
			Attrs: map[string]string{
				"download_url":           "data:text/plain,raw-secret",
				"download_filename_hint": "data-export.csv",
			},
		},
		{
			Type: "download",
			URL:  "blob:https://example.invalid/opaque-download",
			Text: "blob-export.csv",
		},
	})

	if len(actions) != 2 {
		t.Fatalf("normalized actions = %+v, want unsafe download action removed", actions)
	}
	if got, want := actions[0].URL, "https://example.invalid/export.csv?format=csv&signature=REDACTED"; got != want {
		t.Fatalf("sanitized download URL = %q, want %q", got, want)
	}
	if strings.Contains(actions[0].URL, "raw-secret") || strings.Contains(actions[0].URL, "user:") || strings.Contains(actions[0].URL, "#") {
		t.Fatalf("download action retained untrusted URL data: %+v", actions[0])
	}
	if got := actions[0].Attrs["download_url"]; got != actions[0].URL {
		t.Fatalf("download attrs URL = %q, want normalized action URL %q", got, actions[0].URL)
	}
	if actions[1].Attrs != nil {
		if _, exists := actions[1].Attrs["download_url"]; exists {
			t.Fatalf("click action retained data URL metadata: %+v", actions[1].Attrs)
		}
		if _, exists := actions[1].Attrs["download_filename_hint"]; exists {
			t.Fatalf("click action retained filename without a safe download URL: %+v", actions[1].Attrs)
		}
	}
}

func TestSanitizeRecordingDownloadURLRedactsCompoundCredentialQueryKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "camel case access key", key: "accessKey"},
		{name: "camel case secret key", key: "secretKey"},
		{name: "camel case session token", key: "sessionToken"},
		{name: "AWS access key identifier", key: "AWSAccessKeyId"},
		{name: "mixed case API token", key: "apiTokenValue"},
		{name: "compact API key", key: "apikey"},
		{name: "compact access key", key: "accesskey"},
		{name: "compact secret key", key: "secretkey"},
		{name: "compact session token", key: "sessiontoken"},
		{name: "compact AWS access key identifier", key: "awsaccesskeyid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "https://example.invalid/export.csv?" + tc.key + "=raw-credential&format=csv"
			sanitized := SanitizeRecordingDownloadURL(raw)
			if strings.Contains(sanitized, "raw-credential") {
				t.Fatalf("SanitizeRecordingDownloadURL(%q) retained credential: %q", raw, sanitized)
			}
			if !strings.Contains(sanitized, tc.key+"=REDACTED") {
				t.Fatalf("SanitizeRecordingDownloadURL(%q) = %q, want %s=REDACTED", raw, sanitized, tc.key)
			}
		})
	}

	const safeURL = "https://example.invalid/export.csv?format=csv&keynote=agenda&page=2"
	if got := SanitizeRecordingDownloadURL(safeURL); got != safeURL {
		t.Fatalf("SanitizeRecordingDownloadURL(%q) = %q, want safe query unchanged", safeURL, got)
	}
}

func TestNormalizeRecordingActionsJSONCanonicalizesSparseAndGoZeroValues(t *testing.T) {
	sparse := []byte(`[{"type":"click","selector":"#save"}]`)
	runtime, err := json.Marshal([]ScriptAction{{Type: "click", Selector: "#save"}})
	if err != nil {
		t.Fatalf("marshal runtime action: %v", err)
	}

	normalizedSparse, _, err := NormalizeRecordingActionsJSON(sparse)
	if err != nil {
		t.Fatalf("normalize sparse action: %v", err)
	}
	normalizedRuntime, _, err := NormalizeRecordingActionsJSON(runtime)
	if err != nil {
		t.Fatalf("normalize runtime action: %v", err)
	}
	if normalizedRuntime != normalizedSparse {
		t.Fatalf("normalized runtime action = %s, want sparse form %s", normalizedRuntime, normalizedSparse)
	}

	dom := json.RawMessage(`{"schema_version":1,"kind":"semantic_dom_snapshot","url":"https://example.invalid/save","title":"Save","elements":[]}`)
	sparseFingerprint, err := RecordingDraftFingerprintV1(sparse, dom)
	if err != nil {
		t.Fatalf("fingerprint sparse action: %v", err)
	}
	runtimeFingerprint, err := RecordingDraftFingerprintV1(runtime, dom)
	if err != nil {
		t.Fatalf("fingerprint runtime action: %v", err)
	}
	if runtimeFingerprint != sparseFingerprint {
		t.Fatalf("runtime fingerprint = %s, want sparse fingerprint %s", runtimeFingerprint, sparseFingerprint)
	}
}
