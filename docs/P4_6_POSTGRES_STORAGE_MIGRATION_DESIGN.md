# P4.6 PostgreSQL 统一存储迁移详细设计

本文档定义 P4.6 阶段的业务契约、技术边界和红测口径。P4.6 插在 P4.5 录制体验和项目登录态之后、P5 多用户权限之前，目标是把当前 BoltDB + SQLite/GORM 双存储统一迁移为 PostgreSQL/GORM 单一业务数据库。

P4.6 是存储基础设施阶段，不新增业务能力，不改变 P1-P4.5 已确认的 API 语义、前端流程、Playbot 输入输出和登录态业务规则。

## 一、目标

P4.6 完成后应满足：

- 生产后端只使用 PostgreSQL 作为业务数据库。
- PostgreSQL 数据库名称固定为 `PlayBot`。
- 不迁移旧 BoltDB 或 SQLite 数据，切换后视为新空库。
- 启动后自动创建或迁移所需表结构，并 seed 系统 Prompt、内置脚本、默认浏览器实例和默认用户。
- 原 BoltDB 承担的脚本、配置、用户、Cookie、Agent、MCP、调度等数据全部进入 PostgreSQL。
- 原 SQLite/GORM 承担的 Project、Version、Page、PageScript、TestCase、LLMRefinement、TestExecution、ProjectAuthState 继续保留业务字段和行为，但底层连接改为 PostgreSQL。
- 通过操作清单、接口编译断言、静态扫描和契约红测防止遗漏数据库访问点。

## 二、不在 P4.6 范围内

P4.6 不实现：

- 旧 BoltDB 或 SQLite 数据迁移。
- 双读、双写或旧数据库 fallback。
- P5 多用户、成员角色、租户隔离或项目权限模型。
- 新的测试用例生成、执行、自然语言修改、录制体验能力。
- 登录态明文字段加密。P4.6 只延续 P4.5 已确认的“不通过普通响应、日志、Playbot job JSON 和执行报告泄露明文”契约。
- 发布部署自动建库脚本。可以在文档中说明 `CREATE DATABASE "PlayBot";`，但 P4.6 实现不要求拥有数据库管理员权限。

## 三、当前事实

当前仓库存在两套生产存储入口：

- `backend/storage/bolt.go`：BoltDB 保存脚本、LLM 配置、浏览器配置、浏览器实例、Cookie、脚本执行记录、录制配置、Agent 会话和消息、工具配置、MCP 服务、用户、API Key、定时任务和任务执行记录。
- `backend/storage/sqlite.go`：全局 `storage.DB` 使用 SQLite/GORM 保存测试平台核心业务数据。
- `backend/main.go`：启动时先初始化 BoltDB，再按 BoltDB 文件目录初始化 SQLite。
- `backend/api/project_*.go`：P1-P4.5 测试平台接口大量直接使用全局 `storage.DB`。
- `backend/agent`、`backend/services/browser`、`backend/scheduler`、`backend/mcp`、`backend/llm`、`backend/sdk` 等模块直接持有或传递 `*storage.BoltDB`。
- `backend/cmd/fix-headless` 是直接读取 BoltDB 文件的维护命令，P4.6 后必须删除、废弃或改为 PostgreSQL 版本，不能继续作为生产代码依赖 BoltDB。

这些入口都必须纳入 P4.6 清理范围。只替换 `main.go` 启动数据库不算完成。

## 四、配置契约

### 4.1 配置字段

`backend/config.example.toml` 应新增：

```toml
[database]
type = "postgres"
dsn = "postgres://user:password@localhost:5432/PlayBot?sslmode=disable"
```

`database.path` 只允许作为旧本地配置兼容字段存在，不作为 P4.6 生产入口。

真实本地 DSN 不提交到仓库，应放入已忽略的 `backend/config.local.toml` 或由启动参数指定的私有配置文件。

### 4.2 数据库名称

数据库名称固定为 `PlayBot`。

如果使用 SQL 创建数据库，必须写成：

```sql
CREATE DATABASE "PlayBot";
```

原因是 PostgreSQL 会把未加引号的标识符折叠为小写；`CREATE DATABASE PlayBot;` 实际会创建 `playbot`，不符合本阶段约定。

### 4.3 连接失败

