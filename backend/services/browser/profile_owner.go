package browser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const browserProfileOwnerFileName = ".browserwing-profile-owner.json"

type browserProfileOwner struct {
	PID        int    `json:"pid"`
	ControlURL string `json:"control_url"`
}

func browserProfileOwnerMarkerPath(userDataDir string) string {
	return filepath.Join(userDataDir, browserProfileOwnerFileName)
}

func parseBrowserProfileOwner(raw []byte) (browserProfileOwner, error) {
	var owner browserProfileOwner
	if err := json.Unmarshal(raw, &owner); err != nil {
		return browserProfileOwner{}, fmt.Errorf("decode browser profile owner marker: %w", err)
	}
	if owner.PID <= 0 {
		return browserProfileOwner{}, fmt.Errorf("browser profile owner PID is invalid")
	}
	parsed, err := url.Parse(strings.TrimSpace(owner.ControlURL))
	if err != nil {
		return browserProfileOwner{}, fmt.Errorf("parse browser profile owner control URL: %w", err)
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "http" {
		return browserProfileOwner{}, fmt.Errorf("browser profile owner control URL scheme is invalid")
	}
	if !isLocalDevtoolsHost(parsed.Hostname()) {
		return browserProfileOwner{}, fmt.Errorf("browser profile owner control URL must be local")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return browserProfileOwner{}, fmt.Errorf("browser profile owner control URL port is invalid")
	}
	return owner, nil
}

func isLocalDevtoolsHost(host string) bool {
	normalized := strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	return normalized == "127.0.0.1" || normalized == "::1" || normalized == "localhost"
}

func readBrowserProfileOwner(userDataDir string) (browserProfileOwner, error) {
	raw, err := os.ReadFile(browserProfileOwnerMarkerPath(userDataDir))
	if err != nil {
		return browserProfileOwner{}, err
	}
	return parseBrowserProfileOwner(raw)
}

func writeBrowserProfileOwner(userDataDir string, owner browserProfileOwner) error {
	return writeBrowserProfileOwnerWithReplace(userDataDir, owner, os.Rename)
}

func writeBrowserProfileOwnerWithReplace(userDataDir string, owner browserProfileOwner, replace func(string, string) error) error {
	data, err := json.Marshal(owner)
	if err != nil {
		return fmt.Errorf("encode browser profile owner marker: %w", err)
	}
	if _, err := parseBrowserProfileOwner(data); err != nil {
		return err
	}
	temporaryFile, err := os.CreateTemp(userDataDir, browserProfileOwnerFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary browser profile owner marker: %w", err)
	}
	temporaryPath := temporaryFile.Name()
	closed := false
	cleanupTemporaryFile := true
	defer func() {
		if !closed {
			_ = temporaryFile.Close()
		}
		if cleanupTemporaryFile {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporaryFile.Chmod(0o600); err != nil {
		return fmt.Errorf("set temporary browser profile owner marker permissions: %w", err)
	}
	if _, err := temporaryFile.Write(data); err != nil {
		return fmt.Errorf("write temporary browser profile owner marker: %w", err)
	}
	if err := temporaryFile.Sync(); err != nil {
		return fmt.Errorf("sync temporary browser profile owner marker: %w", err)
	}
	if err := temporaryFile.Close(); err != nil {
		return fmt.Errorf("close temporary browser profile owner marker: %w", err)
	}
	closed = true

	if err := replace(temporaryPath, browserProfileOwnerMarkerPath(userDataDir)); err != nil {
		return fmt.Errorf("atomically replace browser profile owner marker: %w", err)
	}
	cleanupTemporaryFile = false
	return nil
}

func browserProfileOwnerDevtoolsEndpoint(owner browserProfileOwner) (string, error) {
	parsed, err := url.Parse(owner.ControlURL)
	if err != nil {
		return "", err
	}
	parsed.Scheme = "http"
	parsed.Path = "/json/version"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}
