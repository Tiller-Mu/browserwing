package storage

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/browserwing/browserwing/builtin"
	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/models"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

const llmAPIKeyKeyID = "local-llm-api-key-v1"

type PostgresStore struct {
	db           *gorm.DB
	llmKey       []byte
	llmKeySource string
}

type mcpServiceToolsRow struct {
	ServiceID string                     `gorm:"primaryKey;size:128"`
	Tools     []models.MCPDiscoveredTool `gorm:"type:jsonb;serializer:json"`
	UpdatedAt time.Time
}

func (mcpServiceToolsRow) TableName() string {
	return "mcp_service_tools"
}

var _ Store = (*PostgresStore)(nil)

func OpenPostgresStore(ctx context.Context, cfg *config.Config) (Store, func() error, error) {
	store, err := openPostgresStore(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return store, store.Close, nil
}

func NewStore(ctx context.Context, cfg *config.Config) (Store, func() error, error) {
	return OpenPostgresStore(ctx, cfg)
}

func openPostgresStore(ctx context.Context, cfg *config.Config) (*PostgresStore, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if cfg.Database == nil {
		return nil, fmt.Errorf("database config is required")
	}
	if strings.ToLower(strings.TrimSpace(cfg.Database.Type)) != "postgres" {
		return nil, fmt.Errorf("database.type must be postgres")
	}
	if !dsnTargetsPlayBot(cfg.Database.DSN) {
		return nil, fmt.Errorf("database dsn must target database name exactly PlayBot")
	}

	db, err := gorm.Open(postgres.Open(cfg.Database.DSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL PlayBot store: %w", err)
	}
	if err := pingGorm(ctx, db); err != nil {
		_ = closeGorm(db)
		return nil, err
	}

	key, source, err := resolveLLMAPIKeyEncryptionKey(cfg, configHasLLMAPIKey(cfg))
	if err != nil {
		_ = closeGorm(db)
		return nil, err
	}

	store := &PostgresStore{db: db, llmKey: key, llmKeySource: source}
	if err := store.autoMigrate(); err != nil {
		_ = closeGorm(db)
		return nil, err
	}
	if err := store.seedFromConfig(cfg); err != nil {
		_ = closeGorm(db)
		return nil, err
	}
	return store, nil
}

func (s *PostgresStore) GormDB() *gorm.DB {
	return s.db
}

func (s *PostgresStore) Close() error {
	return closeGorm(s.db)
}

func closeGorm(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func pingGorm(ctx context.Context, db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("open PostgreSQL sql handle: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("connect PostgreSQL PlayBot: %w", err)
	}
	return nil
}

func (s *PostgresStore) autoMigrate() error {
	if err := s.db.AutoMigrate(
		&models.Script{},
		&models.ScriptExecution{},
		&models.LLMConfigModel{},
		&models.BrowserConfig{},
		&models.BrowserInstance{},
		&models.CookieStore{},
		&models.RecordingConfig{},
		&models.Prompt{},
		&models.AgentSession{},
		&models.AgentMessage{},
		&models.ToolConfig{},
		&models.MCPService{},
		&mcpServiceToolsRow{},
		&models.User{},
		&models.ApiKey{},
		&models.ScheduledTask{},
		&models.TaskExecution{},
	); err != nil {
		return err
	}
	return models.AutoMigrate(s.db)
}

func (s *PostgresStore) seedFromConfig(cfg *config.Config) error {
	if err := s.importLLMConfigs(cfg); err != nil {
		return err
	}
	if err := s.CheckAndUpdateSystemPrompts(); err != nil {
		return err
	}
	if err := s.seedBuiltinScripts(); err != nil {
		return err
	}
	if err := s.seedDefaultBrowserInstance(cfg); err != nil {
		return err
	}
	if cfg.Auth != nil && cfg.Auth.Enabled {
		if err := s.seedDefaultUser(cfg); err != nil {
			return err
		}
	}
	return nil
}

func dsnTargetsPlayBot(dsn string) bool {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return false
	}
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return false
		}
		name, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
		return err == nil && name == "PlayBot"
	}
	for _, field := range strings.Fields(dsn) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key != "dbname" {
			continue
		}
		return strings.Trim(value, `"'`) == "PlayBot"
	}
	return false
}