配置缺少 DSN、数据库类型不是 `postgres`、DSN 指向的数据库名称不是 `PlayBot`，生产启动必须失败并给出明确错误。不能静默退回 BoltDB、SQLite、内存库或自动创建另一套本地文件数据库。

### 4.4 LLM API Key 加密

`docs/CORE_REQUIREMENTS.md` 明确要求不在数据库或日志中明文保存 LLM API Key。P4.6 必须把该要求纳入 PostgreSQL 迁移硬契约。

LLM 配置导入或保存时：

- `llm_configs` 表不得保存明文 `api_key` 列。
- 配置文件中的明文 API Key 只能作为导入输入，写入 PostgreSQL 前必须加密为密文。
- 加密密钥不得保存在数据库中；本地开发可通过 `backend/config.local.toml` 或环境变量提供，示例配置只能放占位值。
- 缺少加密密钥且存在需要落库的 LLM API Key 时，启动或保存必须失败，不能降级保存明文。
- Store 返回给运行时 LLM 调用链路的配置可以包含解密后的可用 API Key，但普通 API 响应、日志、Playbot job JSON、错误摘要和数据库原始字段不得出现明文。
- 如果后续选择外部 secret 引用模式，也必须满足数据库只保存引用值，不保存明文 key；P4.6 默认按密文落库实现和验收。

LLM API Key 加密密钥入口固定如下：

- 配置字段：`[security] llm_api_key_encryption_key`。
- 环境变量：`BROWSERWING_LLM_API_KEY_ENCRYPTION_KEY`。
- 优先级：环境变量高于配置文件字段。
- 格式：base64 编码的 32 字节随机密钥，解码后长度必须严格等于 32 字节。
- 示例配置只能写占位值或注释，不得提交真实密钥。
- 该密钥只能用于 LLM API Key 加密，不能复用 `auth.app_key`、数据库密码、JWT secret 或其他业务密钥。
- 密钥缺失、base64 非法或解码后长度不是 32 字节时，只要存在需要保存或导入的 LLM API Key，就必须拒绝启动或保存。

P4.6 不改变 P1/P4 已确认的 Playbot CLI 调用方式。后端仍可以把解密后的 API Key 作为受控子进程参数 `--llm-api-key` 传给 Playbot CLI；该值不得写入 Playbot job JSON、日志、错误摘要、HTTP 响应或数据库。命令日志和 stderr/stdout 摘要必须继续执行脱敏。若后续要禁止 CLI 参数传递密钥，必须单独设计新的 secret 传递机制，不属于 P4.6。

## 五、存储架构

### 5.1 Store 接口

P4.6 必须新增统一 Store 契约。生产模块依赖 `storage.Store` 或细分领域接口，不再依赖 `*storage.BoltDB` 或全局 `storage.DB`。

Store 至少包含以下领域：

- ScriptStore
- ScriptExecutionStore
- LLMConfigStore
- BrowserConfigStore
- BrowserInstanceStore
- CookieStore
- RecordingConfigStore
- PromptStore
- AgentStore
- ToolConfigStore
- MCPStore
- AuthStore
- SchedulerStore
- TestingPlatformStore

PostgreSQL 实现必须有编译断言：

```go
var _ storage.Store = (*PostgresStore)(nil)
```

如果后续新增 Store 方法但 `PostgresStore` 未实现，应由编译直接失败。

### 5.2 TestingPlatformStore

P4.6 的重点是统一数据库和去掉全局状态，不强制一次性把 P1-P4.5 每条 GORM 查询拆成大量细粒度 repository 方法。

允许 `TestingPlatformStore` 暴露受控的 `GormDB()` 或等价事务入口，但必须满足：

- 只能通过注入到 Handler/ProjectHandlers 的 Store 访问。
- 生产代码不得再引用包级全局 `storage.DB`。
- 测试 helper 可以注入独立 Store 或独立 GORM 连接，不能改写全局变量。
- P5 开始做权限隔离时，再按权限边界把 TestingPlatformStore 细分为项目、页面、用例、执行记录等 repository 方法。

### 5.3 启动链路

P4.6 启动顺序：

1. 读取配置。
2. 校验 `database.type = "postgres"`。
3. 校验 DSN 数据库名称为 `PlayBot`。
4. 打开 PostgreSQL GORM 连接。
5. 执行统一 AutoMigrate。
6. 构造 `PostgresStore`。
7. 构造 LLM manager，并从配置文件导入或加载 LLM 配置到 Store 和内存管理器。
8. 检查并升级系统 Prompt。
9. 加载内置脚本。
10. 初始化默认浏览器实例。
11. 如果认证启用，初始化默认用户。
12. 将同一个 Store 注入 API、LLM、browser manager、Agent、MCP、scheduler 和 SDK 内部客户端。

