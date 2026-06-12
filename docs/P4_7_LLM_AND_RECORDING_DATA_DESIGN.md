# P4.7 LLM 统一配置和录制数据管理详细设计

本文档定义 P4.7 阶段的业务契约、数据边界和红测口径。P4.7 插在 P4.6 PostgreSQL 统一存储迁移之后、P4.8 端到端生成体验收口之前，目标是先把 LLM 能力和录制过程数据收束成可管理、可校验、可审计的系统能力。

P4.7 不直接做最终体验拉通，不做 P5 多用户成员权限。它解决的是底座问题：后续“页面录制 -> 智能生成用例”和 P5 权限隔离不能继续依赖分散的 LLM 入口、页面 `sessionStorage` 草稿或裸磁盘路径。

## 阶段协作要求

P4.7 必须继续遵守项目契约流程，不能由开发者直接按本文档实现生产代码。

执行顺序：

1. 规划者维护并定稿本文档，明确业务契约、数据模型、API、错误处理和验收口径。
2. 业务开发者先 review 本文档，只反馈实现成本、现有代码约束、浏览器录制同步风险和存储结构风险。
3. 用例编写者在设计确认后先写契约红测，红测必须覆盖 LLM 配置选择、启停、密钥脱敏、RecordingSession 生命周期、草稿动作持久化、PageScript 生成、取消失败保护和 RecordingArtifact 元数据。
4. 代码审核者先审核红测是否符合本文档，确认没有把当前 `sessionStorage`、裸 `/files/recordings` 静态目录、固定测试页面或偶然前端路由写成业务契约。
5. 业务开发者只能在红测审核通过后实现生产代码；实现中如发现本文档不可行，应退回规划者修订，而不是在代码里改写契约。
6. 代码审核者最终复核实现和测试，并按影响面运行后端、前端、Playbot 和必要浏览器录制冒烟验证。
7. 规划者阶段收尾，更新 `docs/DEVELOPMENT_PLAN.md`，并在 P4.7 审核通过后再更新 `docs/CONTRACT_RECORDS.md`。

红测优先级：

- 第一优先级是保证 LLM 配置只有一个事实源，所有 AI 能力共用同一套启用、默认、字段完整和密钥脱敏规则。
- 第二优先级是保证录制草稿动作和录制状态进入数据库，`sessionStorage` 只能作为页面临时缓存。
- 第三优先级是为 P5 预留录制产物归属和受控访问边界，避免裸磁盘路径成为权限绕过点。

## 一、当前事实

当前实现已经具备部分基础能力：

- LLM 配置已有后端 API、前端管理页和 PostgreSQL 加密存储。
- Playbot 生成用例和自然语言精修已经可以选择默认或显式 LLM 配置。
- AI Explorer 和录制页 AI 能力也在使用 LLM 配置，但前端入口、错误口径和可用性提示仍不统一。
- 测试平台最终主流程录制 `PageScript` 已保存到数据库，字段包括 `ActionTrace`、`DOMSnapshot` 和 `RecordingMetaJSON`。
- 录制过程中的页面动作仍主要依赖浏览器页面内存和 `sessionStorage`，后端在停止录制时再同步。
- 录屏帧、截图、下载文件等大文件仍按本机目录保存，`/files/recordings` 这类裸静态路径缺少业务归属和权限入口。

因此 P4.7 不是重写现有录制和生成能力，而是补齐统一配置、录制会话和产物元数据三块底座。

## 二、阶段目标

P4.7 完成后应满足：

- LLM 配置是 Playbot 生成、Playbot refine、录制页 AI 自动提取、AI Explorer 的唯一配置事实源。
- P4.7 新增最小系统管理员标识，用于区分“可以管理 LLM 配置”的系统管理员和“只能使用启用模型”的普通用户。
- 全局管理员可以维护多个 LLM 配置，设置默认配置，启用或停用配置。
- 普通用户只能使用已启用配置，不能查看 API Key 明文。
- 所有 AI 能力在 LLM 缺失、停用或字段不完整时前置失败，不进入半执行状态。
- 页面录制开始时创建数据库 `RecordingSession`。
- 录制中的动作草稿可以按 session 持久化到数据库；页面刷新或前端重连后可以恢复会话摘要。
- 停止并保存录制时，由 `RecordingSession` 生成或替换当前页面有效 `PageScript`。
- 录制取消、失败、非法元数据不得替换旧 `PageScript`。
- 录屏、截图、下载文件等大文件由数据库保存元数据和归属，普通响应不返回任意本地绝对路径。
- P4.7 为 P5 项目权限校验预留 `created_by`、project/version/page scope 和受控 artifact 下载入口。

