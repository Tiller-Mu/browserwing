package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/browserwing/browserwing/llm"
	"github.com/browserwing/browserwing/models"
	browserSvc "github.com/browserwing/browserwing/services/browser"
)

func TestP47UserModelRequiresSystemAdminFlag(t *testing.T) {
	userType := reflect.TypeOf(models.User{})
	field, ok := userType.FieldByName("IsAdmin")
	if !ok {
		t.Fatalf("models.User missing IsAdmin bool field required by P4.7 system administrator contract")
	}
	if field.Type.Kind() != reflect.Bool {
		t.Fatalf("models.User.IsAdmin type = %s, want bool", field.Type)
	}
	if jsonTag := field.Tag.Get("json"); !strings.HasPrefix(jsonTag, "is_admin") {
		t.Fatalf("models.User.IsAdmin json tag = %q, want is_admin", jsonTag)
	}
	if gormTag := field.Tag.Get("gorm"); !strings.Contains(gormTag, "default:false") {
		t.Fatalf("models.User.IsAdmin gorm tag = %q, want explicit default:false", gormTag)
	}
	if reflect.ValueOf(models.User{}).FieldByName("IsAdmin").Bool() {
		t.Fatal("zero-value models.User.IsAdmin = true, want ordinary new users to default false")
	}
}

func TestP47RecordingSessionAndArtifactProductionModels(t *testing.T) {
	sessionFields := p47ModelStructFields(t, "RecordingSession")
	p47RequireModelFields(t, "RecordingSession", sessionFields, []string{
		"ID",
		"ProjectID",
		"VersionID",
		"PageID",
		"RecordingKind",
		"AuthContext",
		"TargetURL",
		"Status",
		"ActionsJSON",
		"ActionCount",
		"DOMSnapshot",
		"RecordingMetaJSON",
		"ErrorMessage",
		"StartedAt",
		"LastSyncedAt",
		"StoppedAt",
		"SavedAt",
		"CreatedBy",
		"CreatedAt",
		"UpdatedAt",
	})

	artifactFields := p47ModelStructFields(t, "RecordingArtifact")
	p47RequireModelFields(t, "RecordingArtifact", artifactFields, []string{
		"ID",
		"ProjectID",
		"VersionID",
		"PageID",
		"RecordingSessionID",
		"ArtifactType",
		"StorageBackend",
		"StoragePath",
		"FileName",
		"MimeType",
		"SizeBytes",
		"Sensitive",
		"CreatedAt",
	})
}

func TestP47LLMConfigManagementRequiresAdminAndRedactsResponses(t *testing.T) {
	env := newGenerateContractEnv(t)
	env.handler.config.Auth.Enabled = true
	env.handler.config.Auth.AppKey = "p47-auth-key"
	env.handler.llmManager = llm.NewManager(env.store)

	regular := env.seedP47User(t, "p47-regular", false)
	admin := env.seedP47User(t, "p47-admin", true)
	regularToken := env.p47JWT(t, regular)
	adminToken := env.p47JWT(t, admin)

	const activeDefaultSecret = "test-api-key"
	const disabledSecret = "sk-p47-disabled-secret"
	disabledConfig := &models.LLMConfigModel{
		ID:        "p47-disabled",
		Name:      "p47-disabled",
		Provider:  "openai",
		APIKey:    disabledSecret,
		Model:     "gpt-p47-disabled",
		BaseURL:   "https://llm.example.invalid/v1",
		IsDefault: false,
		IsActive:  false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := env.store.SaveLLMConfig(disabledConfig); err != nil {
		t.Fatalf("seed disabled LLM config: %v", err)
	}

	regularList := env.p47JSONRequest(t, http.MethodGet, "/api/v1/llm-configs", nil, regularToken)
	env.requireStatus(t, regularList, http.StatusOK)
	p47RequireLLMListRedacted(t, regularList, activeDefaultSecret, disabledSecret)
	if !p47ResponseContainsConfigID(t, regularList, "default-test-llm") {
		t.Fatalf("non-admin LLM list should include active config summary %q: %s", "default-test-llm", regularList.Body.String())
	}
	if p47ResponseContainsConfigID(t, regularList, disabledConfig.ID) {
		t.Fatalf("non-admin LLM list includes inactive config %q: %s", disabledConfig.ID, regularList.Body.String())
	}
	regularDisabledDetail := env.p47JSONRequest(t, http.MethodGet, "/api/v1/llm-configs/"+disabledConfig.ID, nil, regularToken)
	if regularDisabledDetail.Code != http.StatusForbidden && regularDisabledDetail.Code != http.StatusNotFound {
		t.Fatalf("non-admin inactive LLM detail status = %d, want 403 or 404; body: %s", regularDisabledDetail.Code, regularDisabledDetail.Body.String())
	}
	if strings.Contains(regularDisabledDetail.Body.String(), disabledSecret) {
		t.Fatalf("non-admin inactive LLM detail leaked API key: %s", regularDisabledDetail.Body.String())
	}

	adminList := env.p47JSONRequest(t, http.MethodGet, "/api/v1/llm-configs", nil, adminToken)
	env.requireStatus(t, adminList, http.StatusOK)
	p47RequireLLMListRedacted(t, adminList, activeDefaultSecret, disabledSecret)
	if !p47ResponseContainsConfigID(t, adminList, disabledConfig.ID) {
		t.Fatalf("admin LLM list should include inactive config metadata %q: %s", disabledConfig.ID, adminList.Body.String())
	}
	adminDisabledDetail := env.p47JSONRequest(t, http.MethodGet, "/api/v1/llm-configs/"+disabledConfig.ID, nil, adminToken)
	env.requireStatus(t, adminDisabledDetail, http.StatusOK)
	p47RequireLLMConfigDetailRedacted(t, adminDisabledDetail, disabledConfig.ID, disabledSecret)

	managementCases := []struct {
		name    string
		method  string
		path    string
		payload map[string]any
	}{
		{
			name:   "create",
			method: http.MethodPost,
			path:   "/api/v1/llm-configs",
			payload: map[string]any{
				"name":       "p47-non-admin-created",
				"provider":   "openai",
				"api_key":    "sk-p47-created",
				"model":      "gpt-p47-created",
				"base_url":   "https://llm.example.invalid/v1",
				"is_active":  true,
				"is_default": false,
			},
		},
		{
			name:   "update",
			method: http.MethodPut,
			path:   "/api/v1/llm-configs/default-test-llm",
			payload: map[string]any{
				"name":       "default-test-llm",
				"provider":   "openai",
				"api_key":    "sk-p47-updated",
				"model":      "gpt-p47-updated",
				"base_url":   "https://llm.example.invalid/v1",
				"is_active":  true,
				"is_default": true,
			},
		},
		{
			name:    "delete",
			method:  http.MethodDelete,
			path:    "/api/v1/llm-configs/p47-disabled",
			payload: nil,
		},
		{
			name:   "test",
			method: http.MethodPost,
			path:   "/api/v1/llm-configs/test",
			payload: map[string]any{
				"name":     "p47-test",
				"provider": "invalid-contract-provider",
				"api_key":  "sk-p47-test",
				"model":    "gpt-p47-test",
				"base_url": "https://llm.example.invalid/v1",
			},
		},
	}

	for _, tc := range managementCases {
		t.Run(tc.name, func(t *testing.T) {
			beforeDefault, _ := env.store.GetLLMConfig("default-test-llm")
			beforeDisabled, disabledErr := env.store.GetLLMConfig(disabledConfig.ID)

			res := env.p47JSONRequest(t, tc.method, tc.path, tc.payload, regularToken)

			env.requireStatus(t, res, http.StatusForbidden)
			if _, err := env.store.GetLLMConfig("p47-non-admin-created"); err == nil {
				t.Fatal("non-admin create mutated LLM config store")
			}
			afterDefault, err := env.store.GetLLMConfig("default-test-llm")
			if err != nil {
				t.Fatalf("default LLM config missing after rejected non-admin action: %v", err)
			}
			if beforeDefault != nil && afterDefault.Model != beforeDefault.Model {
				t.Fatalf("default LLM config model changed after rejected non-admin action: got %q want %q", afterDefault.Model, beforeDefault.Model)
			}
			if disabledErr == nil {
				afterDisabled, err := env.store.GetLLMConfig(disabledConfig.ID)
				if err != nil {
					t.Fatalf("disabled LLM config was deleted by rejected non-admin action: %v", err)
				}
				if afterDisabled.IsActive != beforeDisabled.IsActive || afterDisabled.APIKey != beforeDisabled.APIKey {
					t.Fatalf("disabled LLM config changed after rejected non-admin action: got %+v want %+v", afterDisabled, beforeDisabled)
				}
			}
		})
	}

	const adminCreateSecret = "sk-p47-admin-created-secret"
	adminCreate := env.p47JSONRequest(t, http.MethodPost, "/api/v1/llm-configs", map[string]any{
		"name":       "p47-admin-created",
		"provider":   "openai",
		"api_key":    adminCreateSecret,
		"model":      "gpt-p47-admin-created",
		"base_url":   "https://llm.example.invalid/v1",
		"is_active":  true,
		"is_default": false,
	}, adminToken)
	env.requireStatus(t, adminCreate, http.StatusOK)
	if strings.Contains(adminCreate.Body.String(), adminCreateSecret) {
		t.Fatalf("admin create response leaked API key: %s", adminCreate.Body.String())
	}
	createdConfig, err := env.store.GetLLMConfig("p47-admin-created")
	if err != nil {
		t.Fatalf("admin create did not persist LLM config: %v", err)
	}
	if createdConfig.Model != "gpt-p47-admin-created" || createdConfig.APIKey != adminCreateSecret {
		t.Fatalf("admin created config = %+v, want persisted model and API key", createdConfig)
	}

	const adminUpdateSecret = "sk-p47-admin-updated-secret"
	adminUpdate := env.p47JSONRequest(t, http.MethodPut, "/api/v1/llm-configs/p47-admin-created", map[string]any{
		"name":       "p47-admin-created",
		"provider":   "openai",
		"api_key":    adminUpdateSecret,
		"model":      "gpt-p47-admin-updated",
		"base_url":   "https://llm.example.invalid/v2",
		"is_active":  true,
		"is_default": false,
	}, adminToken)
	env.requireStatus(t, adminUpdate, http.StatusOK)
	if strings.Contains(adminUpdate.Body.String(), adminUpdateSecret) {
		t.Fatalf("admin update response leaked API key: %s", adminUpdate.Body.String())
	}
	updatedConfig, err := env.store.GetLLMConfig("p47-admin-created")
	if err != nil {
		t.Fatalf("admin updated config missing: %v", err)
	}
	if updatedConfig.Model != "gpt-p47-admin-updated" || updatedConfig.APIKey != adminUpdateSecret {
		t.Fatalf("admin updated config = %+v, want updated model and API key", updatedConfig)
	}

	const adminTestSecret = "sk-p47-admin-test-secret"
	var adminTestCalls int32
	env.installP47FakeLLMConfigTester(t, func(_ context.Context, cfg *models.LLMConfigModel) (map[string]any, error) {
		atomic.AddInt32(&adminTestCalls, 1)
		if cfg == nil {
			t.Fatal("admin LLM config tester received nil config")
		}
		if cfg.APIKey != adminTestSecret {
			t.Fatalf("admin LLM config tester APIKey = %q, want configured test key", cfg.APIKey)
		}
		return map[string]any{
			"success":  true,
			"message":  "llm.messages.testSuccess",
			"response": "OK",
		}, nil
	})
	adminTest := env.p47JSONRequest(t, http.MethodPost, "/api/v1/llm-configs/test", map[string]any{
		"name":     "p47-admin-test",
		"provider": "openai",
		"api_key":  adminTestSecret,
		"model":    "gpt-p47-admin-test",
		"base_url": "https://llm.example.invalid/v1",
	}, adminToken)
	env.requireStatus(t, adminTest, http.StatusOK)
	if atomic.LoadInt32(&adminTestCalls) != 1 {
		t.Fatalf("admin LLM config tester calls = %d, want 1", atomic.LoadInt32(&adminTestCalls))
	}
	if strings.Contains(adminTest.Body.String(), adminTestSecret) {
		t.Fatalf("admin test response leaked API key: %s", adminTest.Body.String())
	}

	adminDelete := env.p47JSONRequest(t, http.MethodDelete, "/api/v1/llm-configs/p47-admin-created", nil, adminToken)
	env.requireStatus(t, adminDelete, http.StatusOK)
	if _, err := env.store.GetLLMConfig("p47-admin-created"); err == nil {
		t.Fatal("admin delete did not remove LLM config")
	}
}

func TestP47UnifiedLLMResolutionFailsBeforeGenerateAndRefineSideEffects(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedMainFlow(t, page.ID)
	testCase := env.seedTestCase(t, page.ID, "case before LLM preflight")
	env.store.llmConfigs = map[string]*models.LLMConfigModel{}
	env.setPlaybotStdout(t, validPlaybotOutput("must not be generated"))

	generate := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
		"mode": "append",
	})
	env.requireStatus(t, generate, http.StatusBadRequest)
	env.requirePlaybotCalls(t, 0)
	p47RequireLLMErrorCode(t, generate, "llm_config_missing_default")
	env.requireTestCaseCount(t, page.ID, 1)

	refine := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
		"prompt": "add one edge case",
	})
	env.requireStatus(t, refine, http.StatusBadRequest)
	env.requirePlaybotCalls(t, 0)
	p47RequireLLMErrorCode(t, refine, "llm_config_missing_default")
	env.requireRefinementCount(t, testCase.ID, 0)
}