启动时导入或加载配置文件中的 LLM 配置是硬契约。新空 `PlayBot` 库只在 `config.toml` 中配置 LLM 时，P1 生成和 P4 refine 仍必须能读取到可用的默认或指定 LLM 配置。实现不能只初始化 Prompt、内置脚本、浏览器实例和用户后就启动服务。

禁止在启动链路中初始化 BoltDB 或 SQLite。

## 六、数据模型和表设计原则

### 6.1 总原则

- 可查询、过滤、排序、关联或约束的字段必须拆成 PostgreSQL 列。
- 不参与查询的复杂结构可以使用 JSONB。
- 不允许用单张 key/value 表模拟 BoltDB bucket。
- 不允许把完整业务对象全部塞进 JSONB 后只按 ID 查询，除非该对象在当前业务中没有列表、过滤、排序或约束需求。
- 主键类型应保持现有 API 语义：原 string ID 继续 string 主键；现有测试平台 uint ID 继续保持现有 JSON/API 语义。

### 6.2 原 BoltDB 数据域

以下数据域必须进入 PostgreSQL：

| 领域 | 建议表 | 结构化字段 | JSONB 字段 |
|------|--------|------------|------------|
| Script | scripts | id, name, description, url, group, duration, can_publish, can_fetch, requires_login, is_mcp_command, mcp_command_name, mcp_command_description, created_at, updated_at | actions, tags, downloaded_files, mcp_input_schema, variables |
| ScriptExecution | script_executions | id, script_id, script_name, instance_id, instance_name, start_time, end_time, duration, success, message, error_msg, total_steps, success_steps, failed_steps, video_path, created_at | extracted_data |
| LLMConfig | llm_configs | id, name, provider, api_key_ciphertext, api_key_nonce, api_key_key_id, model, base_url, is_default, is_active, created_at, updated_at | 无 |
| BrowserConfig | browser_configs | id, name, description, is_default, url_pattern, user_agent, use_stealth, headless, no_sandbox, proxy, created_at, updated_at | launch_args |
| BrowserInstance | browser_instances | id, name, description, is_default, is_active, type, bin_path, user_data_dir, control_url, user_agent, use_stealth, headless, no_sandbox, proxy, created_at, updated_at | launch_args |
| CookieStore | cookie_stores | id, platform, created_at, updated_at | cookies |
| RecordingConfig | recording_configs | id, enabled, frame_rate, quality, format, output_dir, created_at, updated_at | 无 |
| Prompt | prompts | id, name, description, content, type, version, created_at, updated_at | 无 |
| AgentSession | agent_sessions | id, llm_config_id, created_at, updated_at | 无 |
| AgentMessage | agent_messages | id, session_id, role, content, timestamp | tool_calls |
| ToolConfig | tool_configs | id, name, type, description, enabled, script_id, created_at, updated_at | parameters |
| MCPService | mcp_services | id, name, description, type, command, url, enabled, status, tool_count, last_error, created_at, updated_at | args, env |
| MCPServiceTools | mcp_service_tools | service_id, updated_at | tools |
| User | users | id, username, password, created_at, updated_at | 无 |
| ApiKey | api_keys | id, name, key, description, user_id, created_at, updated_at | 无 |
| ScheduledTask | scheduled_tasks | id, name, description, enabled, schedule_type, schedule_config, execution_type, script_id, script_name, browser_instance_id, agent_prompt, agent_llm_id, agent_llm_name, agent_session_id, result_dir, last_execution_time, next_execution_time, last_execution_status, execution_count, success_count, failed_count, created_at, updated_at | script_variables |
| TaskExecution | task_executions | id, task_id, task_name, start_time, end_time, duration, success, message, error_msg, execution_type, script_id, agent_session_id, created_at | result_data |

### 6.3 测试平台数据域

以下现有 GORM 模型继续保留业务语义：

- Project
- ProjectVersion
- TestPage
- PageScript
- TestCase
- LLMRefinement
- TestExecution
- ProjectAuthState

P4.6 只改底层连接到 PostgreSQL，不改变 P1-P4.5 API 响应和错误语义。

