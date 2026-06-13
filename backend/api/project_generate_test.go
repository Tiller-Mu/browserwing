package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/storage"
	"github.com/gin-gonic/gin"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var generateContractSequence uint64

func TestGenerateTestCasesRejectsPageWithoutMainFlow(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.setPlaybotStdout(t, validPlaybotOutput("should not be called"))

	res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
		"mode": "append",
	})

	env.requireStatus(t, res, http.StatusBadRequest)
	env.requirePlaybotCalls(t, 0)
	env.requireTestCaseCount(t, page.ID, 0)
}

func TestGenerateTestCasesPreviewReturnsCasesWithoutPersisting(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedMainFlow(t, page.ID)
	oldCase := env.seedTestCase(t, page.ID, "existing case before preview")
	oldSnapshot := env.snapshotTestCase(t, oldCase.ID)
	env.setPlaybotStdout(t, playbotOutputWithCases([]string{"preview case one", "preview case two"}))

	res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
		"mode":        "preview",
		"instruction": "cover boundary paths",
	})

	env.requireStatus(t, res, http.StatusOK)
	body := env.decodeObject(t, res)
	if body["saved"] != false {
		t.Fatalf("saved = %v, want false for preview", body["saved"])
	}
	if body["generated_count"] != float64(2) {
		t.Fatalf("generated_count = %v, want 2", body["generated_count"])
	}
	env.requireTestCaseCount(t, page.ID, 1)
	env.requireTestCaseUnchanged(t, oldSnapshot, "preview must not mutate existing test cases")
}

func TestGenerateTestCasesAppendKeepsExistingCasesAndAddsGeneratedCases(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedMainFlow(t, page.ID)
	oldCase := env.seedTestCase(t, page.ID, "existing case")
	oldSnapshot := env.snapshotTestCase(t, oldCase.ID)
	env.setPlaybotStdout(t, validPlaybotOutput("generated append case"))

	res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
		"mode": "append",
	})

	env.requireStatus(t, res, http.StatusOK)
	env.requireTestCaseCount(t, page.ID, 2)
	env.requireTestCaseUnchanged(t, oldSnapshot, "append must keep existing test cases unchanged")

	var generated models.TestCase
	if err := env.db.Where("page_id = ? AND title = ?", page.ID, "generated append case").First(&generated).Error; err != nil {
		t.Fatalf("generated TestCase not saved: %v", err)
	}
	if generated.Status != "active" {
		t.Fatalf("generated status = %q, want active", generated.Status)
	}
	if generated.ScriptContent != "" {
		t.Fatalf("generated script_content = %q, want empty string in P1", generated.ScriptContent)
	}
	env.requireBlueprintTitle(t, generated.Blueprint, "generated append case")
}

func TestGenerateTestCasesReplaceAtomicallyOverwritesExistingCases(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedMainFlow(t, page.ID)
	oldOne := env.seedTestCase(t, page.ID, "old case one")
	oldTwo := env.seedTestCase(t, page.ID, "old case two")
	env.setPlaybotStdout(t, validPlaybotOutput("replacement case"))

	res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
		"mode": "replace",
	})

	env.requireStatus(t, res, http.StatusOK)
	env.requireTestCaseCount(t, page.ID, 1)
	env.requireTestCaseMissing(t, oldOne.ID, "replace success must remove old cases")
	env.requireTestCaseMissing(t, oldTwo.ID, "replace success must remove old cases")
	var replacement models.TestCase
	if err := env.db.Where("page_id = ? AND title = ?", page.ID, "replacement case").First(&replacement).Error; err != nil {
		t.Fatalf("replacement TestCase not saved: %v", err)
	}
}

func TestGenerateTestCasesReplaceKeepsOldCasesWhenSavingGeneratedCasesFails(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedMainFlow(t, page.ID)
	oldCase := env.seedTestCase(t, page.ID, "old case before failed replace")
	oldSnapshot := env.snapshotTestCase(t, oldCase.ID)
	env.setPlaybotStdout(t, validPlaybotOutput("replacement that cannot be saved"))
	env.failFutureTestCaseCreates(t)

	res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
		"mode": "replace",
	})

	env.requireStatus(t, res, http.StatusInternalServerError)
	env.requireTestCaseCount(t, page.ID, 1)
	env.requireTestCaseUnchanged(t, oldSnapshot, "failed replace must roll back and keep old cases unchanged")
}