func configHasLLMAPIKey(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	if cfg.LLM != nil && strings.TrimSpace(cfg.LLM.APIKey) != "" {
		return true
	}
	for _, llmCfg := range cfg.LLMs {
		if strings.TrimSpace(llmCfg.APIKey) != "" {
			return true
		}
	}
	return false
}

func resolveLLMAPIKeyEncryptionKey(cfg *config.Config, required bool) ([]byte, string, error) {
	raw := strings.TrimSpace(os.Getenv("BROWSERWING_LLM_API_KEY_ENCRYPTION_KEY"))
	source := "env"
	if raw == "" {
		source = "config"
		if cfg != nil && cfg.Security != nil {
			raw = strings.TrimSpace(cfg.Security.LLMAPIKeyEncryptionKey)
		}
	}
	if raw == "" {
		if required {
			return nil, "", fmt.Errorf("llm_api_key_encryption_key is required when persisting LLM API keys")
		}
		return nil, "", nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		if required {
			return nil, "", fmt.Errorf("llm_api_key_encryption_key must be base64 encoded 32 bytes")
		}
		return nil, "", nil
	}
	return key, source, nil
}

func (s *PostgresStore) encryptLLMAPIKey(value string) (ciphertext, nonce string, err error) {
	if value == "" {
		return "", "", nil
	}
	if len(s.llmKey) != 32 {
		return "", "", fmt.Errorf("llm_api_key_encryption_key is required when persisting LLM API keys")
	}
	block, err := aes.NewCipher(s.llmKey)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	nonceBytes := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonceBytes); err != nil {
		return "", "", err
	}
	sealed := gcm.Seal(nil, nonceBytes, []byte(value), []byte(llmAPIKeyKeyID))
	return base64.StdEncoding.EncodeToString(sealed), base64.StdEncoding.EncodeToString(nonceBytes), nil
}

func (s *PostgresStore) decryptLLMAPIKey(config *models.LLMConfigModel) error {
	if config == nil || config.APIKeyCiphertext == "" {
		return nil
	}
	if len(s.llmKey) != 32 {
		return fmt.Errorf("llm_api_key_encryption_key is required to decrypt LLM API key")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(config.APIKeyCiphertext)
	if err != nil {
		return fmt.Errorf("decrypt LLM API key: invalid ciphertext")
	}
	nonce, err := base64.StdEncoding.DecodeString(config.APIKeyNonce)
	if err != nil {
		return fmt.Errorf("decrypt LLM API key: invalid nonce")
	}
	block, err := aes.NewCipher(s.llmKey)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(config.APIKeyKeyID))
	if err != nil {
		return fmt.Errorf("decrypt LLM API key failed")
	}
	config.APIKey = string(plain)
	return nil
}

