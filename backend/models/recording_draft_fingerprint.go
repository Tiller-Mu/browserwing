package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// RecordingDraftFingerprintVersion identifies the semantic-equivalence rules
// shared by the runtime Recorder and the backend RecordingNormalizer.
const RecordingDraftFingerprintVersion = "recording-draft-fingerprint-v1"

// RecordingDraftFingerprintV1 hashes a normalized action trace and a
// restricted DOM snapshot after removing root-level capture metadata. The
// latter deliberately excludes only transient observation time; URL, title,
// element hints, action order, and every other semantic value remain part of
// the recording draft.
func RecordingDraftFingerprintV1(rawActions, rawDOM json.RawMessage) (string, error) {
	actionsJSON, _, err := NormalizeRecordingActionsJSON(rawActions)
	if err != nil {
		return "", fmt.Errorf("normalize recording actions: %w", err)
	}

	var dom any
	if err := json.Unmarshal(rawDOM, &dom); err != nil {
		return "", fmt.Errorf("decode recording dom: %w", err)
	}
	if object, ok := dom.(map[string]any); ok {
		delete(object, "captured_at")
	}
	domJSON, err := json.Marshal(dom)
	if err != nil {
		return "", fmt.Errorf("encode recording dom: %w", err)
	}

	hash := sha256.New()
	for _, part := range []string{RecordingDraftFingerprintVersion, actionsJSON, string(domJSON)} {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
