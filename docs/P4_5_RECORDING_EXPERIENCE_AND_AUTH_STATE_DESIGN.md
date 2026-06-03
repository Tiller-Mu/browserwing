# P4.5 录制体验与项目登录态详细设计

本文档定义 P4.5 阶段的业务契约和验收口径。P4.5 插在 P4 自然语言修改和 P5 多用户权限之间，目标是先把首个可交付版本的人工体验闭环补顺：页面管理从卡片改为列表，页面录制入口收敛到项目页面内，同时完整处理项目登录态，避免后续自动化执行因为缺少 Cookie、localStorage 或 sessionStorage 而跑不起来。

P4.5 不改变 P1-P4 已确认的生成、管理、执行、自然语言修改契约；它补充的是录制和执行前的浏览器会话上下文。

## 阶段协作要求

P4.5 必须继续遵守项目契约流程，不能由开发者直接按本文档实现生产代码。

执行顺序：

1. 规划者维护并定稿本文档，确保业务契约、API、数据流、错误处理和验收口径没有冲突。
2. 业务开发者先 review 本文档，只反馈实现成本、底层浏览器隔离能力、存储结构和兼容风险。
3. 用例编写者在设计确认后先写契约红测，红测必须覆盖登录态捕获、清洁会话、项目登录态恢复、PageScript 元数据、Blueprint `auth_context`、执行前校验和前端列表入口。
4. 代码审核者先审核红测是否符合本文档，确认没有把当前全局 `browser` Cookie Store、固定页面名、固定测试 token 或偶然 DOM 结构写成业务契约。
5. 业务开发者只能在红测审核通过后实现生产代码；实现中如发现本文档不可行，应退回规划者修订，而不是在代码里改写契约。
6. 代码审核者最终复核实现和测试，并按影响面运行后端、前端和必要的浏览器冒烟验证。
7. 规划者阶段收尾，更新 `docs/DEVELOPMENT_PLAN.md` 和 `docs/CONTRACT_RECORDS.md`。

红测优先级：

- 第一优先级是防止登录态串用：干净会话不能被项目登录态或全局 Cookie 污染。
- 第二优先级是保证自动化可跑：项目登录态必须覆盖 Cookie、localStorage、sessionStorage，并在执行首次导航前恢复。
- 第三优先级是保护敏感信息：API、日志、Playbot 输入和执行报告不得泄露登录态明文。

## 一、背景与当前事实

当前用户路径较长：

```text
进入项目 -> 进入版本页面 -> 创建页面 -> 跳到通用浏览器管理 -> 启动浏览器 -> 打开页面 -> 开始录制 -> 停止录制 -> 保存脚本 -> 回到页面 -> 智能生成用例
```

当前实现事实：

- `frontend/src/pages/TestPageManager.tsx` 使用页面卡片布局，页面数量变多后扫描效率下降。
- `frontend/src/pages/BrowserManager.tsx` 同时承载通用脚本、浏览器配置、实例、Cookie、录制和页面录制保存，页面录制用户会经过很多与测试页面无关的区域。
- 页面录制保存只写 `PageScript.ActionTrace` 和 `PageScript.DOMSnapshot`，没有记录本次录制使用的是干净会话还是已登录会话。
- 后端已有通用 Cookie Store，固定 ID 为 `browser`，启动浏览器时会尝试加载该 Cookie Store。
- 现有 Cookie Store 只覆盖 Cookie，不覆盖 localStorage、sessionStorage；很多业务系统把 token 放在 Web Storage 中。
- P3 执行请求和执行输入没有登录态上下文，执行报告也不记录本次执行是否使用登录态。

因此，P4.5 需要同时解决两个问题：

- 体验问题：页面多时要用列表扫描，录制入口要从页面行内直接进入短流程。
- 登录态问题：自动化执行必须能复用项目登录状态，但录制登录页时又必须能使用干净会话，不能被已保存登录态自动跳过登录页。

## 二、阶段目标

P4.5 完成后应满足：

- 用户在项目版本页面以列表方式管理业务页面。
- 每个页面行内可以直接执行“录制登录流程”“录制业务流程”“更新登录态”“智能生成”“新建用例”“查看用例”等操作。
- 项目版本可保存一份默认登录态，登录态覆盖 Cookie、localStorage、sessionStorage，并预留扩展字段承载后续更复杂的浏览器存储。
- 登录态只在用户明确选择使用时套用；录制登录流程和执行登录流程用例时必须使用干净会话。
- 录制出来的 PageScript 和生成出来的 TestCase Blueprint 都能保留会话上下文口径。
- 执行 TestCase 时根据 Blueprint 或请求中的 `auth_context` 决定是否应用项目登录态。
- Playbot 生成和 refine 不接收任何 Cookie、token 或登录态明文。
- 前端不展示 Cookie/token/localStorage value 明文，后端日志不得输出登录态明文。