func (s *PostgresStore) importLLMConfigs(cfg *config.Config) error {
	llms := cfg.ListLLMs()
	for i := range llms {
		llmCfg := llms[i]
		if strings.TrimSpace(llmCfg.Name) == "" {
			llmCfg.Name = "default"
		}
		var existing models.LLMConfigModel
		err := s.db.Where("name = ?", llmCfg.Name).First(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		model := &models.LLMConfigModel{
			ID:        llmCfg.Name,
			Name:      llmCfg.Name,
			Provider:  llmCfg.Provider,
			APIKey:    llmCfg.APIKey,
			Model:     llmCfg.Model,
			BaseURL:   llmCfg.BaseURL,
			IsDefault: i == 0 || llmCfg.Name == "default",
			IsActive:  true,
		}
		if err := s.SaveLLMConfig(model); err != nil {
			return fmt.Errorf("persist LLM config %s: %w", llmCfg.Name, err)
		}
	}
	return nil
}

func (s *PostgresStore) seedBuiltinScripts() error {
	for _, script := range builtin.GetBuiltinScripts() {
		if !strings.HasPrefix(script.ID, "builtin-") {
			continue
		}
		existing, err := s.GetScript(script.ID)
		if err == nil && existing != nil {
			continue
		}
		now := time.Now()
		script.CreatedAt = now
		script.UpdatedAt = now
		if err := s.SaveScript(&script); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) seedDefaultBrowserInstance(cfg *config.Config) error {
	if _, err := s.GetDefaultBrowserInstance(); err == nil {
		return nil
	}
	binPath := ""
	userDataDir := "./chrome_user_data"
	controlURL := ""
	if cfg != nil && cfg.Browser != nil {
		binPath = cfg.Browser.BinPath
		userDataDir = cfg.Browser.UserDataDir
		controlURL = cfg.Browser.ControlURL
	}
	headless := false
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		headless = true
	}
	instanceType := "local"
	if controlURL != "" {
		instanceType = "remote"
	}
	instance := &models.BrowserInstance{
		ID:          "default",
		Name:        "默认浏览器",
		Description: "系统默认浏览器实例",
		Type:        instanceType,
		BinPath:     binPath,
		UserDataDir: userDataDir,
		ControlURL:  controlURL,
		Headless:    &headless,
		LaunchArgs:  []string{"disable-blink-features=AutomationControlled", "no-first-run", "no-default-browser-check"},
		IsDefault:   true,
		IsActive:    false,
	}
	return s.SaveBrowserInstance(instance)
}

func (s *PostgresStore) seedDefaultUser(cfg *config.Config) error {
	users, err := s.ListUsers()
	if err != nil {
		return err
	}
	if len(users) > 0 {
		hasAdmin := false
		for _, user := range users {
			if user != nil && user.IsAdmin {
				hasAdmin = true
				break
			}
		}
		if !hasAdmin {
			if existing, err := s.GetUserByUsername(cfg.Auth.DefaultUsername); err == nil {
				existing.IsAdmin = true
				existing.UpdatedAt = time.Now()
				return s.UpdateUser(existing)
			}
		}
		return nil
	}
	user := &models.User{
		ID:        uuid.New().String(),
		Username:  cfg.Auth.DefaultUsername,
		Password:  cfg.Auth.DefaultPassword,
		IsAdmin:   true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	return s.CreateUser(user)
}

func saveWithConflict(db *gorm.DB, value any) error {
	return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(value).Error
}

func firstByID[T any](db *gorm.DB, id string, label string) (*T, error) {
	var value T
	if err := db.First(&value, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%s not found", label)
		}
		return nil, err
	}
	return &value, nil
}

func (s *PostgresStore) SaveCookies(cookieStore *models.CookieStore) error {
	if cookieStore.UpdatedAt.IsZero() {
		cookieStore.UpdatedAt = time.Now()
	}
	if cookieStore.CreatedAt.IsZero() {
		cookieStore.CreatedAt = time.Now()
	}
	return saveWithConflict(s.db, cookieStore)
}

func (s *PostgresStore) GetCookies(id string) (*models.CookieStore, error) {
	return firstByID[models.CookieStore](s.db, id, "cookies")
}

func (s *PostgresStore) DeleteCookies(id string) error {
	return s.db.Delete(&models.CookieStore{}, "id = ?", id).Error
}

func (s *PostgresStore) SaveScript(script *models.Script) error {
	return saveWithConflict(s.db, script)
}

func (s *PostgresStore) GetScript(id string) (*models.Script, error) {
	return firstByID[models.Script](s.db, id, "script")
}

func (s *PostgresStore) ListScripts() ([]*models.Script, error) {
	var scripts []*models.Script
	err := s.db.Order("created_at desc").Find(&scripts).Error
	return scripts, err
}

func (s *PostgresStore) UpdateScript(script *models.Script) error {
	script.UpdatedAt = time.Now()
	return s.SaveScript(script)
}

func (s *PostgresStore) DeleteScript(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&models.ToolConfig{}, "script_id = ?", id).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Script{}, "id = ?", id).Error
	})
}