## 三、不在 P4.7 范围内

P4.7 不实现：

- P5 项目成员、角色权限、租户隔离或跨用户可见性过滤。P4.7 只允许新增最小系统管理员标识，用于保护 LLM 配置管理接口，不作为项目权限模型。
- 每用户或每项目 LLM API Key。P4.7 采用全局管理员配置、普通用户使用启用模型。
- Playbot 离线生成引擎和录制页 live browser AI 引擎合并。
- 把录屏帧、视频、截图、下载文件二进制直接写入 PostgreSQL。
- 录制到生成用例的最终交互路径优化。该体验收口属于 P4.8。
- 旧磁盘录制产物迁移。P4.7 只要求新产生的产物有数据库元数据。

## 四、LLM 配置契约

### 4.1 系统管理员身份

P4.7 需要补齐 LLM 配置管理的管理员身份契约，但不能提前实现 P5 项目权限。

最小契约：

- `User` 增加系统级布尔字段，例如 `is_admin`，默认值为 `false`。
- 启动 seed 的默认用户必须是 `is_admin = true`。
- 如果数据库中已经存在配置指定的默认用户名且系统内没有任何管理员，启动时可以把该用户提升为 `is_admin = true`，避免 P4.7 升级后锁死 LLM 管理入口。
- `is_admin` 只表示系统管理能力，P4.7 只用于 LLM 配置管理接口和相关前端入口。
- `is_admin` 不表示项目 owner、admin、editor 或 viewer，不参与 project、version、page、testcase、execution 的权限过滤。
- P5 多用户阶段如引入项目成员和角色，必须把项目角色与该系统管理员标识分开设计。

LLM 管理接口权限：

- 创建、更新、删除、测试、启用、停用、设置默认 LLM 配置必须要求 `is_admin = true`。
- LLM 配置管理页只对 `is_admin = true` 的用户展示可管理入口。
- 普通认证用户可以读取“可用于执行的启用配置摘要”，用于生成、refine、AI 提取和 AI Explorer 的模型选择。
- 普通认证用户不得读取停用配置作为可选项，不得执行管理动作，不得看到、导出或复制 API Key。
- 管理员读取配置列表时也不得返回 API Key 明文；管理员只能管理密钥输入或替换，不能通过普通响应取回旧密钥。

### 4.2 配置归属

LLM 配置是系统级能力：

- 配置由 `is_admin = true` 的全局管理员维护。
- 一个系统可以有多个 LLM 配置。
- 同一时间最多一个启用配置可以标记为默认配置。
- 停用配置不可被普通用户选择，也不能被默认解析命中。
- 普通用户可以在允许的 AI 功能中选择启用配置或使用默认配置，但不能查看、导出、复制 API Key。

P4.7 不引入每用户 API Key 或每项目 API Key。P5 如需扩展配置归属，必须基于 P4.7 的全局配置口径单独设计。

### 4.3 统一解析策略

所有 AI 能力必须通过同一套后端解析逻辑获取运行时 LLM 配置：

1. 请求显式传入 `llm_config_id` 时，按该 ID 读取配置。
2. 请求未传 `llm_config_id` 时，读取当前默认配置。
3. 配置不存在、已停用、缺少 base_url、缺少 model、缺少 API Key 或 API Key 解密失败时，返回明确错误。
4. 解析失败时不得调用 Playbot、AgentManager 或录制页 AI 执行逻辑。
5. 解析成功后，运行时可以拿到解密后的 API Key；普通 HTTP 响应、日志、错误摘要、Playbot job JSON、执行报告不得出现明文。

适用范围：