P5 所需 `OwnerUserID`、`TenantID`、`ProjectMember` 不在 P4.6 硬契约中。P4.6 可以预留迁移空间，但红测不得要求多用户权限行为。

## 七、必须保留的业务不变量

P4.6 实现必须保留以下行为：

- P1 生成失败不破坏已有 TestCase。
- P1 `preview` 不写数据库，`append` 追加，`replace` 事务覆盖。
- P2 列表不返回完整 Blueprint 和 ScriptContent。
- P2 腐坏 Blueprint 返回错误，不静默伪造空对象。
- P2 `active` TestCase 必须有非空 steps，`draft` 允许不完整。
- P3 只解释 Blueprint，不执行 ScriptContent，不把 ScriptContent 当 fallback。
- P3 非 active 或不可执行 Blueprint 在执行前拒绝，不创建 TestExecution。
- P3 执行记录列表和详情保持原排序、摘要和腐坏报告错误语义。
- P4 refine 不覆盖 TestCase，apply 才更新 TestCase。
- P4 stale apply 必须拒绝，失败不能部分写入。
- P4 LLM 配置读取继续使用统一 Store 中的 LLM 配置事实源，不允许新增临时配置源。
- P4.5 旧 Blueprint 缺 `auth_context` 仍按 `clean` 兼容。
- P4.5 显式 `project_saved` 且缺少 active 项目登录态时，执行前失败且不创建 TestExecution。
- P4.5 登录态明文不得出现在普通响应、日志、Playbot job JSON 或执行报告中。

## 八、防遗漏机制

P4.6 的核心验收不是“能连上 PostgreSQL”，而是证明没有遗漏任何旧存储入口。

### 8.1 Store Operation Inventory

必须在代码中维护 Store Operation Inventory，记录每个 Store 方法：

- 领域。
- 方法名。
- 对应原 BoltDB 方法或原 `storage.DB` 业务入口。
- 对应 PostgreSQL 表或事务入口。
- 对应红测名称。

Inventory 必须覆盖本文第九节的完整操作清单。

### 8.2 编译防遗漏

PostgresStore 必须完整实现 Store 接口。新增接口方法但未实现时，编译必须失败。

所有生产构造函数应接收 Store 或细分接口，不再接收 `*storage.BoltDB`。

### 8.3 静态扫描防遗漏

`go test ./...` 必须包含静态扫描测试，至少检查：

- 生产代码不得导入 `go.etcd.io/bbolt`。
- 生产代码不得导入 `github.com/glebarez/sqlite`。
- 生产代码不得出现 `storage.NewBoltDB`。
- 生产代码不得出现 `storage.InitSQLite`。
- 生产代码不得出现 `*storage.BoltDB`。
- 生产代码不得出现全局 `storage.DB`。
- `backend/main.go` 不得出现 BoltDB 或 SQLite 初始化。
- `backend/sdk` 生产路径不得打开 BoltDB 文件。
- `backend/cmd/fix-headless` 不得继续依赖 BoltDB；实现者应删除、废弃或改写。

扫描范围应排除：

- `*_test.go` 中的测试替身。
- `docs/`。
- 明确标记为历史说明的文档。
- `backend/sdk/examples` 的生成依赖文件可单独处理，但生产 SDK 源码不得依赖 BoltDB。

### 8.4 契约测试防遗漏

用例编写者必须先写红测，证明：

- Inventory 与 Store 接口同步。
- Inventory 中每个方法都有对应测试名称。
- 旧入口扫描当前会失败。
- PostgreSQL 配置必须指向 `PlayBot`。
- PostgreSQL Store 对 JSONB、排序、默认唯一、删除级联、分页过滤和敏感字段边界的行为正确。

### 8.5 反向发现遗漏

红测不能只按设计文档正向覆盖，还必须能从现有代码反向发现遗漏。

用例编写者应增加以下校验：