## 三、名词与边界

### 项目登录态

项目登录态是为了让自动化测试复用人工登录后的浏览器认证状态而保存的敏感资产。

P4.5 中“项目登录态”实际按 `ProjectVersion` 作用域管理，原因是不同版本通常绑定不同 `BaseURL` 或环境，同一个项目的测试环境、预发环境、生产环境不应互相套用登录状态。前端可用“项目登录态”这个用户可理解的名称，但后端契约必须校验 project、version 层级。

登录态至少包含：

- Cookie。
- 每个允许 origin 下的 localStorage。
- 每个允许 origin 下的 sessionStorage。
- 捕获 URL、捕获页面、捕获时间、捕获用户可见名称。
- origin 白名单和状态摘要。

登录态不得包含：

- LLM API Key。
- 浏览器历史、下载记录、密码管理器数据。
- 与当前项目版本 BaseURL 无关的第三方站点存储，除非用户在捕获时明确加入 origin 白名单。

### 会话模式

P4.5 只定义两个测试平台主流程会话模式：

- `clean`：干净会话，不加载项目登录态，也不加载通用 `browser` Cookie Store。用于录制登录流程、执行登录流程用例、排查认证流程。
- `project_saved`：套用当前项目版本默认登录态。用于录制和执行登录后的业务流程。

通用浏览器管理页可以继续保留原有 Cookie 管理能力，但项目页面录制和 TestCase 执行不能无条件继承全局 `browser` Cookie Store。

### 录制类型

页面行内提供三类入口：

- 录制登录流程：使用 `clean` 会话，录制从登录页开始的真实登录路径。录制结束后可选择把当前浏览器状态捕获为项目登录态。
- 录制业务流程：默认使用 `project_saved` 会话；如果没有可用项目登录态，需要提示用户先更新登录态，或显式选择干净会话继续。
- 更新登录态：不保存 PageScript，只打开目标站点让用户手动登录，完成后捕获当前浏览器登录态。

## 四、数据模型设计

### ProjectAuthState

新增模型建议命名为 `ProjectAuthState`。

字段建议：

```text
ID
ProjectID
VersionID
Name
Status              active | expired | disabled
SchemaVersion       当前为 1
StateJSON           敏感 JSON，不通过普通详情接口返回
StateDigest         对 StateJSON 的摘要，用于判断是否变化
OriginAllowlistJSON 允许恢复的 origin 列表
CookieCount
OriginCount
CapturedURL
CapturedPageID
CapturedAt
LastValidatedAt
InvalidReason
CreatedAt
UpdatedAt
```

约束：

- 每个 `ProjectVersion` 同一时间最多一份默认 `active` 登录态。
- 登录态必须校验 ProjectID 和 VersionID 层级，不能只按 AuthState ID 读取。
- 删除 ProjectVersion 时级联删除对应登录态。
- P5 前不把用户归属作为硬契约，但模型应为后续 `CreatedByUserID`、`UpdatedByUserID` 预留迁移空间。
- `StateJSON` 是敏感字段，列表、详情、执行报告和错误响应不得返回原始内容。

### StateJSON 结构

建议结构：

```json
{
  "schema_version": 1,
  "kind": "browser_storage_state",
  "captured_url": "https://example.com/dashboard",
  "captured_at": "2026-06-03T12:00:00Z",
  "origins": [
    {
      "origin": "https://example.com",
      "local_storage": [
        { "name": "access_token", "value": "..." }
      ],
      "session_storage": [
        { "name": "csrf_token", "value": "..." }
      ]
    }
  ],
  "cookies": [
    {
      "name": "session",
      "value": "...",
      "domain": "example.com",
      "path": "/",
      "expires": 1790000000,
      "http_only": true,
      "secure": true,
      "same_site": "Lax"
    }
  ],
  "extensions": {}
}
```

规则：

- `cookies` 保留执行所需的完整 Cookie 字段。
- `local_storage` 和 `session_storage` 按 origin 分组。
- `extensions` 只允许 object，用于后续 IndexedDB、Storage Buckets 或引擎特定数据扩展；P4.5 红测不要求恢复 IndexedDB。
- 捕获时只保存与当前版本 BaseURL 同源或同站的 origin；跨域登录供应商如确需保存，需要用户在捕获确认中勾选。

### PageScript 录制元数据