func TestP47GenerateUsesExplicitLLMConfigAndRejectsUnavailableSelections(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	env.seedMainFlow(t, page.ID)

	const (
		defaultSecret   = "test-api-key"
		explicitSecret  = "sk-p47-generate-explicit-secret"
		disabledSecret  = "sk-p47-generate-disabled-secret"
		explicitModel   = "gpt-p47-generate-explicit"
		explicitBaseURL = "https://explicit-generate-llm.example.invalid/v1"
		defaultModel    = "test-model"
		defaultBaseURL  = "http://llm.invalid/v1"
	)
	if err := env.store.SaveLLMConfig(&models.LLMConfigModel{
		ID:        "p47-generate-explicit",
		Name:      "p47-generate-explicit",
		Provider:  "openai",
		APIKey:    explicitSecret,
		Model:     explicitModel,
		BaseURL:   explicitBaseURL,
		IsDefault: false,
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed explicit generate LLM config: %v", err)
	}
	if err := env.store.SaveLLMConfig(&models.LLMConfigModel{
		ID:        "p47-generate-disabled",
		Name:      "p47-generate-disabled",
		Provider:  "openai",
		APIKey:    disabledSecret,
		Model:     "gpt-p47-generate-disabled",
		BaseURL:   "https://disabled-generate-llm.example.invalid/v1",
		IsDefault: false,
		IsActive:  false,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed disabled generate LLM config: %v", err)
	}
	if err := env.store.SaveLLMConfig(&models.LLMConfigModel{
		ID:        "p47-generate-incomplete",
		Name:      "p47-generate-incomplete",
		Provider:  "openai",
		APIKey:    "",
		Model:     "gpt-p47-generate-incomplete",
		BaseURL:   "https://incomplete-generate-llm.example.invalid/v1",
		IsDefault: false,
		IsActive:  true,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed incomplete generate LLM config: %v", err)
	}

	env.setPlaybotStdout(t, validPlaybotOutput("explicit generate llm"))
	explicit := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
		"mode":          "preview",
		"llm_config_id": "p47-generate-explicit",
	})
	env.requireStatus(t, explicit, http.StatusOK)
	env.requirePlaybotCalls(t, 1)
	if strings.Contains(explicit.Body.String(), explicitSecret) || strings.Contains(explicit.Body.String(), defaultSecret) {
		t.Fatalf("generate response leaked LLM API key: %s", explicit.Body.String())
	}
	callLog, err := env.playbotCallLog(t)
	if err != nil {
		t.Fatalf("read fake Playbot call log: %v", err)
	}
	if !strings.Contains(callLog, explicitModel) || !strings.Contains(callLog, explicitBaseURL) {
		t.Fatalf("Playbot call did not use explicit generate LLM config; log=%s", callLog)
	}
	if strings.Contains(callLog, defaultModel) || strings.Contains(callLog, defaultBaseURL) {
		t.Fatalf("Playbot call used default LLM despite explicit llm_config_id; log=%s", callLog)
	}
	if !strings.Contains(callLog, explicitSecret) {
		t.Fatalf("Playbot call did not receive explicit generate LLM API key; log=%s", callLog)
	}
	if strings.Contains(callLog, defaultSecret) {
		t.Fatalf("Playbot call used default LLM API key despite explicit llm_config_id; log=%s", callLog)
	}
	env.requireTestCaseCount(t, page.ID, 0)

	rejectedCases := []struct {
		name        string
		llmConfigID string
		wantCode    string
		secret      string
	}{
		{name: "missing", llmConfigID: "p47-generate-missing", wantCode: "llm_config_not_found"},
		{name: "disabled", llmConfigID: "p47-generate-disabled", wantCode: "llm_config_disabled", secret: disabledSecret},
		{name: "incomplete", llmConfigID: "p47-generate-incomplete", wantCode: "llm_config_incomplete"},
	}
	for _, tc := range rejectedCases {
		t.Run(tc.name, func(t *testing.T) {
			beforeCalls := env.playbotCalls(t)
			rejected := env.postGenerate(t, project.ID, version.ID, page.ID, map[string]any{
				"mode":          "append",
				"llm_config_id": tc.llmConfigID,
			})
			env.requireStatus(t, rejected, http.StatusBadRequest)
			env.requireJSONError(t, rejected)
			p47RequireLLMErrorCode(t, rejected, tc.wantCode)
			if got := env.playbotCalls(t); got != beforeCalls {
				t.Fatalf("Playbot calls after rejected generate LLM config = %d, want %d", got, beforeCalls)
			}
			if tc.secret != "" && strings.Contains(rejected.Body.String(), tc.secret) {
				t.Fatalf("rejected generate response leaked LLM API key: %s", rejected.Body.String())
			}
			env.requireTestCaseCount(t, page.ID, 0)
		})
	}
}

func TestP47RefineRejectsExplicitUnavailableLLMConfigsWithCodes(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	testCase := env.seedTestCase(t, page.ID, "case before explicit refine LLM preflight")
	env.setPlaybotStdout(t, validPlaybotRefineOutput("must not refine"))

	const (
		disabledSecret        = "sk-p47-refine-disabled-secret"
		missingModelSecret    = "sk-p47-refine-missing-model-secret"
		missingEndpointSecret = "sk-p47-refine-missing-endpoint-secret"
	)
	configs := []*models.LLMConfigModel{
		{
			ID:        "p47-refine-disabled",
			Name:      "p47-refine-disabled",
			Provider:  "openai",
			APIKey:    disabledSecret,
			Model:     "gpt-p47-refine-disabled",
			BaseURL:   "https://disabled-refine-llm.example.invalid/v1",
			IsDefault: false,
			IsActive:  false,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:        "p47-refine-missing-key",
			Name:      "p47-refine-missing-key",
			Provider:  "openai",
			APIKey:    "",
			Model:     "gpt-p47-refine-missing-key",
			BaseURL:   "https://missing-key-refine-llm.example.invalid/v1",
			IsDefault: false,
			IsActive:  true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:        "p47-refine-missing-model",
			Name:      "p47-refine-missing-model",
			Provider:  "openai",
			APIKey:    missingModelSecret,
			Model:     "",
			BaseURL:   "https://missing-model-refine-llm.example.invalid/v1",
			IsDefault: false,
			IsActive:  true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:        "p47-refine-missing-endpoint",
			Name:      "p47-refine-missing-endpoint",
			Provider:  "openai",
			APIKey:    missingEndpointSecret,
			Model:     "gpt-p47-refine-missing-endpoint",
			BaseURL:   "",
			IsDefault: false,
			IsActive:  true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}
	for _, cfg := range configs {
		if err := env.store.SaveLLMConfig(cfg); err != nil {
			t.Fatalf("seed refine LLM config %s: %v", cfg.ID, err)
		}
	}

	cases := []struct {
		name        string
		llmConfigID string
		wantCode    string
		secret      string
	}{
		{name: "missing", llmConfigID: "p47-refine-missing", wantCode: "llm_config_not_found"},
		{name: "disabled", llmConfigID: "p47-refine-disabled", wantCode: "llm_config_disabled", secret: disabledSecret},
		{name: "missing api key", llmConfigID: "p47-refine-missing-key", wantCode: "llm_config_incomplete"},
		{name: "missing model", llmConfigID: "p47-refine-missing-model", wantCode: "llm_config_incomplete", secret: missingModelSecret},
		{name: "missing endpoint", llmConfigID: "p47-refine-missing-endpoint", wantCode: "llm_config_incomplete", secret: missingEndpointSecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			beforeCalls := env.playbotCalls(t)
			refine := env.postRefineTestCase(t, project.ID, version.ID, page.ID, testCase.ID, map[string]any{
				"prompt":        "bad explicit refine LLM config must fail before Playbot",
				"llm_config_id": tc.llmConfigID,
			})
			env.requireStatus(t, refine, http.StatusBadRequest)
			env.requireJSONError(t, refine)
			p47RequireLLMErrorCode(t, refine, tc.wantCode)
			if got := env.playbotCalls(t); got != beforeCalls {
				t.Fatalf("Playbot calls after rejected refine LLM config = %d, want %d", got, beforeCalls)
			}
			if tc.secret != "" && strings.Contains(refine.Body.String(), tc.secret) {
				t.Fatalf("rejected refine response leaked LLM API key: %s", refine.Body.String())
			}
			env.requireRefinementCount(t, testCase.ID, 0)
		})
	}
}

func TestP47UnifiedLLMResolutionPreflightsAIExplorerBeforeAgent(t *testing.T) {
	env := newGenerateContractEnv(t)
	env.store.llmConfigs = map[string]*models.LLMConfigModel{}
	fakeAgent := &p47FakeAgentManager{}
	explorer := browserSvc.NewExplorer(nil, env.store)
	explorer.SetAgentManager(fakeAgent)
	env.handler.SetExplorer(explorer)

	missingDefault := env.p47JSONRequest(t, http.MethodPost, "/api/v1/ai-explore/start", map[string]any{
		"task_desc": "Open the orders page and summarize the first row",
		"start_url": "https://example.invalid/app/orders",
	}, "")
	env.requireStatus(t, missingDefault, http.StatusBadRequest)
	env.requireJSONError(t, missingDefault)
	p47RequireLLMErrorCode(t, missingDefault, "llm_config_missing_default")
	if got := fakeAgent.calls(); got != 0 {
		t.Fatalf("AI Explorer agent calls = %d, want 0 when default LLM is unavailable", got)
	}

	const disabledSecret = "sk-p47-disabled-explorer-secret"
	const incompleteSecret = "sk-p47-incomplete-explorer-secret"
	explorerConfigs := []*models.LLMConfigModel{
		{
			ID:        "p47-disabled-explorer",
			Name:      "p47-disabled-explorer",
			Provider:  "openai",
			APIKey:    disabledSecret,
			Model:     "gpt-p47-disabled-explorer",
			BaseURL:   "https://llm.example.invalid/v1",
			IsDefault: false,
			IsActive:  false,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
		{
			ID:        "p47-incomplete-explorer",
			Name:      "p47-incomplete-explorer",
			Provider:  "openai",
			APIKey:    incompleteSecret,
			Model:     "",
			BaseURL:   "https://incomplete-explorer-llm.example.invalid/v1",
			IsDefault: false,
			IsActive:  true,
			CreatedAt: time.Now(),
			UpdatedAt: time.Now(),
		},
	}
	for _, cfg := range explorerConfigs {
		if err := env.store.SaveLLMConfig(cfg); err != nil {
			t.Fatalf("seed explorer LLM config %s: %v", cfg.ID, err)
		}
	}

	explicitCases := []struct {
		name        string
		llmConfigID string
		wantCode    string
		secret      string
	}{
		{name: "missing", llmConfigID: "p47-missing-explorer", wantCode: "llm_config_not_found"},
		{name: "disabled", llmConfigID: "p47-disabled-explorer", wantCode: "llm_config_disabled", secret: disabledSecret},
		{name: "incomplete", llmConfigID: "p47-incomplete-explorer", wantCode: "llm_config_incomplete", secret: incompleteSecret},
	}
	for _, tc := range explicitCases {
		t.Run(tc.name, func(t *testing.T) {
			beforeCalls := fakeAgent.calls()
			res := env.p47JSONRequest(t, http.MethodPost, "/api/v1/ai-explore/start", map[string]any{
				"task_desc":     "Open the orders page and summarize the first row",
				"start_url":     "https://example.invalid/app/orders",
				"llm_config_id": tc.llmConfigID,
			}, "")
			env.requireStatus(t, res, http.StatusBadRequest)
			env.requireJSONError(t, res)
			p47RequireLLMErrorCode(t, res, tc.wantCode)
			if tc.secret != "" && strings.Contains(res.Body.String(), tc.secret) {
				t.Fatalf("AI Explorer invalid LLM response leaked API key: %s", res.Body.String())
			}
			if got := fakeAgent.calls(); got != beforeCalls {
				t.Fatalf("AI Explorer agent calls = %d, want unchanged at %d after invalid LLM config", got, beforeCalls)
			}
		})
	}
}

func TestP47StartRecordingPersistsSessionScopeAndRejectsHierarchyMismatch(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	fakeRuntime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, fakeRuntime)

	res := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
		"target_url":     "https://example.invalid/app/orders",
	})
	env.requireStatus(t, res, http.StatusOK)
	body := env.decodeObject(t, res)
	sessionID := strings.TrimSpace(fmt.Sprint(body["recording_session_id"]))
	if sessionID == "" {
		t.Fatalf("recording_session_id missing in response: %s", res.Body.String())
	}
	env.requireP47RecordingSession(t, map[string]any{
		"project_id":       project.ID,
		"version_id":       version.ID,
		"page_id":          page.ID,
		"recording_kind":   "business_flow",
		"auth_context":     "clean",
		"target_url":       "https://example.invalid/app/orders",
		"status":           "recording",
		"action_count":     0,
		"external_session": sessionID,
	})
	env.requireP47RecordingSessionTotalCount(t, 1)

	otherProject, otherVersion, _ := env.seedProjectVersionPage(t)
	mismatch := env.startPageRecordingSession(t, otherProject.ID, otherVersion.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
		"target_url":     "https://example.invalid/app/orders",
	})
	env.requireStatus(t, mismatch, http.StatusNotFound)
	env.requireJSONError(t, mismatch)
	env.requireP47RecordingSessionCount(t, project.ID, version.ID, page.ID, 1)
	env.requireP47RecordingSessionTotalCount(t, 1)

	rejectedStarts := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "invalid recording kind",
			payload: map[string]any{
				"recording_kind": "exploratory_flow",
				"auth_context":   "clean",
				"target_url":     "https://example.invalid/app/orders",
			},
		},
		{
			name: "invalid auth context",
			payload: map[string]any{
				"recording_kind": "business_flow",
				"auth_context":   "auto",
				"target_url":     "https://example.invalid/app/orders",
			},
		},
		{
			name: "project saved without active auth state",
			payload: map[string]any{
				"recording_kind": "business_flow",
				"auth_context":   "project_saved",
				"target_url":     "https://example.invalid/app/orders",
			},
		},
	}
	for _, tc := range rejectedStarts {
		t.Run(tc.name, func(t *testing.T) {
			rejected := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, tc.payload)
			env.requireStatus(t, rejected, http.StatusBadRequest)
			env.requireJSONError(t, rejected)
			env.requireP47RecordingSessionCount(t, project.ID, version.ID, page.ID, 1)
			env.requireP47RecordingSessionTotalCount(t, 1)
		})
	}
}