- Legacy 方法基线：测试内维护当前 `backend/storage/bolt.go` 的公开方法名快照，逐一要求 Store 接口和 Inventory 覆盖；如果某个旧方法没有进入 Store 或 Inventory，测试失败。
- 测试平台访问基线：扫描 `backend/api/project_*.go` 中现有 `storage.DB` 访问文件，要求迁移后这些生产文件不再引用全局入口。
- 遗留常量处理：如果扫描发现旧 bucket 常量但没有公开方法和生产调用，应在红测说明中记录为“无业务入口，不迁移”；一旦存在生产调用，必须补入 Store 和 Inventory。
- 模型字段 roundtrip：为原 BoltDB 模型构造“全字段非零”样本，经 PostgreSQL Store 保存后读取，并用结构体等价比较证明字段没有丢失。
- PostgreSQL schema 校验：对必须结构化的字段查询 `information_schema.columns`，确认字段被建为列，而不是被塞进 JSONB。
- 启动 seed 校验：空 `PlayBot` 库启动初始化后，应能读到配置文件导入的 LLM 配置、系统 Prompt、内置脚本和默认浏览器实例。

这些测试用于发现“设计者或开发者忘了某个字段/方法/启动步骤”的问题，不能只验证开发者已经实现的新路径。

## 九、完整数据库访问操作清单

本节是 P4.6 的防遗漏基线。用例编写者和开发者不得自行删减。

### 9.1 Script

- `SaveScript`
- `GetScript`
- `ListScripts`
- `UpdateScript`
- `DeleteScript`

验收重点：

- description 和 mcp_command_description 必须作为结构化字段保存和读取，不能丢失，也不能塞进无约束 JSONB。
- actions、tags、downloaded_files、mcp_input_schema、variables JSONB roundtrip 不丢字段。
- 列表排序保持现有行为。
- 删除脚本后，关联 script 类型 ToolConfig 必须按既有行为清理。

### 9.2 ScriptExecution

- `SaveScriptExecution`
- `GetScriptExecution`
- `GetLatestScriptExecutionByScriptID`
- `ListScriptExecutions`
- `DeleteScriptExecution`
- `DeleteScriptExecutionsByScriptID`

验收重点：

- 按 script_id 隔离。
- 最新执行记录按时间语义返回。
- extracted_data JSONB roundtrip 不丢字段。

### 9.3 LLMConfig

- `SaveLLMConfig`
- `GetLLMConfig`
- `ListLLMConfigs`
- `UpdateLLMConfig`
- `DeleteLLMConfig`
- `GetDefaultLLMConfig`
- `ClearDefaultLLMConfig`

验收重点：

- 默认配置只允许一个。
- `GetDefaultLLMConfig` 只返回 active 且 default 的配置。
- `llm_configs` 原始数据库行不得包含配置中的明文 API Key，也不得存在用于保存明文的 `api_key` 列。
- Store 返回给 P1 生成、P4 refine 和 LLM manager 的运行时配置必须能解密或解析到可用 API Key。
- 缺失、禁用、字段不完整、密文损坏或解密失败时，P1/P4 Playbot 调用仍返回明确错误且不泄露 API Key。

### 9.4 BrowserConfig

- `SaveBrowserConfig`
- `GetBrowserConfig`
- `GetDefaultBrowserConfig`
- `ListBrowserConfigs`
- `DeleteBrowserConfig`

验收重点：

- 默认配置只允许一个。
- launch_args JSONB roundtrip。
- 删除默认配置行为保持当前错误语义。

### 9.5 BrowserInstance

- `SaveBrowserInstance`
- `GetBrowserInstance`
- `ListBrowserInstances`
- `GetDefaultBrowserInstance`
- `UpdateBrowserInstance`
- `DeleteBrowserInstance`

验收重点：

- 默认实例只允许一个。
- 默认实例不可删除。
- 启动时默认浏览器实例 seed 行为保持不变。

### 9.6 CookieStore

- `SaveCookies`
- `GetCookies`
- `DeleteCookies`

验收重点：

- Cookie JSONB roundtrip。
- P4.5 项目录制和 TestCase 执行不得无条件使用全局 browser Cookie Store。
- Cookie 明文不得进入普通响应、日志、Playbot job JSON 和执行报告。

### 9.7 RecordingConfig

- `SaveRecordingConfig`
- `GetRecordingConfig`
- `GetDefaultRecordingConfig`

验收重点：

- 缺少默认配置时仍返回系统默认值并可保存。

### 9.8 Prompt

- `SavePrompt`
- `GetPrompt`
- `ListPrompts`
- `UpdatePrompt`
- `DeletePrompt`
- `CheckAndUpdateSystemPrompts`

验收重点：

- 系统 Prompt 版本落后且未被用户修改时自动升级。
- 用户修改过的系统 Prompt 不被自动覆盖。

### 9.9 Agent