`PageScript` 需要新增或等价承载录制元数据，建议字段：

```text
RecordingMetaJSON
```

结构建议：

```json
{
  "schema_version": 1,
  "recording_kind": "login_flow",
  "auth_context": "clean",
  "auth_state_id": null,
  "target_url": "https://example.com/login",
  "started_at": "2026-06-03T12:00:00Z",
  "ended_at": "2026-06-03T12:03:00Z"
}
```

`recording_kind` 允许：

- `login_flow`
- `business_flow`

规则：

- 录制登录流程必须保存 `auth_context = "clean"`。
- 录制业务流程使用项目登录态时保存 `auth_context = "project_saved"` 和当时的 `auth_state_id`。
- 重新录制替换 PageScript 时必须一起替换录制元数据。

### TestCase Blueprint 会话字段

Blueprint 顶层新增可选字段：

```json
{
  "schema_version": 1,
  "title": "查询订单",
  "description": "验证登录后可以查询订单",
  "auth_context": "project_saved",
  "steps": []
}
```

规则：

- `auth_context` 只允许 `clean`、`project_saved`。
- 从 PageScript 生成 TestCase 时，必须优先继承 `recording_meta.auth_context`，不能只按 `recording_kind` 推断。
- `login_flow` 的合法录制元数据只能是 `auth_context = "clean"`，因此生成结果继承为 `clean`。
- `business_flow` 可以显式选择 `project_saved` 或 `clean`；生成结果必须继承用户录制时选择的 `auth_context`。
- 旧 PageScript 缺少 `recording_meta` 时，生成结果按兼容语义使用 `auth_context = "clean"`，避免旧录制在没有项目登录态的项目里突然不可执行。
- PageScript 存在 `recording_meta` 但 `auth_context` 非法时，生成接口应拒绝生成，不调用 Playbot 或不保存 TestCase。
- 旧 TestCase 或旧前端手工创建的 Blueprint 可能没有 `auth_context`；后端必须按兼容语义把缺失值视为 `clean`，不能因为 P4.5 新增登录态而让 P1-P3 已存在用例突然要求项目登录态。
- P4.5 新前端在创建或生成用例时应显式写入 `auth_context`；如果用户创建的是登录后业务用例，前端可以显式写入 `project_saved`。
- P2/P4 更新 Blueprint 时如果传入 `auth_context`，后端要校验枚举；如果旧 Blueprint 缺失该字段，可以继续按 legacy 缺省保存，但后续执行仍按 `clean` 处理。

## 五、API 设计

### 登录态摘要

```text
GET /api/v1/projects/:id/versions/:vid/auth-state
```

响应：

```json
{
  "auth_state": {
    "id": 12,
    "project_id": 1,
    "version_id": 2,
    "name": "默认登录态",
    "status": "active",
    "cookie_count": 6,
    "origin_count": 1,
    "captured_url": "https://example.com/dashboard",
    "captured_page_id": 3,
    "captured_at": "2026-06-03T12:00:00Z",
    "last_validated_at": null,
    "invalid_reason": ""
  }
}
```

规则：

- 不返回 Cookie value、localStorage value、sessionStorage value。
- 无登录态返回 `auth_state: null`，不是错误。
- 层级不匹配返回 `404`。

### 捕获登录态

```text
POST /api/v1/projects/:id/versions/:vid/auth-state/capture
```

请求：

```json
{
  "name": "默认登录态",
  "captured_page_id": 3,
  "captured_url": "https://example.com/dashboard",
  "origin_allowlist": ["https://example.com"],
  "replace": true
}
```

规则：

- 浏览器必须处于运行状态，且能读取当前页面。
- 后端从当前页面和允许 origin 捕获 Cookie、localStorage、sessionStorage。
- `replace = true` 时替换当前版本默认登录态；替换必须事务化，失败不能删除旧登录态。
- 捕获结果只返回摘要。
- 捕获到 0 Cookie 且 0 Web Storage 项时返回 `400`，提示没有可保存的登录态。
- 日志只记录数量、origin 和摘要，不记录 value。

### 删除登录态

```text
DELETE /api/v1/projects/:id/versions/:vid/auth-state
```

规则：

- 删除当前版本默认登录态。
- 删除后业务流程执行如需要 `project_saved`，必须执行前失败。

### 录制上下文

```text
GET /api/v1/projects/:id/versions/:vid/pages/:pid/recording-context
```

响应：