func TestP47RecordingSessionSummaryAndStopUseProductionRoutes(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	fakeRuntime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, fakeRuntime)

	start := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
		"target_url":     "https://example.invalid/app/orders",
	})
	env.requireStatus(t, start, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, start)["recording_session_id"]))
	if sessionID == "" {
		t.Fatalf("recording_session_id missing in response: %s", start.Body.String())
	}
	sessionPath := fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s",
		project.ID, version.ID, page.ID, sessionID,
	)

	summary := env.p47JSONRequest(t, http.MethodGet, sessionPath, nil, "")
	env.requireStatus(t, summary, http.StatusOK)
	body := env.decodeObject(t, summary)
	if body["status"] != "recording" {
		t.Fatalf("session summary status = %v, want recording; body: %s", body["status"], summary.Body.String())
	}
	if body["recording_kind"] != "business_flow" || body["auth_context"] != "clean" {
		t.Fatalf("session summary meta mismatch: %s", summary.Body.String())
	}
	if body["target_url"] != "https://example.invalid/app/orders" {
		t.Fatalf("session summary target_url = %v, want target URL", body["target_url"])
	}
	if body["actions_json"] != nil || body["dom_snapshot"] != nil {
		t.Fatalf("session summary leaked draft payloads instead of摘要 fields: %s", summary.Body.String())
	}

	stop := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/stop", map[string]any{
		"dom_snapshot": map[string]any{"title": "Orders stopped"},
	}, "")
	env.requireStatus(t, stop, http.StatusOK)
	stopBody := env.decodeObject(t, stop)
	if stopBody["status"] != "stopped" {
		t.Fatalf("stop response status = %v, want stopped; body: %s", stopBody["status"], stop.Body.String())
	}
	env.requireP47RecordingSession(t, map[string]any{
		"project_id":   project.ID,
		"version_id":   version.ID,
		"page_id":      page.ID,
		"status":       "stopped",
		"dom_snapshot": `"Orders stopped"`,
	})
	row := env.latestP47RecordingSessionRow(t, project.ID, version.ID, page.ID)
	p47RequireNonZeroTimestamp(t, row, "stopped_at")
}