- Playbot 生成 TestCase。
- Playbot refine TestCase。
- 录制页 AI 自动提取或 AI 表单填充。
- AI Explorer。
- 脚本动作中的 AI 控制能力。

### 4.4 错误码口径

P4.7 后端应返回可区分的错误原因，便于前端给出配置入口：

- `llm_config_not_found`
- `llm_config_disabled`
- `llm_config_incomplete`
- `llm_config_missing_default`
- `llm_config_secret_unavailable`

前端不要求逐字展示这些 code，但必须能区分“需要去配置 LLM”和“当前选择不可用”。

### 4.5 密钥边界

P4.7 延续 P4.6 已确认安全边界：

- PostgreSQL 不保存明文 API Key。
- 普通 API 响应不返回明文 API Key。
- 日志、错误摘要、Playbot job JSON、执行报告不包含明文 API Key。
- P4.7 不改变受控 CLI 参数传递 `--llm-api-key` 的既有方式；若后续禁止 CLI 参数传递密钥，应单独设计 secret 传递机制。

## 五、RecordingSession 数据模型

P4.7 新增 `RecordingSession`，作为录制过程数据的数据库事实源。

建议字段：

- `id`：主键。
- `project_id`：所属 Project。
- `version_id`：所属 ProjectVersion。
- `page_id`：所属 TestPage。
- `recording_kind`：`login_flow` 或 `business_flow`。
- `auth_context`：只允许 `clean` 或 `project_saved`。
- `target_url`：本次录制打开的目标 URL。
- `status`：`recording`、`stopped`、`saved`、`cancelled`、`failed`。
- `actions_json`：当前草稿动作 JSON，保存完整动作数组。
- `action_count`：动作数量摘要。
- `dom_snapshot`：停止录制时捕获的页面快照，允许为空但不得伪造成真实快照。
- `recording_meta_json`：规范化后的录制元数据。
- `error_message`：失败原因摘要，不包含 Cookie、Storage value、API Key 等敏感值。
- `started_at`、`last_synced_at`、`stopped_at`、`saved_at`。
- `created_by`：预留 P5 用户归属；P4.7 可以为空或写入当前认证用户 ID。
- `created_at`、`updated_at`。

约束：

- `project_id`、`version_id`、`page_id` 必须通过层级校验。
- `recording_kind` 和 `auth_context` 必须符合 P4.5 契约；兼容语义只适用于旧 PageScript 缺少 `recording_meta` 时按 `business_flow + clean` 派生，不允许新的 RecordingSession 接受其他 `auth_context` 值。
- 同一个浏览器录制生命周期只能绑定一个 active `RecordingSession`。
- `status = saved` 后不得继续追加 actions。
- `status = cancelled` 或 `failed` 不得生成或替换 PageScript。

## 六、RecordingArtifact 数据模型

P4.7 新增 `RecordingArtifact`，用于管理录屏、截图、下载文件等大文件元数据。

建议字段：

- `id`：主键。
- `recording_session_id`：所属 RecordingSession。
- `project_id`、`version_id`、`page_id`：冗余 scope，便于 P5 权限过滤。
- `artifact_type`：`video_frame`、`screenshot`、`download`、`trace`、`other`。
- `storage_backend`：`local`，后续可扩展为对象存储。
- `storage_path`：后端内部相对路径或受控存储 key。
- `file_name`：用户可见文件名。
- `mime_type`。
- `size_bytes`。
- `sensitive`：是否可能包含敏感信息。
- `created_at`。

约束：

- 普通 API 响应不得返回任意本地绝对路径。
- 新业务不得依赖裸 `/files/recordings` 静态目录。
- 下载必须通过受控接口按 artifact id 读取，接口内校验 session 和 project/page scope。
- P4.7 可以继续把实际文件保存在本地目录，但数据库元数据必须是访问事实源。

## 七、录制数据流

### 7.1 开始录制

项目页面录制开始时：

1. 后端校验 project/version/page 层级。
2. 后端校验 `recording_kind` 和 `auth_context`，包括 `business_flow + project_saved` 必须存在 active 项目登录态。
3. 后端创建 `RecordingSession(status = recording)`。
4. 后端启动浏览器隔离上下文，并把 session id 绑定到当前录制生命周期。
5. 响应返回 `recording_session_id` 和会话摘要。

