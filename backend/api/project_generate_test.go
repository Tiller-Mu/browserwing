package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/internal/testsupport"
	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/services/playbotagent"
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

func TestCloneVersionNormalizesHistoricalPageScript(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	source := models.PageScript{
		PageID:            page.ID,
		Name:              "historical flow",
		ActionTrace:       `[{"type":"input","value":"clone-password","attrs":{"type":"password","name":"login_password"},"accessibility":{"value":"clone-password"}},{"type":"download","url":"data:text/plain,discard"}]`,
		DOMSnapshot:       `{"elements":[{"role":"textbox","recorded_selector":"#password"}]}`,
		RecordingMetaJSON: defaultRecordingMetaJSON(),
	}
	if err := env.db.Create(&source).Error; err != nil {
		t.Fatalf("seed historical PageScript: %v", err)
	}

	data := []byte(`{"new_version_name":"normalized clone"}`)
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/projects/%d/versions/%d/clone", project.ID, version.ID), bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	env.router.ServeHTTP(res, req)
	env.requireStatus(t, res, http.StatusOK)
	cloned := env.decodeObject(t, res)
	clonedVersionID := uint(cloned["id"].(float64))

	var clonedPage models.TestPage
	if err := env.db.Where("version_id = ?", clonedVersionID).First(&clonedPage).Error; err != nil {
		t.Fatalf("load cloned page: %v", err)
	}
	var script models.PageScript
	if err := env.db.Where("page_id = ?", clonedPage.ID).First(&script).Error; err != nil {
		t.Fatalf("load cloned PageScript: %v", err)
	}
	if script.SourceRecordingSessionID != nil || script.PageScriptContentHash == "" || script.NormalizerVersion == "" {
		t.Fatalf("cloned PageScript provenance = %+v", script)
	}
	if strings.Contains(script.ActionTrace, "clone-password") || strings.Contains(script.ActionTrace, "data:") || !strings.Contains(script.ActionTrace, "{{REDACTED_SECRET}}") {
		t.Fatalf("cloned PageScript was not normalized: %s", script.ActionTrace)
	}
}