func TestP47RecordingSessionSyncPersistsActionsAndRejectsTerminalStates(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	fakeRuntime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, fakeRuntime)

	start := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
		"target_url":     "https://example.invalid/app/orders",
	})
	env.requireStatus(t, start, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, start)["recording_session_id"]))

	sync := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/sync",
		project.ID, version.ID, page.ID, sessionID,
	), map[string]any{
		"actions": []map[string]any{
			{"type": "click", "selector": "#create-order", "timestamp": 1},
			{"type": "fill", "selector": "#email", "value": "user@example.invalid", "timestamp": 2},
		},
		"dom_snapshot": map[string]any{"title": "Orders"},
	}, "")
	env.requireStatus(t, sync, http.StatusOK)
	env.requireP47RecordingSession(t, map[string]any{
		"project_id":   project.ID,
		"version_id":   version.ID,
		"page_id":      page.ID,
		"auth_context": "clean",
		"status":       "recording",
		"action_count": 2,
		"dom_snapshot": `"Orders"`,
	})
	row := env.latestP47RecordingSessionRow(t, project.ID, version.ID, page.ID)
	p47RequireRecordingAction(t, row, 0, "click", "#create-order")
	p47RequireRecordingAction(t, row, 1, "fill", "#email")

	if err := env.db.Table("recording_sessions").
		Where("project_id = ? AND version_id = ? AND page_id = ?", project.ID, version.ID, page.ID).
		Update("status", "saved").Error; err != nil {
		t.Fatalf("mark RecordingSession saved through production table: %v", err)
	}
	terminalSync := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/sync",
		project.ID, version.ID, page.ID, sessionID,
	), map[string]any{
		"actions": []map[string]any{{"type": "click", "selector": "#should-not-save"}},
	}, "")
	env.requireStatus(t, terminalSync, http.StatusConflict)
	env.requireP47RecordingSession(t, map[string]any{
		"project_id":   project.ID,
		"version_id":   version.ID,
		"page_id":      page.ID,
		"status":       "saved",
		"action_count": 2,
	})
	row = env.latestP47RecordingSessionRow(t, project.ID, version.ID, page.ID)
	p47RequireRecordingAction(t, row, 0, "click", "#create-order")
}