func (s *PostgresStore) SaveLLMConfig(config *models.LLMConfigModel) error {
	return s.saveLLMConfig(config, false)
}

func (s *PostgresStore) UpdateLLMConfig(config *models.LLMConfigModel) error {
	config.UpdatedAt = time.Now()
	return s.saveLLMConfig(config, true)
}

func (s *PostgresStore) saveLLMConfig(config *models.LLMConfigModel, update bool) error {
	if config.ID == "" {
		config.ID = config.Name
	}
	if config.APIKey != "" {
		ciphertext, nonce, err := s.encryptLLMAPIKey(config.APIKey)
		if err != nil {
			return err
		}
		config.APIKeyCiphertext = ciphertext
		config.APIKeyNonce = nonce
		config.APIKeyKeyID = llmAPIKeyKeyID
	} else if update || config.APIKeyCiphertext == "" {
		var existing models.LLMConfigModel
		if err := s.db.First(&existing, "id = ?", config.ID).Error; err == nil {
			config.APIKeyCiphertext = existing.APIKeyCiphertext
			config.APIKeyNonce = existing.APIKeyNonce
			config.APIKeyKeyID = existing.APIKeyKeyID
		}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if config.IsDefault {
			if err := tx.Model(&models.LLMConfigModel{}).Where("id <> ?", config.ID).Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return saveWithConflict(tx, config)
	})
}

func (s *PostgresStore) GetLLMConfig(id string) (*models.LLMConfigModel, error) {
	cfg, err := firstByID[models.LLMConfigModel](s.db, id, "LLM config")
	if err != nil {
		return nil, err
	}
	return cfg, s.decryptLLMAPIKey(cfg)
}

func (s *PostgresStore) ListLLMConfigs() ([]*models.LLMConfigModel, error) {
	var configs []*models.LLMConfigModel
	if err := s.db.Order("created_at desc").Find(&configs).Error; err != nil {
		return nil, err
	}
	for _, cfg := range configs {
		if err := s.decryptLLMAPIKey(cfg); err != nil {
			return nil, err
		}
	}
	return configs, nil
}

func (s *PostgresStore) DeleteLLMConfig(id string) error {
	return s.db.Delete(&models.LLMConfigModel{}, "id = ?", id).Error
}

func (s *PostgresStore) GetDefaultLLMConfig() (*models.LLMConfigModel, error) {
	var cfg models.LLMConfigModel
	if err := s.db.Where("is_default = ? AND is_active = ?", true, true).Order("updated_at desc, created_at desc").First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("Default LLM config not found")
	}
	return &cfg, s.decryptLLMAPIKey(&cfg)
}

func (s *PostgresStore) ClearDefaultLLMConfig() error {
	return s.db.Model(&models.LLMConfigModel{}).Where("is_default = ?", true).Update("is_default", false).Error
}

func (s *PostgresStore) SaveBrowserConfig(config *models.BrowserConfig) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if config.IsDefault {
			if err := tx.Model(&models.BrowserConfig{}).Where("id <> ?", config.ID).Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return saveWithConflict(tx, config)
	})
}

func (s *PostgresStore) GetBrowserConfig(id string) (*models.BrowserConfig, error) {
	return firstByID[models.BrowserConfig](s.db, id, "browser config")
}

func (s *PostgresStore) GetDefaultBrowserConfig() (*models.BrowserConfig, error) {
	var cfg models.BrowserConfig
	if err := s.db.Where("is_default = ?", true).Order("updated_at desc, created_at desc").First(&cfg).Error; err != nil {
		return nil, fmt.Errorf("default browser config not found")
	}
	return &cfg, nil
}

func (s *PostgresStore) ListBrowserConfigs() ([]models.BrowserConfig, error) {
	var configs []models.BrowserConfig
	err := s.db.Order("created_at desc").Find(&configs).Error
	return configs, err
}