- `SaveAgentSession`
- `GetAgentSession`
- `ListAgentSessions`
- `DeleteAgentSession`
- `SaveAgentMessage`
- `GetAgentMessage`
- `ListAgentMessages`

验收重点：

- 删除会话必须删除该会话所有消息。
- 会话列表按更新时间倒序。
- 消息列表按时间正序。
- tool_calls JSONB roundtrip。

### 9.10 ToolConfig

- `SaveToolConfig`
- `GetToolConfig`
- `ListToolConfigs`
- `DeleteToolConfig`
- `DeleteToolConfigByScriptID`

验收重点：

- parameters JSONB roundtrip。
- `DeleteToolConfigByScriptID` 只删除目标 script 的工具配置。
- 列表排序保持当前名称排序语义。

### 9.11 MCPService

- `SaveMCPService`
- `GetMCPService`
- `ListMCPServices`
- `DeleteMCPService`
- `SaveMCPServiceTools`
- `GetMCPServiceTools`

验收重点：

- args、env、tools、schema JSONB roundtrip。
- service tools 与 service_id 严格绑定。
- 删除服务后不应残留可读取 tools。

### 9.12 Auth

- `CreateUser`
- `GetUser`
- `GetUserByUsername`
- `ListUsers`
- `UpdateUser`
- `DeleteUser`
- `CreateApiKey`
- `GetApiKey`
- `GetApiKeyByKey`
- `ListApiKeys`
- `ListApiKeysByUser`
- `UpdateApiKey`
- `DeleteApiKey`

验收重点：

- username 唯一。
- API Key 唯一。
- JWT 和 API Key 中间件继续使用同一 Store 事实源。
- 返回用户信息时不得暴露密码。

### 9.13 Scheduler

- `CreateScheduledTask`
- `GetScheduledTask`
- `UpdateScheduledTask`
- `DeleteScheduledTask`
- `ListScheduledTasks`
- `ListScheduledTasksWithPagination`
- `CreateTaskExecution`
- `GetTaskExecution`
- `DeleteTaskExecution`
- `ListTaskExecutions`
- `ListTaskExecutionsWithPagination`
- `BatchDeleteTaskExecutions`

验收重点：

- scheduled task 按创建时间倒序。
- task execution 按开始时间倒序。
- 分页、搜索、task_id 过滤、success 过滤行为保持不变。
- script_variables、result_data JSONB roundtrip。

### 9.14 TestingPlatform

必须覆盖 P1-P4.5 已有业务数据访问：

- Project 列表、创建、删除。
- ProjectVersion 创建、更新、删除、克隆。
- TestPage 列表、创建、删除、保存录制。
- PageScript 主流程读取、录制元数据保存。
- TestCase 列表、创建、详情、更新、删除。
- TestCase generate 的 preview、append、replace。
- TestExecution 创建、列表、详情。
- LLMRefinement 创建、列表、详情、apply、discard。
- ProjectAuthState 摘要读取、捕获保存、删除、active 状态读取、执行前恢复输入构造。

验收重点：

- 不再通过全局 `storage.DB` 访问。
- 结构层级校验保持不变。
- 所有事务边界保持不变，失败不部分写入。

## 十、用例编写者红测要求

用例编写者应先读：

- `docs/P4_6_POSTGRES_STORAGE_MIGRATION_DESIGN.md`
- `docs/CORE_REQUIREMENTS.md`
- `docs/DEVELOPMENT_PLAN.md`
- `docs/CONTRACT_WORKFLOW.md`
- `docs/CONTRACT_RECORDS.md`
- `backend/storage/bolt.go`
- `backend/storage/sqlite.go`
- `backend/models/testing.go`
- P1-P4.5 现有后端契约测试

### 10.1 清单完整性红测

建议测试：

- `TestP46StoreOperationInventoryMatchesStoreInterface`
- `TestP46StoreOperationInventoryHasContractTestNames`
- `TestP46LegacyBoltMethodsAreCoveredByStoreInventory`
- `TestP46RequiredStructuredFieldsAreDeclaredInSchemaPlan`

期望：

- Store 接口中每个公开方法都在 Inventory 中。
- Inventory 中每个方法都声明对应 contract test 名称。
- Inventory 不允许记录不存在的方法。
- 当前 BoltDB 公开方法快照中的每个方法都能在 Store 接口和 Inventory 中找到归属。
- Script 的 `description`、`mcp_command_description` 等已确认业务字段必须被声明为结构化列。