func TestP47CancelRecordingSessionCancelsActiveSessionAndProtectsPageScript(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	siblingPage := env.seedPageInVersion(t, version.ID, "cancel sibling page")
	oldScript := env.seedMainFlowWithRecordingMeta(t, page.ID, map[string]any{
		"schema_version": 1,
		"recording_kind": "business_flow",
		"auth_context":   "clean",
	})
	oldSnapshot := env.snapshotPageScript(t, oldScript.ID)
	fakeRuntime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, fakeRuntime)

	start := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
		"target_url":     "https://example.invalid/app/orders",
	})
	env.requireStatus(t, start, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, start)["recording_session_id"]))
	sessionPath := fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s",
		project.ID, version.ID, page.ID, sessionID,
	)

	sync := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/sync", map[string]any{
		"actions": []map[string]any{
			{"type": "click", "selector": "#create-order", "timestamp": 1},
		},
		"dom_snapshot": map[string]any{"title": "Draft order flow"},
	}, "")
	env.requireStatus(t, sync, http.StatusOK)
	fakeRuntime.requireEvents(t, "new_clean_context", "open_target_url", "start_recording")

	scopeMismatch := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/cancel",
		project.ID, version.ID, siblingPage.ID, sessionID,
	), nil, "")
	env.requireStatus(t, scopeMismatch, http.StatusNotFound)
	env.requireJSONError(t, scopeMismatch)
	env.requireP47RecordingSession(t, map[string]any{
		"project_id":     project.ID,
		"version_id":     version.ID,
		"page_id":        page.ID,
		"status":         "recording",
		"action_count":   1,
		"dom_snapshot":   `"Draft order flow"`,
		"actions_json":   "#create-order",
		"target_url":     "https://example.invalid/app/orders",
		"auth_context":   "clean",
		"recording_kind": "business_flow",
	})
	env.requirePageScriptUnchanged(t, oldSnapshot, "scope-mismatched cancel must not mutate old PageScript")
	fakeRuntime.requireNoEvent(t, "stop_recording")

	cancel := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/cancel", nil, "")
	env.requireStatus(t, cancel, http.StatusOK)
	body := env.decodeObject(t, cancel)
	if body["status"] != "cancelled" {
		t.Fatalf("cancel response status = %v, want cancelled; body: %s", body["status"], cancel.Body.String())
	}
	fakeRuntime.requireEvents(t, "new_clean_context", "open_target_url", "start_recording", "stop_recording")
	env.requireP47RecordingSession(t, map[string]any{
		"project_id":   project.ID,
		"version_id":   version.ID,
		"page_id":      page.ID,
		"status":       "cancelled",
		"action_count": 1,
		"actions_json": "#create-order",
	})
	env.requirePageScriptUnchanged(t, oldSnapshot, "cancelled RecordingSession must not replace old PageScript")
	env.requireSinglePageScript(t, page.ID)
	row := env.latestP47RecordingSessionRow(t, project.ID, version.ID, page.ID)
	p47RequireEmptyTimestamp(t, row, "saved_at")

	repeatedCancel := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/cancel", nil, "")
	env.requireStatus(t, repeatedCancel, http.StatusConflict)
	env.requireJSONError(t, repeatedCancel)
	fakeRuntime.requireEvents(t, "new_clean_context", "open_target_url", "start_recording", "stop_recording")

	rejectedSync := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/sync", map[string]any{
		"actions": []map[string]any{{"type": "click", "selector": "#should-not-save"}},
	}, "")
	env.requireStatus(t, rejectedSync, http.StatusConflict)
	env.requireJSONError(t, rejectedSync)

	rejectedSave := env.savePageRecording(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_session_id": sessionID,
		"recording_meta": map[string]any{
			"schema_version": 1,
			"recording_kind": "business_flow",
			"auth_context":   "clean",
		},
	})
	env.requireStatus(t, rejectedSave, http.StatusConflict)
	env.requireJSONError(t, rejectedSave)
	env.requirePageScriptUnchanged(t, oldSnapshot, "cancelled RecordingSession sync/save must not pollute old PageScript")
	env.requireP47RecordingSession(t, map[string]any{
		"project_id":   project.ID,
		"version_id":   version.ID,
		"page_id":      page.ID,
		"status":       "cancelled",
		"action_count": 1,
		"actions_json": "#create-order",
	})
	fakeRuntime.requireEvents(t, "new_clean_context", "open_target_url", "start_recording", "stop_recording")
}

func TestP47CancelRecordingSessionAllowsStoppedUnsavedSession(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	oldScript := env.seedMainFlowWithRecordingMeta(t, page.ID, map[string]any{
		"schema_version": 1,
		"recording_kind": "business_flow",
		"auth_context":   "clean",
	})
	oldSnapshot := env.snapshotPageScript(t, oldScript.ID)
	fakeRuntime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, fakeRuntime)

	start := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
		"target_url":     "https://example.invalid/app/orders",
	})
	env.requireStatus(t, start, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, start)["recording_session_id"]))
	sessionPath := fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s",
		project.ID, version.ID, page.ID, sessionID,
	)

	sync := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/sync", map[string]any{
		"actions": []map[string]any{{"type": "click", "selector": "#stopped-draft", "timestamp": 1}},
	}, "")
	env.requireStatus(t, sync, http.StatusOK)
	stop := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/stop", map[string]any{
		"dom_snapshot": map[string]any{"title": "Stopped draft"},
	}, "")
	env.requireStatus(t, stop, http.StatusOK)
	env.requireP47RecordingSession(t, map[string]any{
		"project_id": project.ID,
		"version_id": version.ID,
		"page_id":    page.ID,
		"status":     "stopped",
	})

	cancel := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/cancel", nil, "")
	env.requireStatus(t, cancel, http.StatusOK)
	body := env.decodeObject(t, cancel)
	if body["status"] != "cancelled" {
		t.Fatalf("cancel stopped response status = %v, want cancelled; body: %s", body["status"], cancel.Body.String())
	}
	env.requirePageScriptUnchanged(t, oldSnapshot, "cancelling a stopped unsaved session must not generate a PageScript")
	env.requireSinglePageScript(t, page.ID)
	env.requireP47RecordingSession(t, map[string]any{
		"project_id":   project.ID,
		"version_id":   version.ID,
		"page_id":      page.ID,
		"status":       "cancelled",
		"action_count": 1,
		"actions_json": "#stopped-draft",
	})
	row := env.latestP47RecordingSessionRow(t, project.ID, version.ID, page.ID)
	p47RequireNonZeroTimestamp(t, row, "stopped_at")
	p47RequireEmptyTimestamp(t, row, "saved_at")

	rejectedSave := env.savePageRecording(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_session_id": sessionID,
		"recording_meta": map[string]any{
			"schema_version": 1,
			"recording_kind": "business_flow",
			"auth_context":   "clean",
		},
	})
	env.requireStatus(t, rejectedSave, http.StatusConflict)
	env.requireJSONError(t, rejectedSave)
	env.requirePageScriptUnchanged(t, oldSnapshot, "cancelled stopped session must not be saveable later")
}

func TestP47SaveRecordingSessionReplacesPageScriptWithRecordingMeta(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	oldScript := env.seedMainFlowWithRecordingMeta(t, page.ID, map[string]any{
		"schema_version": 1,
		"recording_kind": "business_flow",
		"auth_context":   "clean",
	})
	oldSnapshot := env.snapshotPageScript(t, oldScript.ID)
	fakeRuntime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, fakeRuntime)

	start := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
		"target_url":     "https://example.invalid/app/orders",
	})
	env.requireStatus(t, start, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, start)["recording_session_id"]))
	sessionPath := fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s",
		project.ID, version.ID, page.ID, sessionID,
	)
	sync := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/sync", map[string]any{
		"actions": []map[string]any{
			{"type": "click", "selector": "#create-order", "timestamp": 1},
		},
		"dom_snapshot": map[string]any{"title": "Orders"},
	}, "")
	env.requireStatus(t, sync, http.StatusOK)
	stop := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/stop", map[string]any{
		"dom_snapshot": map[string]any{"title": "Orders stopped"},
	}, "")
	env.requireStatus(t, stop, http.StatusOK)

	save := env.savePageRecording(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_session_id": sessionID,
		"recording_meta": map[string]any{
			"schema_version": 1,
			"recording_kind": "business_flow",
			"auth_context":   "clean",
		},
	})
	env.requireStatus(t, save, http.StatusOK)
	env.requirePageScriptMissing(t, oldScript.ID, "saving a valid RecordingSession must replace the previous main flow")
	saved := env.requireSinglePageScript(t, page.ID)
	if saved.ID == oldScript.ID {
		t.Fatalf("saved PageScript reused old ID %d, want replacement", oldScript.ID)
	}
	p47RequireActionTraceAction(t, saved.ActionTrace, 0, "click", "#create-order")
	if !strings.Contains(saved.DOMSnapshot, "Orders stopped") {
		t.Fatalf("saved PageScript DOMSnapshot = %s, want stopped snapshot", saved.DOMSnapshot)
	}
	p47RequireRecordingMeta(t, pageScriptRecordingMetaJSON(t, &saved), "business_flow", "clean")
	env.requireP47RecordingSession(t, map[string]any{
		"project_id": project.ID,
		"version_id": version.ID,
		"page_id":    page.ID,
		"status":     "saved",
	})
	row := env.latestP47RecordingSessionRow(t, project.ID, version.ID, page.ID)
	p47RequireRecordingAction(t, row, 0, "click", "#create-order")
	p47RequireNonZeroTimestamp(t, row, "saved_at")

	if strings.Contains(save.Body.String(), oldSnapshot.ActionTrace) || strings.Contains(save.Body.String(), oldSnapshot.DOMSnapshot) {
		t.Fatalf("save response leaked replaced PageScript payload: %s", save.Body.String())
	}
}