func (s *PostgresStore) DeleteBrowserConfig(id string) error {
	var cfg models.BrowserConfig
	if err := s.db.First(&cfg, "id = ?", id).Error; err == nil && cfg.IsDefault {
		return fmt.Errorf("Cannot delete default browser config")
	}
	return s.db.Delete(&models.BrowserConfig{}, "id = ?", id).Error
}

func (s *PostgresStore) SavePrompt(prompt *models.Prompt) error {
	db := s.db
	if prompt != nil && !prompt.UpdatedAt.IsZero() {
		updatedAt := prompt.UpdatedAt
		db = db.Session(&gorm.Session{
			NowFunc: func() time.Time {
				return updatedAt
			},
		})
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"name",
			"description",
			"content",
			"type",
			"version",
			"created_at",
			"updated_at",
		}),
	}).Create(prompt).Error
}

func (s *PostgresStore) GetPrompt(id string) (*models.Prompt, error) {
	return firstByID[models.Prompt](s.db, id, "prompt")
}

func (s *PostgresStore) ListPrompts() ([]*models.Prompt, error) {
	var prompts []*models.Prompt
	err := s.db.Order("created_at desc").Find(&prompts).Error
	return prompts, err
}

func (s *PostgresStore) UpdatePrompt(prompt *models.Prompt) error {
	prompt.UpdatedAt = time.Now()
	return s.SavePrompt(prompt)
}

func (s *PostgresStore) DeletePrompt(id string) error {
	return s.db.Delete(&models.Prompt{}, "id = ?", id).Error
}

func (s *PostgresStore) CheckAndUpdateSystemPrompts() error {
	for _, latest := range models.SystemPrompts {
		existing, err := s.GetPrompt(latest.ID)
		if err != nil {
			cp := *latest
			if cp.CreatedAt.IsZero() {
				cp.CreatedAt = time.Now()
			}
			cp.UpdatedAt = time.Now()
			if err := s.SavePrompt(&cp); err != nil {
				return err
			}
			continue
		}
		if existing.NeedsUpdate(latest) {
			cp := *latest
			cp.CreatedAt = existing.CreatedAt
			cp.UpdatedAt = time.Now()
			if err := s.SavePrompt(&cp); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *PostgresStore) SaveScriptExecution(execution *models.ScriptExecution) error {
	if execution.CreatedAt.IsZero() {
		execution.CreatedAt = time.Now()
	}
	return saveWithConflict(s.db, execution)
}

func (s *PostgresStore) GetScriptExecution(id string) (*models.ScriptExecution, error) {
	return firstByID[models.ScriptExecution](s.db, id, "script execution")
}

func (s *PostgresStore) GetLatestScriptExecutionByScriptID(scriptID string) (*models.ScriptExecution, error) {
	var execution models.ScriptExecution
	if err := s.db.Where("script_id = ?", scriptID).Order("start_time desc, created_at desc").First(&execution).Error; err != nil {
		return nil, err
	}
	return &execution, nil
}

func (s *PostgresStore) ListScriptExecutions(scriptID string) ([]*models.ScriptExecution, error) {
	var executions []*models.ScriptExecution
	q := s.db.Order("start_time desc, created_at desc")
	if scriptID != "" {
		q = q.Where("script_id = ?", scriptID)
	}
	err := q.Find(&executions).Error
	return executions, err
}

func (s *PostgresStore) DeleteScriptExecution(id string) error {
	return s.db.Delete(&models.ScriptExecution{}, "id = ?", id).Error
}

func (s *PostgresStore) DeleteScriptExecutionsByScriptID(scriptID string) error {
	return s.db.Delete(&models.ScriptExecution{}, "script_id = ?", scriptID).Error
}

func (s *PostgresStore) SaveRecordingConfig(config *models.RecordingConfig) error {
	return saveWithConflict(s.db, config)
}

func (s *PostgresStore) GetRecordingConfig(id string) (*models.RecordingConfig, error) {
	return firstByID[models.RecordingConfig](s.db, id, "recording config")
}

func (s *PostgresStore) GetDefaultRecordingConfig() *models.RecordingConfig {
	cfg, err := s.GetRecordingConfig("default")
	if err == nil {
		return cfg
	}
	cfg = models.GetDefaultRecordingConfig()
	_ = s.SaveRecordingConfig(cfg)
	return cfg
}

func (s *PostgresStore) SaveAgentSession(session *models.AgentSession) error {
	return saveWithConflict(s.db, session)
}

func (s *PostgresStore) GetAgentSession(id string) (*models.AgentSession, error) {
	return firstByID[models.AgentSession](s.db, id, "agent session")
}

func (s *PostgresStore) ListAgentSessions() ([]*models.AgentSession, error) {
	var sessions []*models.AgentSession
	err := s.db.Order("updated_at desc").Find(&sessions).Error
	return sessions, err
}

func (s *PostgresStore) DeleteAgentSession(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&models.AgentMessage{}, "session_id = ?", id).Error; err != nil {
			return err
		}
		return tx.Delete(&models.AgentSession{}, "id = ?", id).Error
	})
}