边界：

- 该测试只验证清单和接口同步，不要求连接真实 PostgreSQL。
- 如果开发者新增 Store 方法但未更新清单，测试必须失败。

### 10.2 禁止旧入口红测

建议测试：

- `TestP46ProductionCodeDoesNotUseBoltDBOrSQLite`
- `TestP46ProductionCodeDoesNotUseGlobalStorageDB`
- `TestP46MainInitializesOnlyPostgresStore`
- `TestP46SDKDoesNotOpenBoltDB`

期望：

- 当前生产代码中的旧入口会让红测失败。
- 开发完成后，生产代码不再出现旧入口。

边界：

- 扫描测试应排除 `*_test.go` 和文档。
- 不应因为历史文档文字或测试替身失败。

### 10.3 PostgreSQL 配置红测

建议测试：

- `TestP46DatabaseConfigDefaultsToPostgresPlayBot`
- `TestP46RejectsNonPostgresDatabaseType`
- `TestP46RejectsDSNWithoutPlayBotDatabase`
- `TestP46ExampleConfigUsesPlayBotDSN`
- `TestP46StartupImportsLLMConfigsFromConfigIntoEmptyPlayBot`
- `TestP46RejectsLLMAPIKeyPersistenceWithoutSecretKey`
- `TestP46LLMAPIKeyEncryptionKeySourcePrecedence`
- `TestP46RejectsInvalidLLMAPIKeyEncryptionKey`
- `TestP46DoesNotReuseAuthAppKeyForLLMEncryption`

期望：

- 默认和示例配置指向 PostgreSQL `PlayBot`。
- 非 PostgreSQL 类型启动失败。
- DSN 数据库名不是 `PlayBot` 时启动失败。
- 空 `PlayBot` 库启动时，配置文件中的 LLM 配置会进入 Store 并可被 P1/P4 读取为默认或指定启用配置。
- 缺少加密密钥且配置中存在 LLM API Key 时，启动或保存失败，不得保存明文。
- `BROWSERWING_LLM_API_KEY_ENCRYPTION_KEY` 优先于 `[security] llm_api_key_encryption_key`。
- 加密密钥必须是 base64 编码的 32 字节随机密钥；非法 base64 或解码长度不等于 32 字节时拒绝。
- 即使 `auth.app_key` 存在，也不能作为 LLM API Key 加密密钥 fallback。

边界：

- 不要求测试自动创建数据库。
- 不要求真实 DSN 提交到仓库。

### 10.4 PostgreSQL Store 行为红测

建议测试分组：

- `TestP46PostgresStoreScriptRoundTrip`
- `TestP46PostgresStoreLLMDefaultUniqueness`
- `TestP46PostgresStoreLLMAPIKeyEncryptedAtRest`
- `TestP46PostgresStoreLLMAPIKeyDecryptsForRuntime`
- `TestP46PlaybotJobJSONDoesNotContainLLMAPIKey`
- `TestP46PlaybotCLIAllowsRedactedLLMAPIKeyArgument`
- `TestP46PostgresStoreBrowserDefaultUniqueness`
- `TestP46PostgresStoreCookieRoundTrip`
- `TestP46PostgresStorePromptSystemUpgrade`
- `TestP46PostgresStoreAgentCascadeMessages`
- `TestP46PostgresStoreToolConfigByScriptDeletion`
- `TestP46PostgresStoreMCPServiceToolsRoundTrip`
- `TestP46PostgresStoreAuthLookupAndUniqueness`
- `TestP46PostgresStoreSchedulerPaginationAndFilters`

期望：

- 使用 `backend/config.local.toml` 或测试专用配置连接 PostgreSQL `PlayBot`。
- 每个测试独立清理自身创建的数据。
- JSONB 字段 roundtrip 后结构和值一致。
- 原 BoltDB 模型全字段样本 roundtrip 后结构和值一致；Script 样本必须覆盖 `description` 和 `mcp_command_description`。
- LLM API Key 写入后，直接查询 PostgreSQL 原始字段不得等于或包含配置明文；通过 Store 读取给运行时使用时，应能得到原始可用密钥。
- `information_schema.columns` 中不得存在用于保存明文的 `llm_configs.api_key` 列，必须存在密文字段。
- Playbot job JSON 中不得包含 LLM API Key；Playbot CLI 参数允许携带运行时 API Key，但日志、错误摘要和命令展示必须脱敏。
- 默认唯一、删除级联、分页过滤和排序与当前 BoltDB 行为一致。