func TestP47SaveRecordingSessionRequiresStoppedSession(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	oldScript := env.seedMainFlowWithRecordingMeta(t, page.ID, map[string]any{
		"schema_version": 1,
		"recording_kind": "business_flow",
		"auth_context":   "clean",
	})
	oldSnapshot := env.snapshotPageScript(t, oldScript.ID)
	fakeRuntime := newContractP45Runtime()
	env.installProjectAuthRuntimeFake(t, fakeRuntime)

	start := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
		"target_url":     "https://example.invalid/app/orders",
	})
	env.requireStatus(t, start, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, start)["recording_session_id"]))

	save := env.savePageRecording(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_session_id": sessionID,
		"recording_meta": map[string]any{
			"schema_version": 1,
			"recording_kind": "business_flow",
			"auth_context":   "clean",
		},
	})
	env.requireStatus(t, save, http.StatusConflict)
	env.requireJSONError(t, save)
	fakeRuntime.requireEvents(t, "new_clean_context", "open_target_url", "start_recording")
	env.requirePageScriptUnchanged(t, oldSnapshot, "saving an active RecordingSession must not replace the previous main flow")
	env.requireSinglePageScript(t, page.ID)
	env.requireP47RecordingSession(t, map[string]any{
		"project_id": project.ID,
		"version_id": version.ID,
		"page_id":    page.ID,
		"status":     "recording",
	})
	row := env.latestP47RecordingSessionRow(t, project.ID, version.ID, page.ID)
	p47RequireEmptyTimestamp(t, row, "stopped_at")
	p47RequireEmptyTimestamp(t, row, "saved_at")
}

func TestP47CancelledFailedAndInvalidMetaDoNotReplacePageScript(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		meta       map[string]any
		wantStatus int
	}{
		{
			name:       "cancelled session",
			status:     "cancelled",
			meta:       map[string]any{"schema_version": 1, "recording_kind": "business_flow", "auth_context": "clean"},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "failed session",
			status:     "failed",
			meta:       map[string]any{"schema_version": 1, "recording_kind": "business_flow", "auth_context": "clean"},
			wantStatus: http.StatusConflict,
		},
		{
			name:       "invalid recording meta",
			status:     "stopped",
			meta:       map[string]any{"schema_version": 1, "recording_kind": "business_flow", "auth_context": "auto"},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newGenerateContractEnv(t)
			project, version, page := env.seedProjectVersionPage(t)
			oldScript := env.seedMainFlowWithRecordingMeta(t, page.ID, map[string]any{
				"schema_version": 1,
				"recording_kind": "business_flow",
				"auth_context":   "clean",
			})
			oldSnapshot := env.snapshotPageScript(t, oldScript.ID)
			fakeRuntime := newContractP45Runtime()
			env.installProjectAuthRuntimeFake(t, fakeRuntime)

			start := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
				"recording_kind": "business_flow",
				"auth_context":   "clean",
				"target_url":     "https://example.invalid/app/orders",
			})
			env.requireStatus(t, start, http.StatusOK)
			sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, start)["recording_session_id"]))
			sync := env.p47JSONRequest(t, http.MethodPost, fmt.Sprintf(
				"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s/sync",
				project.ID, version.ID, page.ID, sessionID,
			), map[string]any{
				"actions":      []map[string]any{{"type": "click", "selector": "#new-main-flow"}},
				"dom_snapshot": map[string]any{"title": "New main flow"},
			}, "")
			env.requireStatus(t, sync, http.StatusOK)
			if err := env.db.Table("recording_sessions").
				Where("project_id = ? AND version_id = ? AND page_id = ?", project.ID, version.ID, page.ID).
				Updates(map[string]any{"status": tc.status}).Error; err != nil {
				t.Fatalf("mark RecordingSession %s: %v", tc.status, err)
			}

			save := env.savePageRecording(t, project.ID, version.ID, page.ID, map[string]any{
				"recording_session_id": sessionID,
				"recording_meta":       tc.meta,
			})
			env.requireStatus(t, save, tc.wantStatus)
			env.requireJSONError(t, save)
			env.requirePageScriptUnchanged(t, oldSnapshot, "rejected RecordingSession save must not replace or mutate old PageScript")
			env.requireSinglePageScript(t, page.ID)
			env.requireP47RecordingSession(t, map[string]any{
				"project_id": project.ID,
				"version_id": version.ID,
				"page_id":    page.ID,
				"status":     tc.status,
			})
			row := env.latestP47RecordingSessionRow(t, project.ID, version.ID, page.ID)
			p47RequireEmptyTimestamp(t, row, "saved_at")
		})
	}
}

func TestP47RecordingArtifactDownloadUsesControlledScope(t *testing.T) {
	env := newGenerateContractEnv(t)
	project, version, page := env.seedProjectVersionPage(t)
	fakeRuntime := newContractP45Runtime()
	artifactRoot := filepath.Join(env.tmpDir, "artifact-store")
	env.handler.config.AssetsDir = artifactRoot
	storagePath := "recordings/p47-artifact.png"
	artifactBytes := []byte("p47 artifact bytes")
	artifactAbsolutePath := filepath.Join(artifactRoot, storagePath)
	if err := os.MkdirAll(filepath.Dir(artifactAbsolutePath), 0o755); err != nil {
		t.Fatalf("create artifact storage directory: %v", err)
	}
	if err := os.WriteFile(artifactAbsolutePath, artifactBytes, 0o600); err != nil {
		t.Fatalf("write controlled artifact file: %v", err)
	}
	fakeRuntime.stopResult = map[string]any{
		"actions":      []map[string]any{{"type": "click", "selector": "#artifact-source"}},
		"dom_snapshot": map[string]any{"title": "Artifact source"},
		"artifacts": []map[string]any{{
			"artifact_type":   "screenshot",
			"storage_backend": "local",
			"storage_path":    storagePath,
			"file_name":       "p47-artifact.png",
			"mime_type":       "image/png",
			"size_bytes":      len(artifactBytes),
			"sensitive":       false,
		}},
	}
	env.installProjectAuthRuntimeFake(t, fakeRuntime)

	start := env.startPageRecordingSession(t, project.ID, version.ID, page.ID, map[string]any{
		"recording_kind": "business_flow",
		"auth_context":   "clean",
		"target_url":     "https://example.invalid/app/orders",
	})
	env.requireStatus(t, start, http.StatusOK)
	sessionID := strings.TrimSpace(fmt.Sprint(env.decodeObject(t, start)["recording_session_id"]))
	sessionPath := fmt.Sprintf(
		"/api/v1/projects/%d/versions/%d/pages/%d/recording-session/%s",
		project.ID, version.ID, page.ID, sessionID,
	)

	stop := env.p47JSONRequest(t, http.MethodPost, sessionPath+"/stop", nil, "")
	env.requireStatus(t, stop, http.StatusOK)

	var artifactRows []map[string]any
	if err := env.db.Table("recording_artifacts").
		Where("project_id = ? AND version_id = ? AND page_id = ?", project.ID, version.ID, page.ID).
		Order("id desc").
		Find(&artifactRows).Error; err != nil {
		t.Fatalf("load RecordingArtifact metadata created by stop: %v", err)
	}
	if len(artifactRows) == 0 {
		t.Fatal("stop did not create RecordingArtifact metadata")
	}
	if got := fmt.Sprint(p47RowValue(artifactRows[0], "storage_path")); got != storagePath {
		t.Fatalf("RecordingArtifact storage_path = %q, want %q; row: %+v", got, storagePath, artifactRows[0])
	}
	artifactID := strings.TrimSpace(fmt.Sprint(p47RowValue(artifactRows[0], "id")))
	if artifactID == "" {
		t.Fatalf("RecordingArtifact id missing: %+v", artifactRows[0])
	}

	env.handler.config.Auth.Enabled = true
	env.handler.config.Auth.AppKey = "p47-artifact-auth-key"
	user := env.seedP47User(t, "p47-artifact-user", false)
	token := env.p47JWT(t, user)
	downloadPath := fmt.Sprintf(
		"/api/v1/recording-artifacts/%s/download?project_id=%d&version_id=%d&page_id=%d",
		artifactID, project.ID, version.ID, page.ID,
	)

	unauthenticated := env.p47JSONRequest(t, http.MethodGet, downloadPath, nil, "")
	env.requireStatus(t, unauthenticated, http.StatusUnauthorized)
	if strings.Contains(unauthenticated.Body.String(), storagePath) || strings.Contains(unauthenticated.Body.String(), artifactAbsolutePath) {
		t.Fatalf("unauthenticated artifact download response leaked storage path: %s", unauthenticated.Body.String())
	}

	download := env.p47JSONRequest(t, http.MethodGet, downloadPath, nil, token)
	env.requireStatus(t, download, http.StatusOK)
	if !bytes.Equal(download.Body.Bytes(), artifactBytes) {
		t.Fatalf("downloaded artifact body = %q, want %q", download.Body.String(), string(artifactBytes))
	}
	if contentType := download.Header().Get("Content-Type"); !strings.Contains(contentType, "image/png") {
		t.Fatalf("download Content-Type = %q, want image/png", contentType)
	}

	scopeMismatch := env.p47JSONRequest(t, http.MethodGet, fmt.Sprintf(
		"/api/v1/recording-artifacts/%s/download?project_id=%d&version_id=%d&page_id=%d",
		artifactID, project.ID, version.ID, page.ID+1000,
	), nil, token)
	env.requireStatus(t, scopeMismatch, http.StatusNotFound)
	env.requireJSONError(t, scopeMismatch)
	if strings.Contains(scopeMismatch.Body.String(), storagePath) || strings.Contains(scopeMismatch.Body.String(), artifactAbsolutePath) {
		t.Fatalf("artifact scope mismatch response leaked storage path: %s", scopeMismatch.Body.String())
	}
}