func (s *PostgresStore) SaveAgentMessage(message *models.AgentMessage) error {
	return saveWithConflict(s.db, message)
}

func (s *PostgresStore) GetAgentMessage(id string) (*models.AgentMessage, error) {
	return firstByID[models.AgentMessage](s.db, id, "agent message")
}

func (s *PostgresStore) ListAgentMessages(sessionID string) ([]*models.AgentMessage, error) {
	var messages []*models.AgentMessage
	err := s.db.Where("session_id = ?", sessionID).Order("timestamp asc").Find(&messages).Error
	return messages, err
}

func (s *PostgresStore) SaveToolConfig(config *models.ToolConfig) error {
	return saveWithConflict(s.db, config)
}

func (s *PostgresStore) GetToolConfig(id string) (*models.ToolConfig, error) {
	return firstByID[models.ToolConfig](s.db, id, "tool config")
}

func (s *PostgresStore) ListToolConfigs() ([]*models.ToolConfig, error) {
	var configs []*models.ToolConfig
	err := s.db.Order("name asc").Find(&configs).Error
	return configs, err
}

func (s *PostgresStore) DeleteToolConfig(id string) error {
	return s.db.Delete(&models.ToolConfig{}, "id = ?", id).Error
}

func (s *PostgresStore) DeleteToolConfigByScriptID(scriptID string) error {
	return s.db.Delete(&models.ToolConfig{}, "script_id = ?", scriptID).Error
}

func (s *PostgresStore) SaveMCPService(service *models.MCPService) error {
	return saveWithConflict(s.db, service)
}

func (s *PostgresStore) GetMCPService(id string) (*models.MCPService, error) {
	return firstByID[models.MCPService](s.db, id, "mcp service")
}

func (s *PostgresStore) ListMCPServices() ([]*models.MCPService, error) {
	var services []*models.MCPService
	err := s.db.Order("name asc").Find(&services).Error
	return services, err
}

func (s *PostgresStore) DeleteMCPService(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&mcpServiceToolsRow{}, "service_id = ?", id).Error; err != nil {
			return err
		}
		return tx.Delete(&models.MCPService{}, "id = ?", id).Error
	})
}

func (s *PostgresStore) SaveMCPServiceTools(serviceID string, tools []models.MCPDiscoveredTool) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var service models.MCPService
		if err := tx.First(&service, "id = ?", serviceID).Error; err != nil {
			return fmt.Errorf("mcp service not found: %s", serviceID)
		}
		service.ToolCount = len(tools)
		service.UpdatedAt = time.Now()
		if err := saveWithConflict(tx, &service); err != nil {
			return err
		}
		return saveWithConflict(tx, &mcpServiceToolsRow{ServiceID: serviceID, Tools: tools, UpdatedAt: time.Now()})
	})
}