func TestCloneVersionRejectsInvalidHistoricalRecordingAtomically(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	if err := env.db.Create(&models.PageScript{
		PageID:            page.ID,
		Name:              "invalid historical flow",
		ActionTrace:       `[{"type":"click","selector":"#submit"}]`,
		DOMSnapshot:       `{"elements":[]}`,
		RecordingMetaJSON: `{"schema_version":1,"recording_kind":"business_flow","auth_context":"auto","target_url":"https://example.invalid"}`,
	}).Error; err != nil {
		t.Fatalf("seed invalid PageScript: %v", err)
	}
	var beforeVersions int64
	if err := env.db.Model(&models.ProjectVersion{}).Where("project_id = ?", project.ID).Count(&beforeVersions).Error; err != nil {
		t.Fatalf("count source versions: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/v1/projects/%d/versions/%d/clone", project.ID, version.ID), bytes.NewBufferString(`{"new_version_name":"must rollback"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	env.router.ServeHTTP(res, req)
	env.requireStatus(t, res, http.StatusUnprocessableEntity)
	body := env.decodeObject(t, res)
	if body["code"] != "recording_source_invalid" || strings.Contains(res.Body.String(), "auto") {
		t.Fatalf("invalid clone response leaked source or wrong code: %s", res.Body.String())
	}
	var afterVersions int64
	if err := env.db.Model(&models.ProjectVersion{}).Where("project_id = ?", project.ID).Count(&afterVersions).Error; err != nil {
		t.Fatalf("count versions after failed clone: %v", err)
	}
	if afterVersions != beforeVersions {
		t.Fatalf("invalid clone created partial version: before=%d after=%d", beforeVersions, afterVersions)
	}
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
	db           *gorm.DB
	store        *generateContractStore
	router       *gin.Engine
	handler      *Handler
	tmpDir       string
	playbotAgent *generateFakePlaybotAgent
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

	baseDSN, _, err := testsupport.PostgresDSN()
	if err != nil {
		t.Fatalf("load PostgreSQL contract test DSN: %v", err)
	}
	if baseDSN == "" {
		t.Skipf("generate contract tests require %s or backend/config.local.toml [database].dsn targeting PostgreSQL database PlayBot", testsupport.PostgresDSNEnv)
	}

	// Include a process-unique component.  The test process can be interrupted
	// before t.Cleanup runs; a later `go test` invocation must not collide with
	// that orphaned, test-owned schema merely because its in-process sequence
	// starts at one again.
	schema := fmt.Sprintf("generate_contract_%d_%d", time.Now().UTC().UnixNano(), atomic.AddUint64(&generateContractSequence, 1))
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
			Security: &config.SecurityConfig{
				ProjectAuthStateEncryptionKey:   "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
				ProjectAuthStateEncryptionKeyID: "contract-test-key",
			},
			Auth: &config.AuthConfig{
				Enabled: false,
				AppKey:  "test-secret",
			},
		},
	}
	fakeAgent := newGenerateFakePlaybotAgent(t)
	handler.SetPlaybotAgentClientForTest(fakeAgent)

	env := &generateContractEnv{
		db:           db,
		store:        store,
		router:       SetupRouter(handler, nil, nil, false, false),
		handler:      handler,
		tmpDir:       tmpDir,
		playbotAgent: fakeAgent,
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

type generateFakePlaybotAgent struct {
	t       *testing.T
	jobs    []playbotagent.Job
	secrets []playbotagent.SecretChannel
	result  playbotagent.Result
	err     error
}

func newGenerateFakePlaybotAgent(t *testing.T) *generateFakePlaybotAgent {
	t.Helper()
	return &generateFakePlaybotAgent{t: t}
}

func (a *generateFakePlaybotAgent) Run(_ context.Context, job playbotagent.Job) (playbotagent.Result, error) {
	a.jobs = append(a.jobs, cloneAgentJob(a.t, job))
	a.secrets = append(a.secrets, job.SecretChannel)
	if a.err != nil {
		return playbotagent.Result{}, a.err
	}
	return cloneAgentResult(a.t, a.result), nil
}

func (a *generateFakePlaybotAgent) setResult(result playbotagent.Result, err error) {
	a.result = cloneAgentResult(a.t, result)
	a.err = err
}

func (a *generateFakePlaybotAgent) callCount() int {
	return len(a.jobs)
}

func (a *generateFakePlaybotAgent) jobAt(t *testing.T, index int) playbotagent.Job {
	t.Helper()
	if index < 0 || index >= len(a.jobs) {
		t.Fatalf("Playbot agent job index %d out of range; call count = %d", index, len(a.jobs))
	}
	return cloneAgentJob(t, a.jobs[index])
}

func (a *generateFakePlaybotAgent) lastJob(t *testing.T) playbotagent.Job {
	t.Helper()
	return a.jobAt(t, len(a.jobs)-1)
}

func (a *generateFakePlaybotAgent) lastJobMap(t *testing.T) map[string]any {
	t.Helper()
	job := a.lastJob(t)
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal Playbot agent job: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode Playbot agent job: %v; raw: %s", err, data)
	}
	return out
}

func (a *generateFakePlaybotAgent) lastSecret(t *testing.T) playbotagent.SecretChannel {
	t.Helper()
	if len(a.secrets) == 0 {
		t.Fatal("Playbot agent was not called")
	}
	return a.secrets[len(a.secrets)-1]
}

func cloneAgentJob(t *testing.T, job playbotagent.Job) playbotagent.Job {
	t.Helper()
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("marshal Playbot agent job clone: %v", err)
	}
	var out playbotagent.Job
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode Playbot agent job clone: %v; raw: %s", err, data)
	}
	out.SecretChannel = job.SecretChannel
	return out
}

func cloneAgentResult(t *testing.T, result playbotagent.Result) playbotagent.Result {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal Playbot agent result clone: %v", err)
	}
	var out playbotagent.Result
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode Playbot agent result clone: %v; raw: %s", err, data)
	}
	return out
}

func (e *generateContractEnv) installFakePlaybotCommand(t *testing.T) {
	t.Helper()
	e.setPlaybotStdout(t, validPlaybotOutput("default generated case"))
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
		PageID:            pageID,
		Name:              "recorded main flow",
		ActionTrace:       `{"steps":[{"type":"click","target":{"role":"button","text":"primary action","recorded_selector":"button.primary"}}]}`,
		DOMSnapshot:       `{"elements":[{"role":"button","text":"primary action","recorded_selector":"button.primary"}]}`,
		RecordingMetaJSON: defaultRecordingMetaJSON(),
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

func (e *generateContractEnv) failNextRecordingSessionUpdate(t *testing.T) {
	t.Helper()
	callbackName := fmt.Sprintf("p1_contract_fail_recording_session_update_%d", time.Now().UnixNano())
	var pending int32 = 1
	if err := e.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Name == "RecordingSession" && atomic.CompareAndSwapInt32(&pending, 1, 0) {
			tx.AddError(errors.New("forced RecordingSession update failure"))
		}
	}); err != nil {
		t.Fatalf("register RecordingSession update failure callback: %v", err)
	}
	t.Cleanup(func() {
		_ = e.db.Callback().Update().Remove(callbackName)
	})
}

func (e *generateContractEnv) blockNextRecordingSessionUpdate(t *testing.T) (<-chan struct{}, func()) {
	t.Helper()
	callbackName := fmt.Sprintf("p1_contract_block_recording_session_update_%d", time.Now().UnixNano())
	entered := make(chan struct{})
	release := make(chan struct{})
	var pending int32 = 1
	var releaseOnce sync.Once
	if err := e.db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Name != "RecordingSession" || !atomic.CompareAndSwapInt32(&pending, 1, 0) {
			return
		}
		close(entered)
		<-release
	}); err != nil {
		t.Fatalf("register RecordingSession update barrier: %v", err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		_ = e.db.Callback().Update().Remove(callbackName)
	})
	return entered, func() {
		releaseOnce.Do(func() { close(release) })
	}
}

func (e *generateContractEnv) failNextProjectAuthStateCreate(t *testing.T) {
	t.Helper()
	callbackName := fmt.Sprintf("p1_contract_fail_project_auth_state_create_%d", time.Now().UnixNano())
	var pending int32 = 1
	if err := e.db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Schema != nil && tx.Statement.Schema.Name == "ProjectAuthState" && atomic.CompareAndSwapInt32(&pending, 1, 0) {
			tx.AddError(errors.New("forced ProjectAuthState create failure"))
		}
	}); err != nil {
		t.Fatalf("register ProjectAuthState create failure callback: %v", err)
	}
	t.Cleanup(func() {
		_ = e.db.Callback().Create().Remove(callbackName)
	})
}

func (e *generateContractEnv) setPlaybotStdout(t *testing.T, stdout string) {
	t.Helper()
	e.playbotAgent.setResult(parseLegacyPlaybotAgentResult(t, stdout), nil)
}

func (e *generateContractEnv) setPlaybotStderr(t *testing.T, stderr string) {
	t.Helper()
	_ = stderr
}

func (e *generateContractEnv) setPlaybotFailure(t *testing.T, stderr string) {
	t.Helper()
	e.playbotAgent.setResult(playbotagent.Result{}, errors.New(stderr))
}

func (e *generateContractEnv) playbotCalls(t *testing.T) int {
	t.Helper()
	return e.playbotAgent.callCount()
}

func (e *generateContractEnv) playbotCallLog(t *testing.T) (string, error) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"jobs":    e.playbotAgent.jobs,
		"secrets": e.playbotAgent.secrets,
	})
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
				{"action": "click", "target": map[string]any{"role": "button", "text": "primary action", "recorded_selector": "button.primary"}},
			},
		})
	}
	data, err := json.Marshal(map[string]any{
		"schema_version":  "p4.7.5",
		"status":          "success",
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

func defaultRecordingMetaJSON() string {
	data, err := json.Marshal(recordingMeta("business_flow", "clean"))
	if err != nil {
		panic(err)
	}
	return string(data)
}

func parseLegacyPlaybotAgentResult(t *testing.T, raw string) playbotagent.Result {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return playbotagent.Result{SchemaVersion: "p4.7.5", Status: "success"}
	}
	if strings.TrimSpace(stringFromAny(obj["status"])) != "" {
		var result playbotagent.Result
		data, err := json.Marshal(obj)
		if err != nil {
			t.Fatalf("marshal fake Playbot result: %v", err)
		}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("decode fake Playbot result: %v; raw: %s", err, data)
		}
		return result
	}
	if obj["error"] != nil {
		return playbotagent.Result{SchemaVersion: "p4.7.5", Status: "failed", Code: "playbot_agent_failed"}
	}
	result := playbotagent.Result{SchemaVersion: "p4.7.5", Status: "success"}
	if cases, ok := obj["test_cases"].([]any); ok {
		for _, item := range cases {
			if blueprint, ok := item.(map[string]any); ok {
				result.TestCases = append(result.TestCases, blueprint)
			}
		}
	}
	if refined, ok := obj["refined_blueprint"].(map[string]any); ok {
		result.RefinedBlueprint = refined
	}
	result.Summary = strings.TrimSpace(stringFromAny(obj["summary"]))
	result.RiskNotes = stringFromAny(obj["risk_notes"])
	return result
}