func (e *generateContractEnv) seedP47User(t *testing.T, username string, isAdmin bool) models.User {
	t.Helper()
	user := models.User{
		ID:        username + "-id",
		Username:  username,
		Password:  "contract-password",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	p47SetUserAdmin(t, &user, isAdmin)
	if err := e.store.CreateUser(&user); err != nil {
		t.Fatalf("seed P4.7 user: %v", err)
	}
	return user
}

func p47SetUserAdmin(t *testing.T, user *models.User, isAdmin bool) {
	t.Helper()
	value := reflect.ValueOf(user).Elem().FieldByName("IsAdmin")
	if !value.IsValid() {
		t.Fatalf("models.User missing IsAdmin; P4.7 admin permission tests require production user flag")
	}
	if !value.CanSet() || value.Kind() != reflect.Bool {
		t.Fatalf("models.User.IsAdmin cannot be set as bool in tests")
	}
	value.SetBool(isAdmin)
}

func (e *generateContractEnv) p47JWT(t *testing.T, user models.User) string {
	t.Helper()
	token, err := GenerateJWT(user.ID, user.Username, e.handler.config)
	if err != nil {
		t.Fatalf("generate JWT for %s: %v", user.Username, err)
	}
	return token
}

func (e *generateContractEnv) p47JSONRequest(t *testing.T, method, path string, payload map[string]any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal request payload: %v", err)
		}
		body = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, body)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	e.router.ServeHTTP(res, req)
	return res
}

func p47ResponseContainsConfigID(t *testing.T, res *httptest.ResponseRecorder, id string) bool {
	t.Helper()
	var body struct {
		Configs []map[string]any `json:"configs"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode LLM config list response: %v\nbody: %s", err, res.Body.String())
	}
	for _, cfg := range body.Configs {
		if fmt.Sprint(cfg["id"]) == id || fmt.Sprint(cfg["name"]) == id {
			return true
		}
	}
	return false
}

func p47RequireLLMListRedacted(t *testing.T, res *httptest.ResponseRecorder, secrets ...string) {
	t.Helper()
	raw := res.Body.String()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(raw, secret) {
			t.Fatalf("LLM config list leaked API key %q: %s", secret, raw)
		}
	}

	var body struct {
		Configs []map[string]any `json:"configs"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode LLM config list response: %v\nbody: %s", err, raw)
	}
	for _, cfg := range body.Configs {
		if got := strings.TrimSpace(fmt.Sprint(cfg["api_key"])); got != "" && got != "<nil>" {
			t.Fatalf("LLM config list returned non-empty api_key for config %v: %q; body: %s", cfg["id"], got, raw)
		}
		if _, ok := cfg["apiKey"]; ok {
			t.Fatalf("LLM config list returned camelCase apiKey field for config %v; body: %s", cfg["id"], raw)
		}
	}
}

func p47RequireLLMConfigDetailRedacted(t *testing.T, res *httptest.ResponseRecorder, wantID string, secrets ...string) {
	t.Helper()
	raw := res.Body.String()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(raw, secret) {
			t.Fatalf("LLM config detail leaked API key %q: %s", secret, raw)
		}
	}
	var cfg map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode LLM config detail response: %v\nbody: %s", err, raw)
	}
	if got := fmt.Sprint(cfg["id"]); got != wantID {
		t.Fatalf("LLM config detail id = %q, want %q; body: %s", got, wantID, raw)
	}
	if got := strings.TrimSpace(fmt.Sprint(cfg["api_key"])); got != "" && got != "<nil>" {
		t.Fatalf("LLM config detail returned non-empty api_key: %q; body: %s", got, raw)
	}
	if _, ok := cfg["apiKey"]; ok {
		t.Fatalf("LLM config detail returned camelCase apiKey field; body: %s", raw)
	}
}

type p47LLMConfigTesterInstaller interface {
	SetLLMConfigTesterForTest(func(context.Context, *models.LLMConfigModel) (map[string]any, error))
}

func (e *generateContractEnv) installP47FakeLLMConfigTester(t *testing.T, tester func(context.Context, *models.LLMConfigModel) (map[string]any, error)) {
	t.Helper()
	installer, ok := any(e.handler).(p47LLMConfigTesterInstaller)
	if !ok {
		t.Fatalf("Handler must expose SetLLMConfigTesterForTest so P4.7 admin LLM config test can use a fake tester instead of calling a real LLM")
	}
	installer.SetLLMConfigTesterForTest(tester)
}

func p47RequireLLMErrorCode(t *testing.T, res *httptest.ResponseRecorder, want string) {
	t.Helper()
	body := map[string]any{}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode LLM error response: %v; body: %s", err, res.Body.String())
	}
	candidates := []string{
		strings.TrimSpace(fmt.Sprint(body["code"])),
		strings.TrimSpace(fmt.Sprint(body["error_code"])),
	}
	if errorObject, ok := body["error"].(map[string]any); ok {
		candidates = append(candidates,
			strings.TrimSpace(fmt.Sprint(errorObject["code"])),
			strings.TrimSpace(fmt.Sprint(errorObject["error_code"])),
		)
	} else {
		candidates = append(candidates, strings.TrimSpace(fmt.Sprint(body["error"])))
	}
	for _, got := range candidates {
		if got == want {
			return
		}
	}
	t.Fatalf("LLM error code candidates = %v, want %q; body: %s", candidates, want, res.Body.String())
}

type p47FakeAgentManager struct {
	callCount int32
}

func (m *p47FakeAgentManager) SendMessageInterface(_ context.Context, _ string, _ string, _ chan<- any, _ string) error {
	atomic.AddInt32(&m.callCount, 1)
	return nil
}

func (m *p47FakeAgentManager) calls() int {
	return int(atomic.LoadInt32(&m.callCount))
}

type p47PageScriptSnapshot struct {
	ID                uint
	PageID            uint
	Name              string
	ActionTrace       string
	DOMSnapshot       string
	RecordingMetaJSON string
}

func (e *generateContractEnv) snapshotPageScript(t *testing.T, pageScriptID uint) p47PageScriptSnapshot {
	t.Helper()
	var script models.PageScript
	if err := e.db.Where("id = ?", pageScriptID).First(&script).Error; err != nil {
		t.Fatalf("load PageScript %d: %v", pageScriptID, err)
	}
	return p47PageScriptSnapshot{
		ID:                script.ID,
		PageID:            script.PageID,
		Name:              script.Name,
		ActionTrace:       script.ActionTrace,
		DOMSnapshot:       script.DOMSnapshot,
		RecordingMetaJSON: pageScriptRecordingMetaJSON(t, &script),
	}
}

func (e *generateContractEnv) requirePageScriptMissing(t *testing.T, pageScriptID uint, reason string) {
	t.Helper()
	var script models.PageScript
	err := e.db.Where("id = ?", pageScriptID).First(&script).Error
	if err == nil {
		t.Fatalf("PageScript %d still exists: %s", pageScriptID, reason)
	}
}

func (e *generateContractEnv) requirePageScriptUnchanged(t *testing.T, want p47PageScriptSnapshot, reason string) {
	t.Helper()
	var got models.PageScript
	if err := e.db.Where("id = ?", want.ID).First(&got).Error; err != nil {
		t.Fatalf("load PageScript %d after rejected save: %v", want.ID, err)
	}
	if got.PageID != want.PageID ||
		got.Name != want.Name ||
		got.ActionTrace != want.ActionTrace ||
		got.DOMSnapshot != want.DOMSnapshot ||
		pageScriptRecordingMetaJSON(t, &got) != want.RecordingMetaJSON {
		t.Fatalf("PageScript changed after rejected save: %s\nwant: %+v\n got: %+v", reason, want, got)
	}
}

