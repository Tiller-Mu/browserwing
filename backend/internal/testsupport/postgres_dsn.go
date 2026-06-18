package testsupport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const PostgresDSNEnv = "BROWSERWING_P46_POSTGRES_DSN"

type postgresDSNConfig struct {
	Database struct {
		DSN string `toml:"dsn"`
	} `toml:"database"`
}

// PostgresDSN returns the DSN used by PostgreSQL-backed contract tests.
// Environment variables remain useful in CI; config.local.toml is for local runs.
func PostgresDSN() (dsn string, source string, err error) {
	if dsn := strings.TrimSpace(os.Getenv(PostgresDSNEnv)); dsn != "" {
		return dsn, PostgresDSNEnv, nil
	}

	configPath := filepath.Join(backendRoot(), "config.local.toml")
	dsn, err = postgresDSNFromConfig(configPath)
	if err != nil {
		return "", "", err
	}
	if dsn != "" {
		return dsn, configPath, nil
	}

	return "", "", nil
}

func postgresDSNFromConfig(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read PostgreSQL test DSN config %s: %w", path, err)
	}

	var cfg postgresDSNConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse PostgreSQL test DSN config %s: %w", path, err)
	}
	return strings.TrimSpace(cfg.Database.DSN), nil
}

func backendRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "."
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