func (s *PostgresStore) GetMCPServiceTools(serviceID string) ([]models.MCPDiscoveredTool, error) {
	var row mcpServiceToolsRow
	if err := s.db.First(&row, "service_id = ?", serviceID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []models.MCPDiscoveredTool{}, nil
		}
		return nil, err
	}
	return row.Tools, nil
}

func (s *PostgresStore) CreateUser(user *models.User) error {
	return s.db.Create(user).Error
}

func (s *PostgresStore) GetUser(id string) (*models.User, error) {
	return firstByID[models.User](s.db, id, "user")
}

func (s *PostgresStore) GetUserByUsername(username string) (*models.User, error) {
	var user models.User
	if err := s.db.Where("username = ?", username).First(&user).Error; err != nil {
		return nil, fmt.Errorf("user not found")
	}
	return &user, nil
}

func (s *PostgresStore) ListUsers() ([]*models.User, error) {
	var users []*models.User
	err := s.db.Order("created_at desc").Find(&users).Error
	return users, err
}

func (s *PostgresStore) UpdateUser(user *models.User) error {
	user.UpdatedAt = time.Now()
	return saveWithConflict(s.db, user)
}

func (s *PostgresStore) DeleteUser(id string) error {
	return s.db.Delete(&models.User{}, "id = ?", id).Error
}

func (s *PostgresStore) CreateApiKey(apiKey *models.ApiKey) error {
	return s.db.Create(apiKey).Error
}

func (s *PostgresStore) GetApiKey(id string) (*models.ApiKey, error) {
	return firstByID[models.ApiKey](s.db, id, "api key")
}

func (s *PostgresStore) GetApiKeyByKey(key string) (*models.ApiKey, error) {
	var apiKey models.ApiKey
	if err := s.db.Where("key = ?", key).First(&apiKey).Error; err != nil {
		return nil, fmt.Errorf("api key not found")
	}
	return &apiKey, nil
}

func (s *PostgresStore) ListApiKeys() ([]*models.ApiKey, error) {
	var keys []*models.ApiKey
	err := s.db.Order("created_at desc").Find(&keys).Error
	return keys, err
}

func (s *PostgresStore) ListApiKeysByUser(userID string) ([]*models.ApiKey, error) {
	var keys []*models.ApiKey
	err := s.db.Where("user_id = ?", userID).Order("created_at desc").Find(&keys).Error
	return keys, err
}

func (s *PostgresStore) UpdateApiKey(apiKey *models.ApiKey) error {
	apiKey.UpdatedAt = time.Now()
	return saveWithConflict(s.db, apiKey)
}

func (s *PostgresStore) DeleteApiKey(id string) error {
	return s.db.Delete(&models.ApiKey{}, "id = ?", id).Error
}

func (s *PostgresStore) SaveBrowserInstance(instance *models.BrowserInstance) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if instance.IsDefault {
			if err := tx.Model(&models.BrowserInstance{}).Where("id <> ?", instance.ID).Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return saveWithConflict(tx, instance)
	})
}

func (s *PostgresStore) GetBrowserInstance(id string) (*models.BrowserInstance, error) {
	return firstByID[models.BrowserInstance](s.db, id, "browser instance")
}

func (s *PostgresStore) ListBrowserInstances() ([]models.BrowserInstance, error) {
	var instances []models.BrowserInstance
	err := s.db.Order("is_default desc, created_at asc").Find(&instances).Error
	return instances, err
}

func (s *PostgresStore) GetDefaultBrowserInstance() (*models.BrowserInstance, error) {
	var instance models.BrowserInstance
	if err := s.db.Where("is_default = ?", true).Order("updated_at desc, created_at desc").First(&instance).Error; err != nil {
		return nil, fmt.Errorf("default browser instance not found")
	}
	return &instance, nil
}

func (s *PostgresStore) UpdateBrowserInstance(id string, instance *models.BrowserInstance) error {
	var existing models.BrowserInstance
	if err := s.db.First(&existing, "id = ?", id).Error; err != nil {
		return fmt.Errorf("browser instance not found")
	}
	instance.ID = id
	instance.UpdatedAt = time.Now()
	return s.SaveBrowserInstance(instance)
}

