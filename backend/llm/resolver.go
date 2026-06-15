package llm

import (
	"fmt"
	"strings"

	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/storage"
)

const (
	ErrConfigNotFound          = "llm_config_not_found"
	ErrConfigDisabled          = "llm_config_disabled"
	ErrConfigIncomplete        = "llm_config_incomplete"
	ErrConfigMissingDefault    = "llm_config_missing_default"
	ErrConfigSecretUnavailable = "llm_config_secret_unavailable"
)

type ConfigError struct {
	Code string
}

func (e *ConfigError) Error() string {
	switch e.Code {
	case ErrConfigNotFound:
		return "LLM config not found"
	case ErrConfigDisabled:
		return "LLM config is disabled"
	case ErrConfigIncomplete:
		return "LLM config is incomplete"
	case ErrConfigMissingDefault:
		return "Default LLM config is missing"
	case ErrConfigSecretUnavailable:
		return "LLM config secret is unavailable"
	default:
		return "LLM config is unavailable"
	}
}

// ResolveRuntimeConfig is the shared availability gate for all runtime AI calls.
func ResolveRuntimeConfig(store storage.Store, id string) (*models.LLMConfigModel, error) {
	if store == nil {
		return nil, &ConfigError{Code: ErrConfigMissingDefault}
	}
	id = strings.TrimSpace(id)
	var (
		cfg *models.LLMConfigModel
		err error
	)
	if id != "" {
		cfg, err = store.GetLLMConfig(id)
		if err != nil {
			return nil, classifyLLMLoadError(err, ErrConfigNotFound)
		}
	} else {
		cfg, err = store.GetDefaultLLMConfig()
		if err != nil {
			return nil, classifyLLMLoadError(err, ErrConfigMissingDefault)
		}
	}
	if cfg == nil {
		if id != "" {
			return nil, &ConfigError{Code: ErrConfigNotFound}
		}
		return nil, &ConfigError{Code: ErrConfigMissingDefault}
	}
	if !cfg.IsActive {
		return nil, &ConfigError{Code: ErrConfigDisabled}
	}
	if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.Model) == "" || strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, &ConfigError{Code: ErrConfigIncomplete}
	}
	return cfg, nil
}

func classifyLLMLoadError(err error, fallback string) error {
	if err == nil {
		return nil
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "not found") || strings.Contains(text, "missing") || strings.Contains(text, "default") {
		return &ConfigError{Code: fallback}
	}
	if strings.Contains(text, "decrypt") || strings.Contains(text, "cipher") || strings.Contains(text, "secret") || strings.Contains(text, "api key") {
		return &ConfigError{Code: ErrConfigSecretUnavailable}
	}
	return fmt.Errorf("%w: %v", &ConfigError{Code: fallback}, err)
}