边界：

- 不调用真实 LLM、真实 Playbot、真实外部浏览器。
- 不测试旧数据迁移。

### 10.5 业务回归红测

建议复用并扩展既有 P1-P4.5 契约测试，重点确认：

- 生成、管理、执行、自然语言修改、登录态恢复业务结果不因底层 PostgreSQL 迁移改变。
- 所有测试不再通过改写全局 `storage.DB` 建立测试状态。
- 测试使用注入 Store 或注入 GORM 连接。

### 10.6 不在红测范围

用例编写者不要在 P4.6 红测中要求：

- 多用户权限。
- 旧数据迁移。
- 登录态加密。
- 真实 Docker Compose 或 Testcontainers 自动拉库。
- 前端 UI 新功能。

## 十一、业务开发者实现要求

业务开发者应在红测通过审核后实现，顺序建议如下：

1. 新增 PostgreSQL 配置字段和 DSN 校验。
2. 新增 Store 接口、Inventory 和静态扫描测试。
3. 新增 PostgreSQL GORM 初始化和统一 AutoMigrate。
4. 为原 BoltDB 数据域补 GORM 模型标签和 JSONB 字段。
5. 实现 `PostgresStore`。
6. 替换 `main.go` 启动链路。
7. 替换 API、LLM、browser manager、Agent、MCP、scheduler、SDK 中的 `*storage.BoltDB` 依赖。
8. 移除生产 BoltDB 和 SQLite 初始化文件或将其从生产构建中删除。
9. 更新测试 helper，改为注入 Store 或测试 GORM 连接。
10. 跑后端、前端和 Playbot 标准验证。

实现约束：

- 不允许隐藏 fallback。
- 不允许为了通过测试保留旧数据库入口。
- 不允许在业务模块中直接拼接 SQL 处理外部输入。
- 不允许为测试路径、测试 ID、测试用户写特殊逻辑。
- 不允许吞掉 PostgreSQL 连接或迁移错误后继续启动。

## 十二、验证入口

P4.6 收口至少需要：

```powershell
cd backend
go test ./...
```

PostgreSQL 契约测试应使用本地私有配置，例如：

```powershell
cd backend
go test ./... -args -config config.local.toml
```

前端仍需验证：

```powershell
cd frontend
pnpm run type-check
pnpm run build
```

Playbot 仍需验证：

```powershell
cd playbot-engine
$env:UV_PROJECT_ENVIRONMENT = "D:\depends\python\venvs\browserwing-playbot"
uv lock --check
uv sync --all-extras
D:\depends\python\venvs\browserwing-playbot\Scripts\python.exe -c "import cli; print('ok')"
```

如果本机没有 PostgreSQL `PlayBot`，审核者不能给 P4.6 最终通过结论，只能给出“非 PG 集成部分通过”的有限结论。

## 十三、代码审核重点

代码审核者应重点检查：

- 是否仍有生产路径引用 BoltDB、SQLite 或全局 `storage.DB`。
- Store Operation Inventory 是否覆盖所有旧 BoltDB 方法和测试平台数据访问。
- PostgreSQL DSN 是否固定校验 `PlayBot`。
- JSONB 字段是否只用于复杂载荷，查询字段是否拆列。
- 默认唯一和删除级联是否依赖事务或数据库约束，而不是脆弱的内存扫描。
- P1-P4.5 业务契约是否未被改写。
- 敏感字段是否没有进入普通响应、日志、Playbot job JSON 和执行报告。
- 测试是否真的证明旧入口被清除，而不是只覆盖新 Store。

## 十四、阶段完成定义

P4.6 完成时必须满足：

- 设计经过业务开发者可行性 review。
- 用例编写者红测已通过代码审核。
- 业务开发者实现后，旧入口静态扫描测试通过。
- `PostgresStore` 完整实现 Store 接口并通过编译断言。
- PostgreSQL `PlayBot` 契约测试通过。
- P1-P4.5 业务回归测试通过。
- 前端 type-check 和 build 通过。
- Playbot 环境验证通过。
- `docs/DEVELOPMENT_PLAN.md` 和 `docs/CONTRACT_RECORDS.md` 已按最终实现收口更新。
