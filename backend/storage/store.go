package storage

import (
	"github.com/browserwing/browserwing/models"
	"gorm.io/gorm"
)

type Store interface {
	Close() error
	GormDB() *gorm.DB

	SaveCookies(cookieStore *models.CookieStore) error
	GetCookies(id string) (*models.CookieStore, error)
	DeleteCookies(id string) error

	SaveScript(script *models.Script) error
	GetScript(id string) (*models.Script, error)
	ListScripts() ([]*models.Script, error)
	UpdateScript(script *models.Script) error
	DeleteScript(id string) error

	SaveLLMConfig(config *models.LLMConfigModel) error
	GetLLMConfig(id string) (*models.LLMConfigModel, error)
	ListLLMConfigs() ([]*models.LLMConfigModel, error)
	UpdateLLMConfig(config *models.LLMConfigModel) error
	DeleteLLMConfig(id string) error
	GetDefaultLLMConfig() (*models.LLMConfigModel, error)
	ClearDefaultLLMConfig() error

	SaveBrowserConfig(config *models.BrowserConfig) error
	GetBrowserConfig(id string) (*models.BrowserConfig, error)
	GetDefaultBrowserConfig() (*models.BrowserConfig, error)
	ListBrowserConfigs() ([]models.BrowserConfig, error)
	DeleteBrowserConfig(id string) error

	SavePrompt(prompt *models.Prompt) error
	GetPrompt(id string) (*models.Prompt, error)
	ListPrompts() ([]*models.Prompt, error)
	UpdatePrompt(prompt *models.Prompt) error
	DeletePrompt(id string) error
	CheckAndUpdateSystemPrompts() error

	SaveScriptExecution(execution *models.ScriptExecution) error
	GetScriptExecution(id string) (*models.ScriptExecution, error)
	GetLatestScriptExecutionByScriptID(scriptID string) (*models.ScriptExecution, error)
	ListScriptExecutions(scriptID string) ([]*models.ScriptExecution, error)
	DeleteScriptExecution(id string) error
	DeleteScriptExecutionsByScriptID(scriptID string) error

	SaveRecordingConfig(config *models.RecordingConfig) error
	GetRecordingConfig(id string) (*models.RecordingConfig, error)
	GetDefaultRecordingConfig() *models.RecordingConfig

	SaveAgentSession(session *models.AgentSession) error
	GetAgentSession(id string) (*models.AgentSession, error)
	ListAgentSessions() ([]*models.AgentSession, error)
	DeleteAgentSession(id string) error
	SaveAgentMessage(message *models.AgentMessage) error
	GetAgentMessage(id string) (*models.AgentMessage, error)
	ListAgentMessages(sessionID string) ([]*models.AgentMessage, error)

	SaveToolConfig(config *models.ToolConfig) error
	GetToolConfig(id string) (*models.ToolConfig, error)
	ListToolConfigs() ([]*models.ToolConfig, error)
	DeleteToolConfig(id string) error
	DeleteToolConfigByScriptID(scriptID string) error

	SaveMCPService(service *models.MCPService) error
	GetMCPService(id string) (*models.MCPService, error)
	ListMCPServices() ([]*models.MCPService, error)
	DeleteMCPService(id string) error
	SaveMCPServiceTools(serviceID string, tools []models.MCPDiscoveredTool) error
	GetMCPServiceTools(serviceID string) ([]models.MCPDiscoveredTool, error)

	CreateUser(user *models.User) error
	GetUser(id string) (*models.User, error)
	GetUserByUsername(username string) (*models.User, error)
	ListUsers() ([]*models.User, error)
	UpdateUser(user *models.User) error
	DeleteUser(id string) error
	CreateApiKey(apiKey *models.ApiKey) error
	GetApiKey(id string) (*models.ApiKey, error)
	GetApiKeyByKey(key string) (*models.ApiKey, error)
	ListApiKeys() ([]*models.ApiKey, error)
	ListApiKeysByUser(userID string) ([]*models.ApiKey, error)
	UpdateApiKey(apiKey *models.ApiKey) error
	DeleteApiKey(id string) error

	SaveBrowserInstance(instance *models.BrowserInstance) error
	GetBrowserInstance(id string) (*models.BrowserInstance, error)
	ListBrowserInstances() ([]models.BrowserInstance, error)
	GetDefaultBrowserInstance() (*models.BrowserInstance, error)
	UpdateBrowserInstance(id string, instance *models.BrowserInstance) error
	DeleteBrowserInstance(id string) error

	CreateScheduledTask(task *models.ScheduledTask) error
	GetScheduledTask(id string) (*models.ScheduledTask, error)
	UpdateScheduledTask(task *models.ScheduledTask) error
	DeleteScheduledTask(id string) error
	ListScheduledTasks() ([]models.ScheduledTask, error)
	ListScheduledTasksWithPagination(page, pageSize int, searchQuery string) ([]models.ScheduledTask, int, error)
	CreateTaskExecution(execution *models.TaskExecution) error
	GetTaskExecution(id string) (*models.TaskExecution, error)
	DeleteTaskExecution(id string) error
	ListTaskExecutions() ([]models.TaskExecution, error)
	ListTaskExecutionsWithPagination(page, pageSize int, taskID, searchQuery string, successFilter string) ([]models.TaskExecution, int, error)
	BatchDeleteTaskExecutions(ids []string) error
}

type StoreOperationInventoryEntry struct {
	Domain         string
	Method         string
	LegacySource   string
	PostgresTarget string
	ContractTest   string
}

var StoreOperationInventory = append([]StoreOperationInventoryEntry{
	{Domain: "Store", Method: "Close", LegacySource: "BoltDB.Close", PostgresTarget: "postgres connection cleanup", ContractTest: "TestP46StoreOperationInventoryMatchesStoreInterface"},
	{Domain: "TestingPlatform", Method: "GormDB", LegacySource: "storage.DB testing platform global", PostgresTarget: "controlled TestingPlatform GORM transaction entrance", ContractTest: "TestP46PostgresStoreTestingPlatformBusinessDataAccess"},
}, legacyStoreOperationInventory...)