func TestGenerateTestCasesPlaybotFailureDoesNotDamageExistingCases(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedMainFlow(t, page.ID)
	oldCase := env.seedTestCase(t, page.ID, "old case before playbot failure")
	oldSnapshot := env.snapshotTestCase(t, oldCase.ID)
	env.setPlaybotFailure(t, "playbot failed while generating")

	res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
		"mode": "replace",
	})

	env.requireStatus(t, res, http.StatusInternalServerError)
	env.requirePlaybotCalls(t, 1)
	env.requireTestCaseCount(t, page.ID, 1)
	env.requireTestCaseUnchanged(t, oldSnapshot, "Playbot failure must not mutate existing cases")
}

func TestGenerateTestCasesRejectsMismatchedProjectVersionPageHierarchy(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedMainFlow(t, page.ID)
	otherProject, otherVersion, otherPage := env.seedProjectVersionPage(t)
	env.seedMainFlow(t, otherPage.ID)
	env.setPlaybotStdout(t, validPlaybotOutput("control generated case"))

	control := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
		"mode": "append",
	})
	env.requireStatus(t, control, http.StatusOK)

	cases := []struct {
		name      string
		projectID uint
		versionID uint
		pageID    uint
	}{
		{
			name:      "version belongs to another project",
			projectID: project.ID,
			versionID: otherVersion.ID,
			pageID:    otherPage.ID,
		},
		{
			name:      "page belongs to another version",
			projectID: otherProject.ID,
			versionID: otherVersion.ID,
			pageID:    page.ID,
		},
		{
			name:      "project belongs to another version/page chain",
			projectID: otherProject.ID,
			versionID: version.ID,
			pageID:    page.ID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beforeCalls := env.playbotCalls(t)
			res := env.postGenerate(t, tc.projectID, tc.versionID, tc.pageID, map[string]any{
				"mode": "append",
			})
			env.requireStatus(t, res, http.StatusNotFound)
			env.requireJSONError(t, res)
			if got := env.playbotCalls(t); got != beforeCalls {
				t.Fatalf("Playbot calls = %d, want unchanged at %d for hierarchy mismatch", got, beforeCalls)
			}
		})
	}
}