### 7.2 录制中同步

录制过程中：

- 页面内 `sessionStorage` 可以继续保存临时动作，防止单页刷新丢失。
- 后端应周期性或在事件触发时把动作数组同步到 `RecordingSession.actions_json`。
- 前端刷新或重连时，可以通过 session id 读取会话摘要和动作数量。
- 同步失败应记录错误并暴露可理解状态，不得静默让用户以为录制已被可靠保存。

### 7.3 停止录制

停止录制时：

1. 后端执行最后一次动作同步。
2. 后端捕获可用 DOMSnapshot 或语义快照。
3. 后端把 `RecordingSession` 更新为 `stopped`。
4. 如果本次产生截图、录屏帧、下载文件等产物，写入 `RecordingArtifact` 元数据。
5. 停止录制本身不必立即替换 `PageScript`；只有用户选择保存主流程时才生成或替换。

### 7.4 保存主流程

保存录制时：

1. 后端校验 `RecordingSession` 属于当前 project/version/page，且状态为 `stopped` 或仍可最终停止。
2. 后端校验并规范化 `recording_meta`。
3. 后端用 session 的 actions、snapshot 和 meta 生成新的 `PageScript`。
4. 后端在事务中删除当前页面旧主流程 `PageScript` 并创建新 `PageScript`。
5. 后端把 `RecordingSession.status` 更新为 `saved`。

兼容要求：

- 当前有效主流程定义仍是该页面当前保存的 `PageScript`。
- P1-P4.6 的生成、管理、执行和 refine 继续从 `PageScript` 读取上下文。
- 旧前端缺少 session id 的保存路径如仍保留，只能走兼容入口；新项目页面录制必须使用 `recording_session_id`。

### 7.5 取消和失败

取消录制时：

- `RecordingSession.status` 更新为 `cancelled`。
- 不生成新 PageScript。
- 不删除旧 PageScript。

录制失败时：

- `RecordingSession.status` 更新为 `failed`。
- `error_message` 只保存脱敏摘要。
- 不生成新 PageScript。
- 不删除旧 PageScript。

## 八、API 契约

P4.7 优先扩展现有项目页面录制接口，不新增平行业务入口。

建议接口：

- `POST /api/v1/projects/:id/versions/:vid/pages/:pid/recording-session`
  - 创建 RecordingSession 并启动录制。
  - 响应必须包含 `recording_session_id`。

- `GET /api/v1/projects/:id/versions/:vid/pages/:pid/recording-session/:sid`
  - 返回会话摘要、状态、动作数量、目标 URL、录制类型和会话模式。
  - 不返回 Cookie、Storage value、LLM API Key 或本地绝对路径。

- `POST /api/v1/projects/:id/versions/:vid/pages/:pid/recording-session/:sid/sync`
  - 同步 actions 草稿和可选 snapshot 摘要。
  - 只接受属于当前 session 的数据。

- `POST /api/v1/projects/:id/versions/:vid/pages/:pid/recording-session/:sid/stop`
  - 停止录制并更新 session 状态。

- `POST /api/v1/projects/:id/versions/:vid/pages/:pid/recordings`
  - 保存主流程。
  - 新路径必须要求 `recording_session_id`；缺失时只允许明确兼容旧前端的路径，并按 P4.5 legacy 规则处理。

- `GET /api/v1/recording-artifacts/:artifact_id/download`
  - 受控下载接口。
  - P4.7 至少校验 artifact 存在、scope 一致和认证存在；P5 再补角色权限。

## 九、前端要求

P4.7 前端只做必要能力露出，不做最终体验收口：

- LLM 配置管理页继续存在，并明确展示启用、停用、默认状态。
- 普通使用场景中的 LLM 选择器只展示可用配置，不展示 API Key。
- 项目录制页持有 `recording_session_id`。
- 项目录制页可以展示当前录制会话状态、动作数量和错误摘要。
- 页面刷新后，如果 URL 或上下文中有 session id，可以恢复会话摘要。
- 停止并保存时必须把 `recording_session_id` 传给保存接口。
- 录制失败或取消时，不提示用户旧主流程已更新。

