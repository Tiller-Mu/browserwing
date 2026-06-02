package playbot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveEngineDirRejectsInvalidExplicitPathWithoutFallback(t *testing.T) {
	root := t.TempDir()
	defaultEngine := filepath.Join(root, "playbot-engine")
	if err := os.MkdirAll(defaultEngine, 0o755); err != nil {
		t.Fatalf("create default engine dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(defaultEngine, "cli.py"), []byte("# default cli\n"), 0o644); err != nil {
		t.Fatalf("write default cli.py: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to temp root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})

	invalidExplicit := filepath.Join(root, "configured-engine")
	if err := os.MkdirAll(invalidExplicit, 0o755); err != nil {
		t.Fatalf("create invalid explicit engine dir: %v", err)
	}

	got, err := resolveEngineDir(invalidExplicit)
	if err == nil {
		t.Fatalf("resolveEngineDir returned %q, want error for explicit dir without cli.py", got)
	}
	if !strings.Contains(err.Error(), "PLAYBOT_ENGINE_DIR is invalid") {
		t.Fatalf("error = %q, want explicit configuration error", err.Error())
	}
}

func TestResolveEngineDirRejectsInvalidEnvPathWithoutFallback(t *testing.T) {
	root := t.TempDir()
	defaultEngine := filepath.Join(root, "playbot-engine")
	if err := os.MkdirAll(defaultEngine, 0o755); err != nil {
		t.Fatalf("create default engine dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(defaultEngine, "cli.py"), []byte("# default cli\n"), 0o644); err != nil {
		t.Fatalf("write default cli.py: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir to temp root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})

	invalidEnvDir := filepath.Join(root, "env-engine")
	if err := os.MkdirAll(invalidEnvDir, 0o755); err != nil {
		t.Fatalf("create invalid env engine dir: %v", err)
	}
	t.Setenv("PLAYBOT_ENGINE_DIR", invalidEnvDir)

	got, err := resolveEngineDir("")
	if err == nil {
		t.Fatalf("resolveEngineDir returned %q, want error for PLAYBOT_ENGINE_DIR without cli.py", got)
	}
	if !strings.Contains(err.Error(), "PLAYBOT_ENGINE_DIR is invalid") {
		t.Fatalf("error = %q, want explicit configuration error", err.Error())
	}
}