func (s *PostgresStore) DeleteBrowserInstance(id string) error {
	var instance models.BrowserInstance
	if err := s.db.First(&instance, "id = ?", id).Error; err == nil && instance.IsDefault {
		return fmt.Errorf("cannot delete default browser instance")
	}
	return s.db.Delete(&models.BrowserInstance{}, "id = ?", id).Error
}

func (s *PostgresStore) CreateScheduledTask(task *models.ScheduledTask) error {
	return s.db.Create(task).Error
}

func (s *PostgresStore) GetScheduledTask(id string) (*models.ScheduledTask, error) {
	return firstByID[models.ScheduledTask](s.db, id, "scheduled task")
}

func (s *PostgresStore) UpdateScheduledTask(task *models.ScheduledTask) error {
	var existing models.ScheduledTask
	if err := s.db.First(&existing, "id = ?", task.ID).Error; err != nil {
		return fmt.Errorf("scheduled task not found")
	}
	return saveWithConflict(s.db, task)
}

func (s *PostgresStore) DeleteScheduledTask(id string) error {
	return s.db.Delete(&models.ScheduledTask{}, "id = ?", id).Error
}

func (s *PostgresStore) ListScheduledTasks() ([]models.ScheduledTask, error) {
	var tasks []models.ScheduledTask
	err := s.db.Order("created_at desc").Find(&tasks).Error
	return tasks, err
}

func (s *PostgresStore) ListScheduledTasksWithPagination(page, pageSize int, searchQuery string) ([]models.ScheduledTask, int, error) {
	var tasks []models.ScheduledTask
	var total int64
	q := s.db.Model(&models.ScheduledTask{})
	if searchQuery != "" {
		like := "%" + strings.ToLower(searchQuery) + "%"
		q = q.Where("LOWER(name) LIKE ? OR LOWER(description) LIKE ?", like, like)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * pageSize
	if offset < 0 {
		offset = 0
	}
	err := q.Order("created_at desc").Limit(pageSize).Offset(offset).Find(&tasks).Error
	return tasks, int(total), err
}

func (s *PostgresStore) CreateTaskExecution(execution *models.TaskExecution) error {
	if execution.CreatedAt.IsZero() {
		execution.CreatedAt = time.Now()
	}
	return s.db.Create(execution).Error
}

func (s *PostgresStore) GetTaskExecution(id string) (*models.TaskExecution, error) {
	return firstByID[models.TaskExecution](s.db, id, "task execution")
}

func (s *PostgresStore) DeleteTaskExecution(id string) error {
	return s.db.Delete(&models.TaskExecution{}, "id = ?", id).Error
}

func (s *PostgresStore) ListTaskExecutions() ([]models.TaskExecution, error) {
	var executions []models.TaskExecution
	err := s.db.Order("start_time desc").Find(&executions).Error
	return executions, err
}

func (s *PostgresStore) ListTaskExecutionsWithPagination(page, pageSize int, taskID, searchQuery string, successFilter string) ([]models.TaskExecution, int, error) {
	var executions []models.TaskExecution
	var total int64
	q := s.db.Model(&models.TaskExecution{})
	if taskID != "" {
		q = q.Where("task_id = ?", taskID)
	}
	if searchQuery != "" {
		like := "%" + strings.ToLower(searchQuery) + "%"
		q = q.Where("LOWER(task_name) LIKE ? OR LOWER(message) LIKE ?", like, like)
	}
	if successFilter == "success" {
		q = q.Where("success = ?", true)
	} else if successFilter == "failed" {
		q = q.Where("success = ?", false)
	}
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * pageSize
	if offset < 0 {
		offset = 0
	}
	err := q.Order("start_time desc").Limit(pageSize).Offset(offset).Find(&executions).Error
	return executions, int(total), err
}

func (s *PostgresStore) BatchDeleteTaskExecutions(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return s.db.Delete(&models.TaskExecution{}, "id IN ?", ids).Error
}