```json
{
  "page": {
    "id": 3,
    "name": "订单列表",
    "path": "/orders"
  },
  "version": {
    "id": 2,
    "base_url": "https://example.com"
  },
  "target_url": "https://example.com/orders",
  "has_main_script": true,
  "auth_state": {
    "id": 12,
    "status": "active",
    "cookie_count": 6,
    "origin_count": 1,
    "captured_at": "2026-06-03T12:00:00Z"
  }
}
```

规则：

- `target_url` 使用 P1/P3 已确认的 `ProjectVersion.BaseURL + TestPage.Path` 逻辑。
- 不返回登录态明文。

### 启动页面录制会话

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/recording-session
```

请求：

```json
{
  "recording_kind": "business_flow",
  "auth_context": "project_saved"
}
```

规则：

- `recording_kind = "login_flow"` 时 `auth_context` 必须为 `clean`。
- `recording_kind = "business_flow"` 时允许 `project_saved` 或显式 `clean`。
- `auth_context = "project_saved"` 但当前版本没有 active 登录态时返回 `400`，不启动录制。
- 项目页面录制会话必须使用隔离浏览器上下文，不能自动加载全局 `browser` Cookie Store。
- 如果使用 `project_saved`，必须先恢复登录态，再打开 `target_url`。

### 保存页面录制

现有：

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/recordings
```

请求需要扩展：

```json
{
  "name": "订单列表主流程",
  "action_trace": "{}",
  "dom_snapshot": "{}",
  "recording_meta": {
    "schema_version": 1,
    "recording_kind": "business_flow",
    "auth_context": "project_saved",
    "auth_state_id": 12,
    "target_url": "https://example.com/orders"
  }
}
```

规则：

- 缺少 `recording_meta` 时，为兼容旧前端可按 `business_flow + clean` 保存，但新前端必须传。
- `auth_state_id` 只用于记录当时录制参考，不在执行时强制使用旧状态；执行默认使用当前版本 active 登录态。

### 执行 TestCase