func p47RequireRecordingMeta(t *testing.T, raw string, wantKind string, wantAuthContext string) {
	t.Helper()
	var meta map[string]any
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		t.Fatalf("recording_meta JSON is invalid: %v; raw=%s", err, raw)
	}
	if meta["recording_kind"] != wantKind || meta["auth_context"] != wantAuthContext {
		t.Fatalf("recording_meta = %+v, want recording_kind=%s auth_context=%s", meta, wantKind, wantAuthContext)
	}
}

func (e *generateContractEnv) requireP47RecordingSessionCount(t *testing.T, projectID, versionID, pageID uint, want int64) {
	t.Helper()
	var count int64
	if err := e.db.Table("recording_sessions").
		Where("project_id = ? AND version_id = ? AND page_id = ?", projectID, versionID, pageID).
		Count(&count).Error; err != nil {
		t.Fatalf("count RecordingSession rows: %v", err)
	}
	if count != want {
		t.Fatalf("RecordingSession count = %d, want %d", count, want)
	}
}

func (e *generateContractEnv) requireP47RecordingSessionTotalCount(t *testing.T, want int64) {
	t.Helper()
	var count int64
	if err := e.db.Table("recording_sessions").Count(&count).Error; err != nil {
		t.Fatalf("count all RecordingSession rows: %v", err)
	}
	if count != want {
		t.Fatalf("total RecordingSession count = %d, want %d", count, want)
	}
}

func (e *generateContractEnv) requireP47RecordingSession(t *testing.T, want map[string]any) {
	t.Helper()
	row := e.latestP47RecordingSessionRow(t, p47UintFromAny(want["project_id"]), p47UintFromAny(want["version_id"]), p47UintFromAny(want["page_id"]))
	for key, value := range want {
		if key == "external_session" {
			if !p47RowHasValue(row, value) {
				t.Fatalf("RecordingSession row does not contain external session %v: %+v", value, row)
			}
			continue
		}
		if key == "actions_json" || key == "dom_snapshot" {
			if !strings.Contains(fmt.Sprint(p47RowValue(row, key)), fmt.Sprint(value)) {
				t.Fatalf("RecordingSession %s = %v, want to contain %v; row: %+v", key, p47RowValue(row, key), value, row)
			}
			continue
		}
		got := p47RowValue(row, key)
		if fmt.Sprint(got) != fmt.Sprint(value) {
			t.Fatalf("RecordingSession %s = %v, want %v; row: %+v", key, got, value, row)
		}
	}
}

func (e *generateContractEnv) latestP47RecordingSessionRow(t *testing.T, projectID, versionID, pageID uint) map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := e.db.Table("recording_sessions").
		Where("project_id = ? AND version_id = ? AND page_id = ?", projectID, versionID, pageID).
		Order("id desc").
		Find(&rows).Error; err != nil {
		t.Fatalf("load RecordingSession rows: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("RecordingSession not found for project=%d version=%d page=%d", projectID, versionID, pageID)
	}
	return rows[0]
}

func p47UintFromAny(value any) uint {
	switch v := value.(type) {
	case uint:
		return v
	case uint64:
		return uint(v)
	case int:
		return uint(v)
	case int64:
		return uint(v)
	case float64:
		return uint(v)
	default:
		return 0
	}
}

func p47RequireRecordingAction(t *testing.T, row map[string]any, index int, wantType, wantSelector string) {
	t.Helper()
	actions := p47ParseActionsJSON(t, p47RowValue(row, "actions_json"))
	if index < 0 || index >= len(actions) {
		t.Fatalf("RecordingSession actions_json has %d actions, want index %d; row: %+v", len(actions), index, row)
	}
	if got := strings.TrimSpace(fmt.Sprint(actions[index]["type"])); got != wantType {
		t.Fatalf("RecordingSession actions_json[%d].type = %q, want %q; actions: %+v", index, got, wantType, actions)
	}
	if got := strings.TrimSpace(fmt.Sprint(actions[index]["selector"])); got != wantSelector {
		t.Fatalf("RecordingSession actions_json[%d].selector = %q, want %q; actions: %+v", index, got, wantSelector, actions)
	}
}

func p47RequireActionTraceAction(t *testing.T, raw string, index int, wantType, wantSelector string) {
	t.Helper()
	actions := p47ParseActionsJSON(t, raw)
	if index < 0 || index >= len(actions) {
		t.Fatalf("PageScript ActionTrace has %d actions, want index %d; raw: %s", len(actions), index, raw)
	}
	if got := strings.TrimSpace(fmt.Sprint(actions[index]["type"])); got != wantType {
		t.Fatalf("PageScript ActionTrace[%d].type = %q, want %q; actions: %+v", index, got, wantType, actions)
	}
	if got := strings.TrimSpace(fmt.Sprint(actions[index]["selector"])); got != wantSelector {
		t.Fatalf("PageScript ActionTrace[%d].selector = %q, want %q; actions: %+v", index, got, wantSelector, actions)
	}
}

func p47ParseActionsJSON(t *testing.T, raw any) []map[string]any {
	t.Helper()
	var data []byte
	switch typed := raw.(type) {
	case string:
		data = []byte(typed)
	case []byte:
		data = typed
	case json.RawMessage:
		data = []byte(typed)
	default:
		data = []byte(fmt.Sprint(typed))
	}
	var actions []map[string]any
	if err := json.Unmarshal(data, &actions); err != nil {
		t.Fatalf("parse actions JSON %q: %v", string(data), err)
	}
	return actions
}

func p47RequireNonZeroTimestamp(t *testing.T, row map[string]any, column string) {
	t.Helper()
	value := p47RowValue(row, column)
	switch typed := value.(type) {
	case time.Time:
		if typed.IsZero() {
			t.Fatalf("RecordingSession %s is zero time: %+v", column, row)
		}
	case *time.Time:
		if typed == nil || typed.IsZero() {
			t.Fatalf("RecordingSession %s is nil or zero time: %+v", column, row)
		}
	case nil:
		t.Fatalf("RecordingSession %s is missing: %+v", column, row)
	default:
		text := strings.TrimSpace(fmt.Sprint(typed))
		if text == "" || strings.HasPrefix(text, "0001-01-01") {
			t.Fatalf("RecordingSession %s = %q, want non-zero timestamp; row: %+v", column, text, row)
		}
	}
}

func p47RequireEmptyTimestamp(t *testing.T, row map[string]any, column string) {
	t.Helper()
	value := p47RowValue(row, column)
	switch typed := value.(type) {
	case nil:
		return
	case time.Time:
		if typed.IsZero() {
			return
		}
	case *time.Time:
		if typed == nil || typed.IsZero() {
			return
		}
	default:
		text := strings.TrimSpace(fmt.Sprint(typed))
		if text == "" || strings.HasPrefix(text, "0001-01-01") {
			return
		}
	}
	t.Fatalf("RecordingSession %s = %v, want empty timestamp after rejected save; row: %+v", column, value, row)
}

func p47RowHasValue(row map[string]any, value any) bool {
	want := fmt.Sprint(value)
	for _, got := range row {
		if fmt.Sprint(got) == want {
			return true
		}
	}
	return false
}

func p47RowValue(row map[string]any, key string) any {
	for gotKey, value := range row {
		if strings.EqualFold(gotKey, key) {
			return value
		}
	}
	return nil
}

func p47RequireModelFields(t *testing.T, structName string, fields map[string]string, required []string) {
	t.Helper()
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			t.Fatalf("models.%s missing field %s; fields: %+v", structName, field, fields)
		}
	}
}

func p47ModelStructFields(t *testing.T, structName string) map[string]string {
	t.Helper()
	modelsDir := filepath.Join(p47BackendRoot(t), "models")
	entries, err := os.ReadDir(modelsDir)
	if err != nil {
		t.Fatalf("read models directory: %v", err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(modelsDir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != structName {
					continue
				}
				structType, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					t.Fatalf("models.%s is %T, want struct", structName, typeSpec.Type)
				}
				return p47ASTStructFields(t, fset, structType)
			}
		}
	}
	t.Fatalf("models.%s production struct not found", structName)
	return nil
}

func p47ASTStructFields(t *testing.T, fset *token.FileSet, structType *ast.StructType) map[string]string {
	t.Helper()
	fields := make(map[string]string)
	for _, field := range structType.Fields.List {
		if len(field.Names) == 0 {
			continue
		}
		var buf bytes.Buffer
		if err := printer.Fprint(&buf, fset, field.Type); err != nil {
			t.Fatalf("print AST field type: %v", err)
		}
		for _, name := range field.Names {
			fields[name.Name] = buf.String()
		}
	}
	return fields
}

func p47ReadBackendFile(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{p47BackendRoot(t)}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func p47BackendRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			t.Fatalf("could not locate backend root from %s", wd)
		}
		wd = parent
	}
}