func TestGenerateTestCasesRejectsInvalidPlaybotOutputWithoutSaving(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
	}{
		{
			name:   "stdout is not JSON",
			stdout: "{not-json",
		},
		{
			name: "missing required steps",
			stdout: `{
				"test_cases": [
					{"title": "invalid generated case", "description": "missing steps"}
				],
				"analysis": {},
				"generated_count": 1,
				"error": null
			}`,
		},
		{
			name: "empty test_cases",
			stdout: `{
				"test_cases": [],
				"analysis": {},
				"generated_count": 0,
				"error": null
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			env.seedMainFlow(t, page.ID)
			oldCase := env.seedTestCase(t, page.ID, "old case before invalid output")
			oldSnapshot := env.snapshotTestCase(t, oldCase.ID)
			env.setPlaybotStdout(t, tc.stdout)

			res := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
				"mode": "replace",
			})

			env.requireStatus(t, res, http.StatusBadRequest)
			env.requireTestCaseCount(t, page.ID, 1)
			env.requireTestCaseUnchanged(t, oldSnapshot, "invalid Playbot output must not save, delete, or mutate cases")
		})
	}
}

type testCaseSnapshot struct {
	ID            uint
	PageID        uint
	Title         string
	Description   string
	Blueprint     string
	ScriptContent string
	Status        string
}

type generateContractEnv struct {
	db         *gorm.DB
	store      *generateContractStore
	router     *gin.Engine
	handler    *Handler
	tmpDir     string
	callsFile  string
	stdoutFile string
	stderrFile string
}

type generateContractStore struct {
	storage.Store
	db         *gorm.DB
	llmConfigs map[string]*models.LLMConfigModel
	users      map[string]*models.User
}

func newGenerateContractStore(db *gorm.DB) *generateContractStore {
	return &generateContractStore{
		db:         db,
		llmConfigs: make(map[string]*models.LLMConfigModel),
		users:      make(map[string]*models.User),
	}
}

func (s *generateContractStore) Close() error {
	return nil
}

func (s *generateContractStore) GormDB() *gorm.DB {
	return s.db
}

func (s *generateContractStore) SaveLLMConfig(config *models.LLMConfigModel) error {
	if config == nil {
		return fmt.Errorf("LLM config is required")
	}
	cp := *config
	if cp.IsDefault {
		for id, existing := range s.llmConfigs {
			if id != cp.ID {
				existing.IsDefault = false
			}
		}
	}
	s.llmConfigs[cp.ID] = &cp
	return nil
}

func (s *generateContractStore) UpdateLLMConfig(config *models.LLMConfigModel) error {
	if config == nil {
		return fmt.Errorf("LLM config is required")
	}
	existing, ok := s.llmConfigs[config.ID]
	if !ok {
		return fmt.Errorf("LLM config not found")
	}
	cp := *config
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = existing.CreatedAt
	}
	if cp.APIKey == "" {
		cp.APIKey = existing.APIKey
		cp.APIKeyCiphertext = existing.APIKeyCiphertext
		cp.APIKeyNonce = existing.APIKeyNonce
		cp.APIKeyKeyID = existing.APIKeyKeyID
	}
	return s.SaveLLMConfig(&cp)
}

func (s *generateContractStore) DeleteLLMConfig(id string) error {
	if _, ok := s.llmConfigs[id]; !ok {
		return fmt.Errorf("LLM config not found")
	}
	delete(s.llmConfigs, id)
	return nil
}

func (s *generateContractStore) GetLLMConfig(id string) (*models.LLMConfigModel, error) {
	if cfg, ok := s.llmConfigs[id]; ok {
		cp := *cfg
		return &cp, nil
	}
	return nil, fmt.Errorf("LLM config not found")
}

func (s *generateContractStore) GetDefaultLLMConfig() (*models.LLMConfigModel, error) {
	for _, cfg := range s.llmConfigs {
		if cfg.IsDefault && cfg.IsActive {
			cp := *cfg
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("Default LLM config not found")
}

func (s *generateContractStore) ClearDefaultLLMConfig() error {
	for _, cfg := range s.llmConfigs {
		cfg.IsDefault = false
	}
	return nil
}

func (s *generateContractStore) ListLLMConfigs() ([]*models.LLMConfigModel, error) {
	configs := make([]*models.LLMConfigModel, 0, len(s.llmConfigs))
	for _, cfg := range s.llmConfigs {
		cp := *cfg
		configs = append(configs, &cp)
	}
	return configs, nil
}

func (s *generateContractStore) CreateUser(user *models.User) error {
	if user == nil {
		return fmt.Errorf("user is required")
	}
	cp := *user
	if cp.ID == "" {
		cp.ID = cp.Username
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	cp.UpdatedAt = time.Now()
	s.users[cp.ID] = &cp
	return nil
}

func (s *generateContractStore) GetUser(id string) (*models.User, error) {
	if user, ok := s.users[id]; ok {
		cp := *user
		return &cp, nil
	}
	return nil, fmt.Errorf("user not found")
}

func (s *generateContractStore) GetUserByUsername(username string) (*models.User, error) {
	for _, user := range s.users {
		if user.Username == username {
			cp := *user
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("user not found")
}

func (s *generateContractStore) ListUsers() ([]*models.User, error) {
	users := make([]*models.User, 0, len(s.users))
	for _, user := range s.users {
		cp := *user
		users = append(users, &cp)
	}
	return users, nil
}

func (s *generateContractStore) UpdateUser(user *models.User) error {
	if user == nil {
		return fmt.Errorf("user is required")
	}
	if _, ok := s.users[user.ID]; !ok {
		return fmt.Errorf("user not found")
	}
	cp := *user
	cp.UpdatedAt = time.Now()
	s.users[cp.ID] = &cp
	return nil
}

func (s *generateContractStore) DeleteUser(id string) error {
	if _, ok := s.users[id]; !ok {
		return fmt.Errorf("user not found")
	}
	delete(s.users, id)
	return nil
}

func newGenerateContractGormDB(t *testing.T) *gorm.DB {
	t.Helper()

	baseDSN := strings.TrimSpace(os.Getenv("BROWSERWING_P46_POSTGRES_DSN"))
	if baseDSN == "" {
		t.Skip("generate contract tests require BROWSERWING_P46_POSTGRES_DSN targeting PostgreSQL database PlayBot")
	}

	schema := fmt.Sprintf("generate_contract_%d", atomic.AddUint64(&generateContractSequence, 1))
	adminDB, err := gorm.Open(postgres.Open(baseDSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open PostgreSQL admin connection: %v", err)
	}
	adminSQL, err := adminDB.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL admin sql handle: %v", err)
	}
	if err := adminDB.Exec("CREATE SCHEMA " + quotePostgresIdentifier(schema)).Error; err != nil {
		t.Fatalf("create PostgreSQL test schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		if err := adminDB.Exec("DROP SCHEMA IF EXISTS " + quotePostgresIdentifier(schema) + " CASCADE").Error; err != nil {
			t.Fatalf("drop PostgreSQL test schema %s: %v", schema, err)
		}
		_ = adminSQL.Close()
	})

	db, err := gorm.Open(postgres.Open(postgresDSNWithSearchPath(t, baseDSN, schema)), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open PostgreSQL test schema connection: %v", err)
	}
	testSQL, err := db.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL test sql handle: %v", err)
	}
	t.Cleanup(func() { _ = testSQL.Close() })
	return db
}

func postgresDSNWithSearchPath(t *testing.T, rawDSN, schema string) string {
	t.Helper()
	option := "-c search_path=" + schema
	if strings.Contains(rawDSN, "://") {
		parsed, err := url.Parse(rawDSN)
		if err != nil {
			t.Fatalf("parse PostgreSQL DSN: %v", err)
		}
		query := parsed.Query()
		if existing := strings.TrimSpace(query.Get("options")); existing != "" {
			option = existing + " " + option
		}
		query.Set("options", option)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(rawDSN) + " options='" + option + "'"
}

func quotePostgresIdentifier(value string) string {
	return "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
}

func newGenerateContractEnv(t *testing.T) *generateContractEnv {
	t.Helper()

	gin.SetMode(gin.TestMode)
	db := newGenerateContractGormDB(t)
	if err := models.AutoMigrate(db); err != nil {
		t.Fatalf("migrate testing models: %v", err)
	}

	tmpDir := t.TempDir()
	store := newGenerateContractStore(db)
	if err := store.SaveLLMConfig(&models.LLMConfigModel{
		ID:        "default-test-llm",
		Name:      "Default test LLM",
		Provider:  "custom",
		APIKey:    "test-api-key",
		Model:     "test-model",
		BaseURL:   "http://llm.invalid/v1",
		IsDefault: true,
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed default LLM config: %v", err)
	}

	handler := &Handler{
		db:        store,
		mcpServer: newNoopMCPServer(),
		config: &config.Config{
			Auth: &config.AuthConfig{
				Enabled: false,
				AppKey:  "test-secret",
			},
		},
	}

	env := &generateContractEnv{
		db:         db,
		store:      store,
		router:     SetupRouter(handler, nil, nil, false, false),
		handler:    handler,
		tmpDir:     tmpDir,
		callsFile:  filepath.Join(tmpDir, "playbot-calls.txt"),
		stdoutFile: filepath.Join(tmpDir, "playbot-stdout.json"),
		stderrFile: filepath.Join(tmpDir, "playbot-stderr.txt"),
	}
	env.installFakePlaybotCommand(t)
	return env
}

type noopMCPServer struct {
	sse *mcpserver.SSEServer
}

func newNoopMCPServer() *noopMCPServer {
	return &noopMCPServer{
		sse: mcpserver.NewSSEServer(mcpserver.NewMCPServer("test", "0.0.0")),
	}
}

func (n *noopMCPServer) GetStatus() map[string]interface{} {
	return map[string]interface{}{}
}

func (n *noopMCPServer) RegisterScript(*models.Script) error {
	return nil
}

func (n *noopMCPServer) UnregisterScript(string) {}

func (n *noopMCPServer) ServeSteamableHTTP(http.ResponseWriter, *http.Request) {}

func (n *noopMCPServer) GetSSEServer() *mcpserver.SSEServer {
	return n.sse
}

func (e *generateContractEnv) installFakePlaybotCommand(t *testing.T) {
	t.Helper()

	fakePython := filepath.Join(e.tmpDir, "fake-playbot-python")
	if runtime.GOOS == "windows" {
		fakePython += ".cmd"
		script := strings.Join([]string{
			"@echo off",
			"if defined BROWSERWING_FAKE_PLAYBOT_CALLS_FILE echo call %*>>\"%BROWSERWING_FAKE_PLAYBOT_CALLS_FILE%\"",
			"if defined BROWSERWING_FAKE_PLAYBOT_STDERR_FILE type \"%BROWSERWING_FAKE_PLAYBOT_STDERR_FILE%\" 1>&2",
			"if defined BROWSERWING_FAKE_PLAYBOT_STDOUT_FILE type \"%BROWSERWING_FAKE_PLAYBOT_STDOUT_FILE%\"",
			"if defined BROWSERWING_FAKE_PLAYBOT_EXIT_CODE exit /b %BROWSERWING_FAKE_PLAYBOT_EXIT_CODE%",
			"exit /b 0",
			"",
		}, "\r\n")
		if err := os.WriteFile(fakePython, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake python command: %v", err)
		}
	} else {
		script := strings.Join([]string{
			"#!/bin/sh",
			"if [ -n \"$BROWSERWING_FAKE_PLAYBOT_CALLS_FILE\" ]; then echo \"call $*\" >> \"$BROWSERWING_FAKE_PLAYBOT_CALLS_FILE\"; fi",
			"if [ -n \"$BROWSERWING_FAKE_PLAYBOT_STDERR_FILE\" ]; then cat \"$BROWSERWING_FAKE_PLAYBOT_STDERR_FILE\" >&2; fi",
			"if [ -n \"$BROWSERWING_FAKE_PLAYBOT_STDOUT_FILE\" ]; then cat \"$BROWSERWING_FAKE_PLAYBOT_STDOUT_FILE\"; fi",
			"exit ${BROWSERWING_FAKE_PLAYBOT_EXIT_CODE:-0}",
			"",
		}, "\n")
		if err := os.WriteFile(fakePython, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake python command: %v", err)
		}
		if err := os.Chmod(fakePython, 0o755); err != nil {
			t.Fatalf("chmod fake python command: %v", err)
		}
	}

	engineDir := filepath.Join(e.tmpDir, "playbot-engine")
	if err := os.MkdirAll(engineDir, 0o755); err != nil {
		t.Fatalf("create fake playbot engine dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(engineDir, "cli.py"), []byte("# fake cli placeholder\n"), 0o644); err != nil {
		t.Fatalf("write fake cli.py: %v", err)
	}

	t.Setenv("PLAYBOT_PYTHON", fakePython)
	t.Setenv("PLAYBOT_ENGINE_DIR", engineDir)
	t.Setenv("BROWSERWING_FAKE_PLAYBOT_CALLS_FILE", e.callsFile)
	t.Setenv("BROWSERWING_FAKE_PLAYBOT_STDOUT_FILE", e.stdoutFile)
	t.Setenv("BROWSERWING_FAKE_PLAYBOT_STDERR_FILE", e.stderrFile)
	t.Setenv("BROWSERWING_FAKE_PLAYBOT_EXIT_CODE", "0")

	e.setPlaybotStdout(t, validPlaybotOutput("default generated case"))
	e.setPlaybotStderr(t, "")
}

func (e *generateContractEnv) seedProjectVersionPage(t *testing.T) (models.Project, models.ProjectVersion, models.TestPage) {
	t.Helper()
	seq := atomic.AddUint64(&generateContractSequence, 1)
	project := models.Project{
		Name:        fmt.Sprintf("project-%d", seq),
		Description: "contract test project",
	}
	if err := e.db.Create(&project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	version := models.ProjectVersion{
		ProjectID:   project.ID,
		VersionName: "v1",
		BaseURL:     "https://example.invalid/app",
	}
	if err := e.db.Create(&version).Error; err != nil {
		t.Fatalf("seed version: %v", err)
	}
	page := models.TestPage{
		VersionID:   version.ID,
		Name:        "contract page",
		Path:        "/orders",
		Description: "page description used by Playbot",
	}
	if err := e.db.Create(&page).Error; err != nil {
		t.Fatalf("seed page: %v", err)
	}
	return project, version, page
}

func (e *generateContractEnv) seedMainFlow(t *testing.T, pageID uint) models.PageScript {
	t.Helper()
	script := models.PageScript{
		PageID:      pageID,
		Name:        "recorded main flow",
		ActionTrace: `{"steps":[{"type":"click","target":"primary action"}]}`,
		DOMSnapshot: `{"elements":[{"role":"button","text":"primary action"}]}`,
	}
	if err := e.db.Create(&script).Error; err != nil {
		t.Fatalf("seed main flow: %v", err)
	}
	return script
}

func (e *generateContractEnv) seedTestCase(t *testing.T, pageID uint, title string) models.TestCase {
	t.Helper()
	blueprint, err := json.Marshal(map[string]any{
		"title":       title,
		"description": "existing description",
		"steps":       []map[string]any{{"action": "noop"}},
	})
	if err != nil {
		t.Fatalf("marshal seed blueprint: %v", err)
	}
	testCase := models.TestCase{
		PageID:        pageID,
		Title:         title,
		Description:   "existing description",
		Blueprint:     string(blueprint),
		ScriptContent: "existing script should remain untouched",
		Status:        "active",
	}
	if err := e.db.Create(&testCase).Error; err != nil {
		t.Fatalf("seed TestCase: %v", err)
	}
	return testCase
}

func (e *generateContractEnv) postGenerate(t *testing.T, projectID, versionID, pageID uint, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request payload: %v", err)
	}
	path := fmt.Sprintf("/api/v1/projects/%d/versions/%d/pages/%d/test-cases/generate", projectID, versionID, pageID)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	e.router.ServeHTTP(res, req)
	return res
}

func (e *generateContractEnv) requireStatus(t *testing.T, res *httptest.ResponseRecorder, want int) {
	t.Helper()
	if res.Code != want {
		t.Fatalf("status = %d, want %d; body: %s", res.Code, want, res.Body.String())
	}
}

func (e *generateContractEnv) decodeObject(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response object: %v; body: %s", err, res.Body.String())
	}
	return body
}

func (e *generateContractEnv) requireJSONError(t *testing.T, res *httptest.ResponseRecorder) {
	t.Helper()
	body := e.decodeObject(t, res)
	if strings.TrimSpace(fmt.Sprint(body["error"])) == "" {
		t.Fatalf("response JSON missing non-empty error field: %v", body)
	}
}

func (e *generateContractEnv) requireTestCaseCount(t *testing.T, pageID uint, want int64) {
	t.Helper()
	var count int64
	if err := e.db.Model(&models.TestCase{}).Where("page_id = ?", pageID).Count(&count).Error; err != nil {
		t.Fatalf("count TestCase rows: %v", err)
	}
	if count != want {
		t.Fatalf("TestCase count for page %d = %d, want %d", pageID, count, want)
	}
}

func (e *generateContractEnv) requireTestCaseExists(t *testing.T, id uint, message string) {
	t.Helper()
	var testCase models.TestCase
	if err := e.db.First(&testCase, id).Error; err != nil {
		t.Fatalf("%s: TestCase %d should exist: %v", message, id, err)
	}
}

func (e *generateContractEnv) snapshotTestCase(t *testing.T, id uint) testCaseSnapshot {
	t.Helper()
	var testCase models.TestCase
	if err := e.db.First(&testCase, id).Error; err != nil {
		t.Fatalf("snapshot TestCase %d: %v", id, err)
	}
	return testCaseSnapshot{
		ID:            testCase.ID,
		PageID:        testCase.PageID,
		Title:         testCase.Title,
		Description:   testCase.Description,
		Blueprint:     testCase.Blueprint,
		ScriptContent: testCase.ScriptContent,
		Status:        testCase.Status,
	}
}

func (e *generateContractEnv) requireTestCaseUnchanged(t *testing.T, want testCaseSnapshot, message string) {
	t.Helper()
	got := e.snapshotTestCase(t, want.ID)
	if got != want {
		t.Fatalf("%s: TestCase changed\nwant: %+v\n got: %+v", message, want, got)
	}
}

func (e *generateContractEnv) requireTestCaseMissing(t *testing.T, id uint, message string) {
	t.Helper()
	var testCase models.TestCase
	err := e.db.First(&testCase, id).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("%s: TestCase %d lookup error = %v, want record not found", message, id, err)
	}
}

func (e *generateContractEnv) requireBlueprintTitle(t *testing.T, blueprint string, want string) {
	t.Helper()
	var parsed struct {
		Title string `json:"title"`
		Steps []any  `json:"steps"`
	}
	if err := json.Unmarshal([]byte(blueprint), &parsed); err != nil {
		t.Fatalf("Blueprint is not valid JSON: %v; blueprint: %s", err, blueprint)
	}
	if parsed.Title != want {
		t.Fatalf("Blueprint title = %q, want %q", parsed.Title, want)
	}
	if len(parsed.Steps) == 0 {
		t.Fatalf("Blueprint steps is empty, want generated steps preserved")
	}
}

func (e *generateContractEnv) failFutureTestCaseCreates(t *testing.T) {
	t.Helper()
	callbackName := "p1_contract_fail_test_case_create"
	if err := e.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Name == "TestCase" {
			tx.AddError(errors.New("forced TestCase create failure"))
		}
	}); err != nil {
		t.Fatalf("register create failure callback: %v", err)
	}
	t.Cleanup(func() {
		_ = e.db.Callback().Create().Remove(callbackName)
	})
}

func (e *generateContractEnv) setPlaybotStdout(t *testing.T, stdout string) {
	t.Helper()
	if err := os.WriteFile(e.stdoutFile, []byte(stdout), 0o644); err != nil {
		t.Fatalf("write fake Playbot stdout: %v", err)
	}
	t.Setenv("BROWSERWING_FAKE_PLAYBOT_EXIT_CODE", "0")
}

func (e *generateContractEnv) setPlaybotStderr(t *testing.T, stderr string) {
	t.Helper()
	if err := os.WriteFile(e.stderrFile, []byte(stderr), 0o644); err != nil {
		t.Fatalf("write fake Playbot stderr: %v", err)
	}
}

func (e *generateContractEnv) setPlaybotFailure(t *testing.T, stderr string) {
	t.Helper()
	e.setPlaybotStdout(t, "")
	e.setPlaybotStderr(t, stderr)
	t.Setenv("BROWSERWING_FAKE_PLAYBOT_EXIT_CODE", "7")
}

func (e *generateContractEnv) playbotCalls(t *testing.T) int {
	t.Helper()
	data, err := e.playbotCallLog(t)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read fake Playbot calls file: %v", err)
	}
	return strings.Count(data, "call")
}

func (e *generateContractEnv) playbotCallLog(t *testing.T) (string, error) {
	t.Helper()
	data, err := os.ReadFile(e.callsFile)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (e *generateContractEnv) requirePlaybotCalls(t *testing.T, want int) {
	t.Helper()
	if got := e.playbotCalls(t); got != want {
		t.Fatalf("Playbot calls = %d, want %d", got, want)
	}
}

func validPlaybotOutput(title string) string {
	return playbotOutputWithCases([]string{title})
}

func playbotOutputWithCases(titles []string) string {
	testCases := make([]map[string]any, 0, len(titles))
	for _, title := range titles {
		testCases = append(testCases, map[string]any{
			"title":       title,
			"description": "generated description for " + title,
			"steps": []map[string]any{
				{"action": "click", "target": "primary action"},
			},
		})
	}
	data, err := json.Marshal(map[string]any{
		"test_cases":      testCases,
		"analysis":        map[string]any{"source": "fake-playbot"},
		"generated_count": len(testCases),
		"error":           nil,
	})
	if err != nil {
		panic(err)
	}
	return string(data)
}