P4.8 再负责把这些能力组织成完整、顺滑的用户路径。

## 十、给测试者的红测要求

### 10.1 LLM 配置红测

必须覆盖：

- 启动 seed 的默认用户具备 `is_admin = true`，普通新用户默认 `is_admin = false`。
- 非管理员调用 LLM 配置创建、更新、删除、测试、启用、停用或设置默认接口时返回权限错误，且不改变配置。
- 非管理员读取 LLM 选择列表时只能看到启用配置摘要，看不到停用配置和 API Key。
- 管理员读取 LLM 配置列表时可以看到启用/停用/default 状态，但仍看不到 API Key 明文。
- 没有默认启用 LLM 配置时，生成、refine、AI 提取前置失败，不调用下游执行器。
- 显式选择不存在配置时失败。
- 显式选择停用配置时失败。
- 配置缺少 base_url、model 或 API Key 时失败。
- 多个启用配置存在时，显式选择优先于默认配置。
- 普通响应、日志、Playbot job JSON、执行报告不包含明文 API Key。
- Playbot 生成和 refine、录制页 AI 提取、AI Explorer 使用同一个配置解析 helper 或同一套可验证规则。

### 10.2 RecordingSession 红测

必须覆盖：

- 开始录制创建 `RecordingSession(status = recording)`，并写入 project/version/page/kind/auth_context/target_url。
- 层级不匹配时不创建 RecordingSession。
- 非法 `recording_kind` 或 `auth_context` 不创建 RecordingSession。
- `auth_context` 只允许 `clean` 和 `project_saved`；`reuse_browser`、`auto` 或其他值必须拒绝。
- `business_flow + project_saved` 在缺少 active 项目登录态时不创建 RecordingSession。
- 录制中同步 actions 后，数据库保存完整动作数组和 action_count。
- 页面刷新或重连场景可以读取 session 摘要。
- `status = saved/cancelled/failed` 后拒绝继续同步 actions。

### 10.3 保存和失败保护红测

必须覆盖：

- 停止并保存合法 RecordingSession 时，生成新的 PageScript 并替换旧主流程。
- 保存时 `recording_meta` 写入 PageScript，且符合 P4.5 login_flow/business_flow/auth_context 规则。
- 取消录制不删除旧 PageScript。
- 录制失败不删除旧 PageScript。
- 非法 `recording_meta` 不删除旧 PageScript，不更新 RecordingSession 为 saved。
- 旧前端兼容路径如果保留，缺少 meta 时仍按 `business_flow + clean` 兼容，不得变成 `project_saved`。

### 10.4 RecordingArtifact 红测

必须覆盖：

- 停止录制产生的截图、录屏帧、下载文件写入 RecordingArtifact 元数据。
- 普通响应不返回本地绝对路径。
- 下载接口只能通过 artifact id 和 scope 读取，不依赖裸 `/files/recordings`。
- scope 不匹配时拒绝下载。

### 10.5 回归红测

必须覆盖：

- P1 生成用例继续从 PageScript 读取 ActionTrace、DOMSnapshot 和 recording_meta。
- P4 refine 缺少 PageScript 时仍按 warning 处理，不因 RecordingSession 缺失而失败。
- P4.5 登录态捕获、恢复和 Blueprint `auth_context` 继承契约继续通过。
- P4.6 PostgreSQL Store 静态防遗漏和核心数据访问契约继续通过。

## 十一、验收标准

P4.7 收口时必须满足：

- 相关后端契约红测通过。
- 前端类型检查和构建通过。
- Playbot 最小导入和既有 generate/refine 调用契约通过。
- 至少完成一次人工或自动化冒烟：开始项目页面录制、同步动作、停止、保存 PageScript、确认旧 PageScript 替换行为。
- `docs/DEVELOPMENT_PLAN.md` 已更新阶段状态。
- `docs/CONTRACT_RECORDS.md` 只在审核通过后更新，不在规划阶段提前写入。