现有：

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/run
```

请求扩展：

```json
{
  "auth_context": "project_saved",
  "browser_instance_id": "",
  "headless": true,
  "stop_on_failure": true,
  "capture_screenshot": true
}
```

规则：

- 未传 `auth_context` 时读取 Blueprint 顶层 `auth_context`。
- Blueprint 也未指定时按旧用例兼容为 `clean`，并可在执行报告里记录 `auth_context_source = "legacy_default"`。
- `auth_context = "clean"` 时必须使用干净上下文，不能恢复项目登录态或全局 Cookie。
- `auth_context = "project_saved"` 时执行前必须找到当前版本 active 登录态，并在首次导航前恢复。
- 显式 `auth_context = "project_saved"` 但缺少登录态属于执行前校验失败，返回 `400`，不得创建 TestExecution。
- 执行报告记录 `auth_context`、`auth_state_id`、`auth_state_captured_at` 等摘要，不记录敏感 value。

## 六、前端体验设计

### 页面管理改为列表

`TestPageManager` 主体从卡片网格改为列表或表格。

建议列：

```text
页面名称 | 路径 | 主流程 | 用例数 | 最近更新 | 操作
```

操作区：

- 录制登录流程。
- 录制业务流程。
- 智能生成。
- 新建用例。
- 查看用例。
- 删除。

顶部增加项目登录态摘要：

```text
项目登录态：已保存，6 个 Cookie，1 个 Origin，更新时间 2026-06-03 12:00
操作：更新登录态、删除登录态、验证登录态
```

无登录态时：

```text
项目登录态：未保存。录制业务流程或执行登录后用例前，建议先更新登录态。
```

### 短录制页

保留通用 `BrowserManager`，但增加项目页面录制模式。进入方式可以继续使用 `/browser?...`，也可以新增独立页面；前端呈现必须收敛。

项目页面录制模式只展示：

- 当前项目、版本、页面、目标 URL。
- 当前会话模式：干净会话或项目登录态。
- 启动浏览器。
- 打开目标页。
- 开始录制。
- 停止并保存。
- 保存当前登录态。
- 返回页面列表。

隐藏或折叠：

- 通用脚本库。
- 通用浏览器配置。
- 通用实例管理。
- 独立 Cookie 管理入口。
- 与页面录制无关的历史访问区。

### 登录态捕获确认

录制登录流程停止后，如果当前页面存在可捕获状态，弹出选择：

- 保存主流程并更新项目登录态。
- 只保存主流程。
- 只更新项目登录态。

捕获确认弹窗只展示摘要：

- 将保存哪些 origin。
- Cookie 数量。
- localStorage/sessionStorage key 数量。
- 不展示具体 value。

## 七、Playbot 输入输出规则

P4.5 不把登录态传给 Playbot。

生成用例时可以传给 Playbot 的只有非敏感会话口径：

```json
{
  "recording_kind": "business_flow",
  "auth_context": "project_saved",
  "has_project_auth_state": true
}
```

Playbot 生成出的 Blueprint 必须包含或保留 `auth_context`。

规则：

- 生成出的 Blueprint 应继承 PageScript `recording_meta.auth_context`。
- `business_flow + clean` 录制生成的 Blueprint 必须保持 `auth_context = "clean"`，不能因为 `recording_kind = "business_flow"` 被改成 `project_saved`。
- 旧 PageScript 缺少 `recording_meta` 时，后端按兼容语义补 `auth_context = "clean"`。
- Playbot 不能生成、推断或回显任何 token、Cookie value、localStorage value。
- 后端解析 Playbot 输出时校验 `auth_context` 枚举；非法值拒绝保存。

## 八、执行恢复规则

执行恢复顺序：

1. 校验 project、version、page、testcase 层级。
2. 解析 Blueprint 并确定有效 `auth_context`；Blueprint 缺失该字段时按 legacy 缺省处理为 `clean`。
3. 如果是 `project_saved`，读取当前版本 active 登录态；缺失则 `400`，不创建执行记录。
4. 创建隔离浏览器上下文。
5. 如果是 `project_saved`，恢复 Cookie 和 Web Storage。
6. 按 P3 默认导航和首步 `navigate` 优先关系执行。
7. 保存 TestExecution，报告中记录登录态摘要。

恢复 Web Storage 的要求：

- localStorage/sessionStorage 必须在对应 origin 下恢复。
- 恢复前不得访问非白名单 origin。
- 恢复失败应作为执行错误或录制启动错误暴露，不得静默降级成只恢复 Cookie。

## 九、安全与隐私边界

登录态是敏感资产，P4.5 必须做到：

- API 列表、详情、执行报告、前端展示都只返回摘要，不返回 value。
- 日志不得记录 Cookie value、localStorage value、sessionStorage value。
- Playbot 输入不得包含登录态明文。
- 项目页面录制和执行不得自动套用全局 `browser` Cookie Store。
- 捕获登录态必须由用户显式点击，不允许停止录制后静默保存。
- 删除登录态必须有确认。

存储加密：

- 如果项目已有密钥管理或配置化加密能力，`StateJSON` 应加密后存储。
- 如果 P4.5 实现时尚无加密基础，必须至少做到服务端私有存储、API 不暴露明文、日志脱敏，并在 `docs/CONTRACT_RECORDS.md` 中记录发布前需要补加密的遗留风险。

## 十、红测要求

本节是给用例编写者的直接开工清单。红测只证明 P4.5 业务契约，不要求真实访问外部登录站点，不要求真实 LLM 调用，不要求真实浏览器跑完整端到端登录。涉及浏览器、Playbot、登录态存储的测试应优先使用 fake service 或测试替身记录调用顺序和输入输出。

### 总体边界

必须覆盖：

- 登录态捕获、摘要读取、删除和层级校验。
- `clean` 与 `project_saved` 两种会话模式的互斥边界。
- 项目录制不再隐式复用全局 `browser` Cookie Store。
- PageScript 保存录制元数据。
- Playbot 生成链路只传非敏感会话口径，并把 `auth_context` 落入 Blueprint。
- TestCase 执行前按 `auth_context` 恢复或拒绝登录态。
- 旧 Blueprint 缺失 `auth_context` 时仍按 `clean` 执行，不要求项目登录态。
- 前端页面列表和行内录制入口。

不在 P4.5 红测范围：

- IndexedDB、CacheStorage、Service Worker Cache 的完整捕获和恢复。
- P5 多用户、成员角色和租户越权。
- 真实第三方登录网站的稳定性。
- 企业级密钥轮换和审计报表。
- 通用 BrowserManager 原有 Cookie 管理页的完整重测，除非改动直接影响项目页面录制。

敏感信息边界：

- 红测可以使用 `secret-cookie-value`、`secret-local-token`、`secret-session-token` 这类假值。
- 这些假值不得出现在 API 响应、执行报告、Playbot 输入或日志断言对象中。
- 后端存储内部是否包含假值可以通过测试专用读取方式验证；普通公开 API 不得返回。

### 后端红测一：登录态捕获和摘要

建议测试名：

```text
TestCaptureProjectAuthStateScopesToProjectVersionAndRedactsValues
```

前置：

- 创建两个 ProjectVersion，分别属于不同项目或同项目不同版本。
- fake browser 返回一份 storage state，包含 Cookie、localStorage、sessionStorage。
- storage state 中放入可识别假 secret value。

动作：

- 调用当前版本的登录态捕获接口。
- 调用当前版本登录态摘要接口。
- 尝试用另一个版本读取该登录态。

期望：

- 捕获成功并返回摘要，摘要包含 cookie_count、origin_count、captured_url、captured_at。
- 摘要响应不包含任何 Cookie value、localStorage value、sessionStorage value。
- 另一个版本读取返回 `404` 或空摘要，不能读到当前版本登录态。

边界：

- 不要求测试真实浏览器的 Cookie 结构兼容全部 Chrome 字段。
- 不要求测试加密算法，只测公开 API 不泄露明文。

### 后端红测二：空登录态和替换失败保护

建议测试名：

```text
TestCaptureProjectAuthStateRejectsEmptyStateAndKeepsPreviousOnFailure
```

前置：

- 当前版本已有一份 active 登录态。
- fake browser 可切换为返回空 Cookie、空 localStorage、空 sessionStorage。
- fake storage 或 fake service 可制造替换失败。

动作：

- 捕获空登录态。
- 捕获替换过程中制造保存失败。
- 再读取登录态摘要或测试专用存储记录。

期望：

- 空登录态返回 `400`。
- 替换失败返回错误。
- 旧 active 登录态仍存在，摘要不变。

边界：

- 不要求捕获接口判断登录是否真的有效，只要求不能保存空状态。

### 后端红测三：录制登录流程必须干净

建议测试名：

```text
TestStartLoginFlowRecordingAlwaysUsesCleanSession
```

前置：

- 当前版本已有 active 项目登录态。
- 全局 `browser` Cookie Store 也存在一份假 Cookie。
- fake browser manager 记录是否调用了 restore auth state、load global cookie、open target URL、start recording。

动作：

- 以 `recording_kind = "login_flow"`、`auth_context = "clean"` 启动录制会话。

期望：

- 不读取、不恢复项目登录态。
- 不加载全局 `browser` Cookie Store。
- 使用隔离干净上下文打开目标 URL。
- 启动录制成功。

边界：

- 不测试真实登录页是否被自动跳转，只验证系统没有主动套用登录态。

### 后端红测四：录制业务流程需要显式项目登录态

建议测试名：

```text
TestStartBusinessFlowRecordingRequiresAndRestoresProjectAuthState
```

前置：

- 准备一个页面和版本 BaseURL。
- 第一组没有 active 登录态。
- 第二组有 active 登录态。
- fake browser manager 记录调用顺序。

动作：

- 没有登录态时，以 `business_flow + project_saved` 启动录制。
- 有登录态时，以 `business_flow + project_saved` 启动录制。

期望：

- 没有登录态时返回 `400`，不启动浏览器录制，不打开目标 URL。
- 有登录态时先恢复项目登录态，再打开 `BaseURL + Path`，再开始录制。
- 不加载全局 `browser` Cookie Store。

边界：

- `business_flow + clean` 可以作为显式调试入口存在，但必须由请求明确传入；测试者不需要把它当默认路径。

### 后端红测五：保存 PageScript 录制元数据

建议测试名：

```text
TestSavePageRecordingPersistsRecordingMetaAndValidatesAuthContext
```

前置：

- 创建 Project、Version、Page。
- 准备合法 ActionTrace、DOMSnapshot。

动作：

- 保存 `login_flow + clean` 录制。
- 保存 `business_flow + project_saved` 录制。
- 保存非法 `auth_context`。

期望：

- PageScript 持久化 `RecordingMetaJSON` 或等价字段。
- 登录流程元数据为 `recording_kind = "login_flow"`、`auth_context = "clean"`。
- 业务流程元数据为 `recording_kind = "business_flow"`、`auth_context = "project_saved"`。
- 非法 `auth_context` 返回 `400`，不替换旧 PageScript。

边界：

- 为旧前端兼容而允许缺少 `recording_meta` 的行为可以单独覆盖；新前端路径必须传元数据。

### 后端红测六：生成链路继承会话口径且不泄密

建议测试名：

```text
TestGenerateTestCasesCarriesAuthContextWithoutSendingAuthSecretsToPlaybot
```

前置：

- 准备三类带 `recording_meta` 的 PageScript：
  - `login_flow + clean`。
  - `business_flow + project_saved`。
  - `business_flow + clean`。
- 另准备一类旧 PageScript，缺少 `recording_meta`。
- 准备 active 项目登录态，内部包含假 secret value。
- fake Playbot service 记录收到的 input，并返回合法 test_cases。

动作：

- 对 `login_flow + clean` 页面生成用例。
- 对 `business_flow + project_saved` 页面生成用例。
- 对 `business_flow + clean` 页面生成用例。
- 对旧 PageScript 缺少 `recording_meta` 的页面生成用例。

期望：

- fake Playbot input 只包含 `auth_context`、`recording_kind`、`has_project_auth_state` 这类非敏感字段。
- fake Playbot input 不包含 Cookie/localStorage/sessionStorage value。
- 登录流程生成的 Blueprint 顶层为 `auth_context = "clean"`。
- 业务流程生成的 Blueprint 顶层为 `auth_context = "project_saved"`。
- 显式 `business_flow + clean` 生成的 Blueprint 顶层必须是 `auth_context = "clean"`，不能被改成 `project_saved`。
- 旧 PageScript 缺少 `recording_meta` 时，生成的 Blueprint 顶层按兼容语义为 `auth_context = "clean"`。

边界：

- 不要求 Playbot 自己理解登录态；后端可以在解析保存时按 PageScript 录制元数据补齐 `auth_context`。
- 如果 PageScript 有 `recording_meta` 但 `auth_context` 非法，应由生成接口拒绝；不要在该测试里用静默默认值掩盖坏数据。

### 后端红测七：非法 Playbot auth_context 拒绝保存

建议测试名：

```text
TestGenerateTestCasesRejectsInvalidBlueprintAuthContext
```

前置：

- fake Playbot 返回 `auth_context = "auto"` 或其他非法值。

动作：

- 调用生成接口，模式覆盖 `append`、`replace` 或至少覆盖会写库的模式。

期望：

- 返回 `400` 或明确的 Playbot 输出非法错误。
- 不保存任何新 TestCase。
- `replace` 模式下旧用例不被删除。

边界：

- 不要求测试所有 Blueprint 字段校验，P1/P2/P3 已覆盖的 schema 只需保持复用。

### 后端红测八：clean 和旧 Blueprint 执行不得恢复登录态

建议测试名：

```text
TestRunTestCaseCleanOrLegacyAuthContextDoesNotRestoreAuthState
```

前置：

- 当前版本存在 active 项目登录态和全局 `browser` Cookie Store。
- 准备两个 TestCase：
  - Blueprint 顶层 `auth_context = "clean"`。
  - 旧 Blueprint 缺失 `auth_context`。
- fake runner 或 fake browser auth service 记录是否恢复登录态。

动作：

- 分别执行两个 TestCase。

期望：

- 不读取或不恢复项目登录态。
- 不加载全局 `browser` Cookie Store。
- 正常进入 runner。
- 显式 `clean` 用例的 TestExecution 报告记录 `auth_context = "clean"`。
- 旧 Blueprint 缺字段用例的 TestExecution 报告记录有效 `auth_context = "clean"`，并可记录 `auth_context_source = "legacy_default"`。
- 两类报告都不含任何 secret value。

边界：

- 不要求真实浏览器验证登录页可见，只验证系统执行路径没有主动套登录态。
- 旧 Blueprint 缺字段是兼容契约，不要求后端在执行时自动回写 Blueprint。

### 后端红测九：project_saved 执行缺少登录态时前置失败

建议测试名：

```text
TestRunTestCaseProjectSavedRequiresAuthStateBeforeRunner
```

前置：

- TestCase Blueprint 顶层 `auth_context = "project_saved"`。
- 当前版本没有 active 登录态。
- fake runner 记录是否被调用。

动作：

- 执行该 TestCase。

期望：

- 返回 `400`。
- fake runner 未被调用。
- 不创建 TestExecution。

边界：

- 只对显式 `project_saved` 执行前失败；旧 Blueprint 缺失 `auth_context` 不属于该失败条件。
- 不允许把显式 `project_saved` 静默降级为 `clean`。

### 后端红测十：project_saved 执行先恢复再导航

建议测试名：

```text
TestRunTestCaseProjectSavedRestoresAuthStateBeforeNavigation
```

前置：

- 当前版本有 active 登录态。
- 准备两个 TestCase：
  - 第一条 step 不是 `navigate`，使用默认导航。
  - 第一条 step 是 `navigate`，使用显式导航。
- fake browser executor 记录调用顺序。

动作：

- 分别执行两个 TestCase。

期望：

- 两个用例都在第一次导航前恢复登录态。
- 默认导航仍遵守 P3 的 `initial_navigation.mode = "default"`。
- 首步 `navigate` 仍遵守 P3 的 `initial_navigation.mode = "explicit_step"`，不产生双导航。
- 报告只记录登录态摘要。

边界：

- 不重新测试 P3 所有 action 执行细节，只验证登录态恢复插入点不破坏 P3 导航契约。

### 前端红测一：页面列表和行内入口

建议测试名：

```text
TestPageManagerRendersCompactPageListWithRecordingActions
```

前置：

- mock 页面列表返回多条页面。
- mock 登录态摘要返回已保存状态。

动作：

- 渲染页面管理页。

期望：

- 页面以列表或表格形态展示，不再以大卡片作为主扫描形态。
- 每行展示页面名称、路径、主流程状态、用例数和操作区。
- 行内存在“录制登录流程”和“录制业务流程”两个明确入口。
- 顶部展示登录态摘要，只展示数量和时间。

边界：

- 不要求固定文案逐字一致，但入口语义必须可识别。

### 前端红测二：无登录态时业务录制引导

建议测试名：

```text
TestBusinessRecordingGuidesWhenProjectAuthStateMissing
```

前置：

- mock 登录态摘要为空。
- 页面存在业务流程录制入口。

动作：

- 点击“录制业务流程”。

期望：

- 前端不直接用 `project_saved` 启动录制。
- 展示先更新登录态或显式使用干净会话的选择。
- 如果用户选择更新登录态，进入更新登录态流程。

边界：

- 不要求前端阻止用户显式选择 `clean` 调试业务页。

### 前端红测三：录制入口携带会话模式

建议测试名：

```text
TestRecordingActionsStartSessionsWithExpectedAuthContext
```

前置：

- mock `recording-session` API。

动作：

- 点击“录制登录流程”。
- 点击“录制业务流程”并选择使用项目登录态。

期望：

- 登录流程请求包含 `recording_kind = "login_flow"`、`auth_context = "clean"`。
- 业务流程请求包含 `recording_kind = "business_flow"`、`auth_context = "project_saved"`。

边界：

- 不要求测试真实浏览器页面打开，只测前端请求和状态切换。

### 前端红测四：保存录制携带 recording_meta

建议测试名：

```text
TestPageRecordingSaveIncludesRecordingMeta
```

前置：

- 进入项目页面录制模式。
- mock 停止录制返回 actions。
- mock 保存录制 API。

动作：

- 停止录制并保存。

期望：

- 保存请求包含 `recording_meta`。
- `recording_meta.auth_context` 和启动录制时选择一致。
- `recording_meta.recording_kind` 和启动录制时选择一致。

边界：

- 不要求前端生成 DOMSnapshot 的质量，P4.5 只测元数据。

### 验证命令

后端红测建议先跑定向用例，再跑全量：

```powershell
cd backend
go test ./api -run "AuthState|Recording|Generate|RunTestCase"
go test ./...
```

前端红测或类型契约变更建议：

```powershell
cd frontend
pnpm run type-check
pnpm run build
```

## 十一、人工验收

推荐验收路径：

1. 创建项目和版本，填写 Base URL。
2. 创建“登录页”和“订单列表”两个页面。
3. 点击“登录页”的“录制登录流程”，确认浏览器未自动登录。
4. 手工完成登录，停止录制，保存主流程并更新项目登录态。
5. 回到页面列表，确认顶部显示项目登录态摘要。
6. 点击“订单列表”的“录制业务流程”，确认打开时已经处于登录后状态。
7. 停止并保存订单主流程。
8. 智能生成订单用例，确认生成用例 Blueprint 包含 `auth_context = "project_saved"`。
9. 执行订单用例，确认执行前套用登录态并通过。
10. 执行登录页用例，确认使用干净会话，不被项目登录态跳过。

## 十二、不在 P4.5 范围内

P4.5 不实现：

- P5 多用户成员权限和租户隔离。
- 企业级密钥托管、密钥轮换和审计报表。
- CI/CD 队列和批量执行。
- IndexedDB、CacheStorage、Service Worker Cache 的完整恢复红测。
- 多套命名登录态切换。P4.5 只要求每个 ProjectVersion 一份默认 active 登录态。

这些内容可以在 P5/P6 或后续稳定化阶段继续扩展。

## 十三、遗留风险

- 部分现代应用把认证状态放入 IndexedDB 或 Service Worker Cache，P4.5 的 Cookie + localStorage + sessionStorage 可能仍无法覆盖全部站点。需要在捕获摘要中检测并提示存在未覆盖存储类型。
- 如果底层浏览器控制仍只有全局 browser profile，开发实现必须优先补隔离上下文，否则清洁会话契约无法成立。
- 登录态包含敏感凭据，发布前应补齐加密存储和访问审计。
