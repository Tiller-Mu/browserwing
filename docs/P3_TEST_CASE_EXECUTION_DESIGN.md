# P3 用例执行详细设计

本文档定义 P3 阶段的业务契约和验收口径。P3 承接 P1/P2 已完成的 TestCase 生成、保存和管理能力，只补齐“单个 TestCase 执行 -> 保存 TestExecution -> 查看执行结果”的闭环，不提前实现批量执行、自然语言失败修复、任意脚本执行和完整多用户权限。

## 一、阶段目标

用户可以在 TestCase 详情页执行一个启用状态的测试用例，系统按 Blueprint 解释执行步骤，保存一条 TestExecution，并在详情页展示最近执行结果和步骤报告。

P3 完成后应满足：

- 单个 active TestCase 可以被执行。
- 执行前继续校验 project/version/page/testcase 完整层级归属。
- 执行结果保存为 `models.TestExecution`。
- `passed`、`failed`、`error` 只作为 TestExecution 状态，不写入 TestCase.Status。
- 报告可以定位到失败步骤、错误信息、耗时、最终 URL 和关键截图。
- 执行失败必须保存可诊断信息，不能吞错或只返回前端临时状态。
- P3 首版只解释 Blueprint，不执行 `ScriptContent`。

## 二、现有事实

依据当前代码和 P1/P2 收尾记录：

- `backend/models/testing.go` 已有 `TestExecution` 模型，字段包括 `TestCaseID`、`Status`、`ErrorMessage`、`DurationMs`、`ReportData`、`CreatedAt`。
- P2 已新增 TestCase CRUD API，并确认 TestCase 资产状态只允许 `active`、`draft`、`archived`。
- P2 已确认 TestCase 详情返回 parsed Blueprint，腐坏 Blueprint 返回错误，不静默伪造空对象。
- `backend/api/project_testcase.go` 已有 project/version/page/testcase 层级校验 helper，可作为 P3 执行 API 的归属校验基础。
- `backend/executor` 已提供 `Navigate`、`Click`、`Type`、`Select`、`WaitFor`、`GetText`、`Screenshot` 等浏览器操作能力。
- `backend/api/router.go` 已有 `/api/v1/executor/*` 和旧 `/api/v1/scripts/:id/play`，但它们服务于通用浏览器操作和旧脚本回放，不是 TestCase 执行记录事实源。
- `backend/models/script_execution.go` 的 `ScriptExecution` 存在于 BoltDB 脚本系统，不应和 GORM `TestExecution` 混用。
- `frontend/src/pages/TestCaseDetail.tsx` 当前只提供详情、保存和删除，没有执行按钮或执行记录展示。

推断：

- P3 可以复用 BrowserWing executor 做底层动作，但应新增 TestCase 专属执行服务，统一负责 Blueprint schema 校验、步骤解释、执行记录保存和报告生成。
- 为保持 P1/P2 的层级归属契约，P3 执行记录详情也应走 project/version/page/testcase scoped 路由，不新增裸 `executionId` 读取入口。

## 三、阶段边界

### P3 范围内

- 单个 TestCase 执行。
- Blueprint 执行级 schema 校验。
- Blueprint step 到 BrowserWing executor action 的转换。
- 执行记录创建、列表、详情读取。
- 结果状态、错误分类和报告数据结构。
- TestCase 详情页执行按钮、执行中状态、最近执行结果和步骤报告展示。

### P3 范围外

- 页面下全部用例批量执行。
- 版本下全部用例批量执行。
- 并发执行队列、取消执行、重试策略。
- 直接执行 `ScriptContent`。
- 生成或编译脚本。
- 失败后调用 Playbot 分析和修复用例。
- LLMRefinement 历史。
- 完整用户/租户权限隔离。
- 复杂趋势图、覆盖率统计和 CI/CD 集成。

说明：

`docs/DEVELOPMENT_PLAN.md` 中曾提到“如果 ScriptContent 存在，执行脚本”。结合当前安全边界和 P2 遗留风险，P3 首版改为只解释 Blueprint。`ScriptContent` 可以继续作为可编辑资产保存和展示，但不能成为隐藏 fallback；Blueprint 无法执行时必须返回明确错误。

## 四、业务契约

### 1. 层级归属

执行、执行记录列表和执行记录详情 API 都必须校验：

- `project_id` 存在。
- `version_id` 属于该 project。
- `page_id` 属于该 version。
- `testcase_id` 属于该 page。
- 带 `execution_id` 的接口还必须校验 TestExecution 属于该 TestCase。

任一层不存在或不匹配，返回 `404`。不得只凭 `execution_id` 读取报告。

P3 仍不实现完整用户/租户隔离；P5 会补项目成员权限。但 P3 必须延续 P1/P2 的结构隔离。

### 2. 可执行状态

只有 `TestCase.Status = "active"` 的用例可以执行。

规则：

- `draft`：返回 `400`，提示草稿不能执行；不创建 TestExecution。
- `archived`：返回 `400`，提示归档用例不能执行；不创建 TestExecution。
- `active`：继续做 Blueprint 执行级校验。

TestCase.Status 不因执行结果变化。执行成功或失败只写 TestExecution.Status。

### 3. 执行前校验

以下属于执行前校验失败，返回 `400` 或 `404`，不创建 TestExecution：

- 层级不匹配或资源不存在。
- TestCase 不是 active。
- TestCase.Blueprint 不是合法 JSON object。
- Blueprint 缺少非空 `steps` 数组。
- 存在未知 action。
- step 缺少该 action 必需字段。
- step timeout 超出允许范围。
- 目标页面 URL 无法由版本 BaseURL 和页面 path 拼接得到。

说明：

执行记录代表一次真实运行。执行尚未开始的 schema 或资产错误不应制造执行历史，否则会让用户误以为浏览器已经跑过。

### 4. 执行后状态

只要浏览器执行已经开始，就必须保存 TestExecution。

状态定义：

- `passed`：所有步骤执行成功，所有断言通过。
- `failed`：浏览器动作可运行，但业务断言失败，例如 `expect_text` 未匹配、`expect_visible` 未找到目标。
- `error`：执行环境、浏览器、导航、定位、未知运行异常或内部服务错误导致用例无法继续。

分类规则：

- 操作型步骤失败一般归为 `error`：`navigate`、`click`、`fill`、`select`、`wait`。
- 断言型步骤失败归为 `failed`：`expect_visible`、`expect_text`。
- 报告保存失败属于后端错误，但不得伪造 passed；如果浏览器步骤已跑完而保存失败，接口返回 `500`，并在日志中保留 trace id。

### 5. 执行顺序和停止策略

P3 默认顺序执行，不并发。

规则：

- 默认执行 URL 由 `ProjectVersion.BaseURL` + `TestPage.Path` 得到。
- 如果 Blueprint 第一条 step 不是 `navigate`，runner 先执行一次默认页面 URL 导航，再从 step 0 开始执行；这次默认导航属于执行准备动作，不计入 Blueprint steps，但要写入报告。
- 如果 Blueprint 第一条 step 是 `navigate`，runner 必须跳过默认页面 URL 导航，由该 step 完成初始导航；该 step 仍按 index 0 计入步骤报告。
- 首步 `navigate` 的 URL 优先级高于默认执行 URL。未传 `url` 时才回退到默认执行 URL。
- 默认 `stop_on_failure = true`，遇到第一条失败或异常步骤后停止后续步骤。
- 请求可以传 `stop_on_failure = false`，但 P3 前端默认使用 true；红测以默认 true 为主。
- 每一步都记录开始时间、结束时间、耗时、状态和错误摘要。

### 6. 报告数据

`TestExecution.ReportData` 存储 JSON 字符串。它是执行报告事实源，不能只存前端临时展示字段。

最低 schema：

```json
{
  "schema_version": 1,
  "source": "blueprint",
  "execution_url": "https://example.com/login",
  "initial_navigation": {
    "mode": "default",
    "url": "https://example.com/login",
    "step_index": null
  },
  "browser_instance_id": "",
  "started_at": "2026-06-02T00:00:00Z",
  "ended_at": "2026-06-02T00:00:03Z",
  "duration_ms": 3000,
  "summary": {
    "total_steps": 3,
    "passed_steps": 2,
    "failed_steps": 1,
    "failed_step_index": 2
  },
  "steps": [
    {
      "index": 0,
      "action": "fill",
      "description": "输入用户名",
      "status": "passed",
      "started_at": "2026-06-02T00:00:01Z",
      "ended_at": "2026-06-02T00:00:01Z",
      "duration_ms": 120,
      "target_summary": "placeholder: 用户名"
    },
    {
      "index": 1,
      "action": "expect_text",
      "description": "检查错误提示",
      "status": "failed",
      "started_at": "2026-06-02T00:00:02Z",
      "ended_at": "2026-06-02T00:00:03Z",
      "duration_ms": 900,
      "target_summary": "text: 请输入密码",
      "error": "expected text not found"
    }
  ],
  "artifacts": {
    "screenshots": [
      "screenshots/20260602/execution-12-step-1.png"
    ]
  },
  "final_url": "https://example.com/login"
}
```

报告约束：

- 不复制完整 Blueprint。
- 不复制 `ScriptContent`。
- 不保存 LLM API Key、认证 token 或浏览器 Cookie。
- 输入值默认不写入报告；如确需展示，只记录长度或脱敏摘要。
- 截图路径可保存，截图文件由现有 executor screenshot 能力或后续 artifact helper 生成。
- `initial_navigation.mode` 只能是 `default` 或 `explicit_step`；`default` 的 `step_index` 为 null，`explicit_step` 的 `step_index` 为 0。

### 7. 最近执行结果

P3 应允许页面和详情读取最近执行结果，但不要求复杂统计。

规则：

- TestCase 执行列表按 `created_at desc, id desc` 排序。
- 默认返回最近 20 条；可支持 `limit`，最大 50。
- 执行列表返回 summary，不返回完整 `report_data`。
- 执行详情返回 parsed `report_data` object；如果 report_data 腐坏，返回 `500`，不静默伪造空报告。

## 五、Blueprint 执行级 schema

P3 不要求完整覆盖未来所有 Playbot schema，但必须定义首版可执行最小集。

### 顶层字段

```json
{
  "schema_version": 1,
  "title": "正确登录",
  "description": "验证正确账号密码可以登录",
  "steps": []
}
```

规则：

- `steps` 必须是非空数组。
- `schema_version` 可为空；为空时按 `1` 处理。
- P3 不因额外字段失败，但不得依赖额外字段作为隐藏事实源。

### Step 通用字段

```json
{
  "action": "fill",
  "description": "输入用户名",
  "target": {
    "recorded_selector": "input[name='username']",
    "selector": "input[name='username']",
    "xpath": "//*[@id='username']",
    "role": "textbox",
    "text": "用户名",
    "label": "用户名",
    "placeholder": "请输入用户名",
    "ref_id": "@e1"
  },
  "value": "alice",
  "timeout_ms": 10000
}
```

兼容字段：

- P1/P2 示例中出现过 `target_hint`，P3 后端应将 `target_hint` 作为 `target` 的兼容别名。
- `fill` 的 `value` 也可兼容 `text`。
- `wait` 的等待时长可兼容 `duration_ms`。

### 支持动作

P3 首版支持以下 action：

- `navigate`
- `click`
- `fill`
- `select`
- `wait`
- `expect_visible`
- `expect_text`

未知 action 必须在执行前校验阶段返回 `400`，不得跳过。

### 动作字段规则

#### navigate

必需字段：

- `url` 可选。

规则：

- 未传 `url` 时导航到版本 BaseURL + 页面 path。
- 传相对路径时基于版本 BaseURL 拼接。
- 传绝对 URL 时直接使用，但必须保留在报告中。

#### click

必需字段：

- `target` 或 `target_hint` 至少提供一个可解析定位线索。

执行：

- 转换为 executor `Click(identifier)`。
- 默认等待可见、可用。

#### fill

必需字段：

- `target` 或 `target_hint`。
- `value` 或 `text`，允许为空字符串。

执行：

- 转换为 executor `Type(identifier, value)`。
- 默认先清空再输入。

#### select

必需字段：

- `target` 或 `target_hint`。
- `value`。

执行：

- 转换为 executor `Select(identifier, value)`。

#### wait

必需字段二选一：

- `target` 或 `target_hint`：等待目标出现，默认 state 为 `visible`。
- `duration_ms`：固定等待。

限制：

- `duration_ms` 最大 30000。
- `timeout_ms` 最大 60000。

#### expect_visible

必需字段：

- `target` 或 `target_hint`。

执行：

- 等待目标可见。
- 未找到或不可见归为 `failed`。

#### expect_text

必需字段：

- `value` 或 `text`，表示期望文本。

可选字段：

- `target` 或 `target_hint`。有目标时读取目标文本；无目标时读取页面文本。

执行：

- 目标文本或页面文本包含期望文本时通过。
- 未匹配时归为 `failed`。

### 定位优先级

将 target 转换为 executor identifier 时，按以下顺序选择：

1. `ref_id`
2. `recorded_selector`
3. `selector`
4. `css`
5. `xpath`
6. `label`
7. `placeholder`
8. `role + text`
9. `text`

规则：

- `xpath` 可加 `xpath:` 前缀，也可直接传 XPath。
- `selector` 和 `css` 可加 `css:` 前缀，也可直接传 CSS selector。
- 当前 executor 已支持 RefID、CSS、XPath、文本、aria-label、placeholder 等定位方式；P3 不应另写第二套浏览器查找逻辑。
- 当 target 同时提供 `role` 和 `text` 时，必须优先使用二者组合；只有缺少 role 或组合定位不可构造时，才退回纯 `text`。
- 如果 target 同时给出多个线索，报告中只记录被采用的 `target_summary`，不泄露完整 selector 列表。

## 六、API 契约

### POST 执行单个 TestCase

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/run
```

请求：

```json
{
  "browser_instance_id": "",
  "headless": false,
  "stop_on_failure": true,
  "capture_screenshot": true
}
```

字段说明：

- `browser_instance_id` 可选；为空使用当前浏览器实例。
- `headless` 可选；首版可先透传给执行服务或忽略，但不得改变全局配置后不恢复。
- `stop_on_failure` 可选，默认 true。
- `capture_screenshot` 可选，默认 true；失败步骤至少尝试截图。

成功响应：

```json
{
  "execution": {
    "id": 12,
    "test_case_id": 11,
    "status": "passed",
    "error_message": "",
    "duration_ms": 3000,
    "report_data": {
      "schema_version": 1,
      "source": "blueprint",
      "summary": {
        "total_steps": 3,
        "passed_steps": 3,
        "failed_steps": 0,
        "failed_step_index": null
      },
      "steps": []
    },
    "created_at": "2026-06-02T00:00:03Z"
  }
}
```

失败但已执行响应：

```json
{
  "execution": {
    "id": 13,
    "test_case_id": 11,
    "status": "failed",
    "error_message": "step 2 expect_text failed: expected text not found",
    "duration_ms": 2400,
    "report_data": {
      "schema_version": 1,
      "summary": {
        "total_steps": 3,
        "passed_steps": 2,
        "failed_steps": 1,
        "failed_step_index": 2
      },
      "steps": []
    },
    "created_at": "2026-06-02T00:00:03Z"
  }
}
```

说明：

- `failed` 和 `error` 执行结果仍返回 `200`，因为执行已经完成并保存报告。
- 执行前校验失败返回 `400` 或 `404`，不返回 execution。
- 保存失败返回 `500`。

### GET 执行记录列表

```text
GET /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/executions?limit=20
```

响应：

```json
{
  "executions": [
    {
      "id": 13,
      "test_case_id": 11,
      "status": "failed",
      "error_message": "step 2 expect_text failed: expected text not found",
      "duration_ms": 2400,
      "created_at": "2026-06-02T00:00:03Z"
    }
  ],
  "count": 1
}
```

列表不返回 `report_data`。

### GET 执行记录详情

```text
GET /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/executions/:eid
```

响应：

```json
{
  "execution": {
    "id": 13,
    "test_case_id": 11,
    "status": "failed",
    "error_message": "step 2 expect_text failed: expected text not found",
    "duration_ms": 2400,
    "report_data": {
      "schema_version": 1,
      "summary": {
        "total_steps": 3,
        "passed_steps": 2,
        "failed_steps": 1,
        "failed_step_index": 2
      },
      "steps": []
    },
    "created_at": "2026-06-02T00:00:03Z"
  }
}
```

错误响应：

- `400`：请求 JSON 非法、非 active 用例、Blueprint schema 不可执行、非法 timeout。
- `404`：project/version/page/testcase/execution 层级不存在或不匹配。
- `500`：浏览器服务启动失败前的内部错误、执行记录保存失败、历史 report_data 腐坏。

错误响应沿用当前风格：

```json
{
  "error": "Active TestCase blueprint must contain executable steps"
}
```

## 七、后端设计

### 新增/调整文件建议

- `backend/api/project_testcase_execution.go`
  - 新增执行、执行列表、执行详情 handlers。
  - 复用 P2 的层级校验和 TestCase 归属 helper。
  - 返回 execution summary/detail DTO。
- `backend/services/testcase_executor/`
  - 新增 TestCase Blueprint 执行服务。
  - 负责 schema 校验、步骤解释、状态分类、报告组装。
  - 依赖现有 `backend/executor`，不依赖旧 ScriptExecution。
- `backend/api/router.go`
  - 注册 TestCase execution 路由。
- `backend/api/project_testcase_execution_test.go`
  - 写 P3 契约红测。

### DTO

建议响应 DTO：

```go
type testExecutionSummaryResponse struct {
    ID           uint      `json:"id"`
    TestCaseID   uint      `json:"test_case_id"`
    Status       string    `json:"status"`
    ErrorMessage string    `json:"error_message"`
    DurationMs   int       `json:"duration_ms"`
    CreatedAt    time.Time `json:"created_at"`
}

type testExecutionDetailResponse struct {
    ID           uint           `json:"id"`
    TestCaseID   uint           `json:"test_case_id"`
    Status       string         `json:"status"`
    ErrorMessage string         `json:"error_message"`
    DurationMs   int            `json:"duration_ms"`
    ReportData   map[string]any `json:"report_data"`
    CreatedAt    time.Time      `json:"created_at"`
}
```

### 执行服务接口

建议将执行服务抽象出来，方便红测用 fake executor 验证状态分类和报告保存，不必真实启动浏览器：

```go
type TestCaseRunner interface {
    Run(ctx context.Context, input RunTestCaseInput) (RunTestCaseResult, error)
}
```

`ProjectHandlers` 可以注入默认 runner。测试中应尽量走真实路由和数据库，只在浏览器动作层使用 fake runner，避免手写第二套 API 事实源。

### 执行记录保存

流程建议：

1. handler 解析 path 参数。
2. 校验 project/version/page/testcase 层级。
3. 读取 TestCase，并校验 `Status == active`。
4. 解析并校验 Blueprint 执行 schema。
5. 调用 runner 执行。
6. 将 runner 结果保存为 `models.TestExecution`。
7. 返回 parsed detail DTO。

保存规则：

- runner 返回 `passed`、`failed` 或 `error` 都要保存 TestExecution。
- `ReportData` 保存前用 `json.Marshal` 生成字符串。
- `DurationMs` 由服务端计算，不信任前端。
- 保存后返回数据库里的 ID 和 CreatedAt。

### 并发和浏览器实例

P3 不实现执行队列。首版可以串行执行当前请求。

约束：

- 同一个浏览器实例同时只能跑一个 TestCase；如已有执行进行中，返回 `409` 或在服务内串行化。建议首版返回 `409`，契约红测可后置。
- 执行过程中不能开启录制模式。
- 如果需要临时改变 headless，必须执行后恢复原配置。
- 页面执行结束后是否关闭页面由 runner 明确处理；不能留下全局状态影响下一次执行。

### 截图和 artifact

P3 最低要求：

- 失败步骤尝试截图。
- 截图失败不能覆盖原始失败原因。
- 报告中保存相对路径或可由后端静态服务访问的路径。

成功步骤截图可选，不作为红测核心契约。

## 八、前端设计

### API 封装

在 `frontend/src/api/project.ts` 增加：

```ts
runTestCase(projectId, versionId, pageId, testCaseId, data)
listTestCaseExecutions(projectId, versionId, pageId, testCaseId, limit?)
getTestCaseExecution(projectId, versionId, pageId, testCaseId, executionId)
```

补充类型：

- `TestExecutionStatus = 'passed' | 'failed' | 'error'`
- `TestExecutionSummary`
- `TestExecutionDetail`
- `RunTestCaseRequest`
- `ExecutionReportData`

### TestCase 详情页

`frontend/src/pages/TestCaseDetail.tsx` 增加真实执行能力：

- 顶部新增“执行”按钮。
- TestCase 非 active 时禁用执行按钮，并显示当前状态。
- 执行中禁用保存、删除和再次执行，展示 loading。
- 执行完成后刷新执行列表和最近执行详情。
- 执行结果状态用 `passed`、`failed`、`error` 展示，不写回 TestCase.Status。
- 报告区展示：
  - 最近执行状态。
  - 耗时。
  - 错误信息。
  - 步骤列表。
  - 失败步骤高亮。
  - 截图链接。

### 页面管理页

页面用例卡片最近执行状态后置到 P3 之后，不作为 P3 必交付：

- P3 前端只要求在 TestCase 详情页展示最近执行结果和执行历史。
- 如果后端列表暂不返回最近执行结果，页面管理页不展示 latest_execution，也不伪造。
- 不应把 TestCase.Status 当作最近执行状态。
- 后续如要展示，应由后端 summary 明确返回 `latest_execution`。

### 前端错误处理

- 执行前校验失败展示后端错误。
- 执行结果为 failed/error 时仍展示报告，不按接口错误处理。
- 读取历史报告失败时显示错误，不清空当前 TestCase 编辑表单。

## 九、契约红测建议

用例编写者应优先写后端契约红测：

1. 执行 API 必须校验完整层级。
   - 期望：正确 project/version/page/testcase 才能执行。
   - 期望：错配 project、version、page 或 testcase 返回 `404`。
   - 期望：错配时不创建 TestExecution。
2. 只有 active TestCase 可以执行。
   - 期望：active 进入 runner。
   - 期望：draft、archived 返回 `400`。
   - 期望：draft、archived 不创建 TestExecution。
3. Blueprint 执行级 schema 校验。
   - 期望：缺少 steps、空 steps、未知 action、缺少必需 target/value 返回 `400`。
   - 期望：校验失败不创建 TestExecution。
   - 期望：`target_hint` 可以作为 `target` 兼容字段。
   - 期望：第一条 step 不是 `navigate` 时，runner 先执行默认页面 URL 导航，并在 ReportData 中记录 `initial_navigation.mode = "default"`。
   - 期望：第一条 step 是 `navigate` 时，runner 跳过默认页面 URL 导航，只执行该显式 navigate，并在 ReportData 中记录 `initial_navigation.mode = "explicit_step"`。
   - 期望：target 同时提供 `role` 和 `text` 时，定位转换优先选择 `role + text`，缺少 role 时才退回纯 `text`。
4. 执行成功保存 passed。
   - 使用 fake runner 返回 passed。
   - 期望：接口返回 `200`。
   - 期望：数据库新增 TestExecution，状态为 `passed`。
   - 期望：TestCase.Status 仍为 `active`。
5. 断言失败保存 failed。
   - 使用 fake runner 模拟 `expect_text` 或 `expect_visible` 失败。
   - 期望：接口返回 `200`。
   - 期望：TestExecution.Status 为 `failed`。
   - 期望：ErrorMessage 包含失败步骤摘要。
   - 期望：ReportData 记录 failed_step_index。
6. 执行异常保存 error。
   - 使用 fake runner 模拟 click/navigation/browser error。
   - 期望：接口返回 `200`。
   - 期望：TestExecution.Status 为 `error`。
   - 期望：ReportData 包含失败步骤和错误摘要。
7. 执行记录列表。
   - 期望：只返回当前 TestCase 的执行记录。
   - 期望：跨 page/version/project/testcase 的执行记录不出现。
   - 期望：按 `created_at desc, id desc` 排序。
   - 期望：列表不返回完整 `report_data`。
8. 执行记录详情。
   - 期望：正确层级返回 parsed ReportData。
   - 期望：execution 不属于当前 testcase 返回 `404`。
   - 期望：腐坏 ReportData 返回 `500`，不得静默返回空报告。
9. 状态分离。
   - 期望：执行 passed/failed/error 后 TestCase.Status 不变。
   - 期望：TestCase.Status 仍只允许 P2 的 active/draft/archived。

测试约束：

- 红测不真实启动浏览器。
- 允许 fake runner/fake executor 模拟底层动作，但 API、GORM schema、TestCase 层级校验和 TestExecution 保存必须走生产代码。
- 不手写第二套 Blueprint 解释事实源；如实现提供 parser/validator helper，测试应调用 API 或 helper 验证行为。
- 不复用 BoltDB `ScriptExecution` 作为 TestExecution 断言对象。
- 涉及数据库状态的测试必须隔离并恢复全局 `storage.DB`。
- 红测写清期望行为、依据来源、当前失败形态和验证命令。

前端契约或集成用例后置补充：

- active 用例详情页显示执行按钮。
- draft/archived 用例禁用执行按钮。
- 点击执行后按钮 loading，完成后展示最近执行报告。
- failed/error 执行结果仍展示报告，不当成接口失败吞掉。
- 执行后 TestCase 状态标签不变。

## 十、验收方式

后端最小验证：

```powershell
cd backend
go test ./...
```

前端最小验证：

```powershell
cd frontend
pnpm run type-check
pnpm run build
```

人工验收：

1. 创建项目、版本、页面。
2. 创建或生成一个 active TestCase，Blueprint 包含可执行 steps。
3. 打开 TestCase 详情页，点击执行。
4. 确认执行中有 loading，完成后展示最近执行结果。
5. 故意让一个断言失败，确认状态为 failed，并能看到失败步骤和错误信息。
6. 故意让一个定位失败，确认状态为 error，并能看到失败步骤和错误信息。
7. 刷新详情页，确认执行记录仍能读取。
8. 将 TestCase 改为 draft 或 archived，确认不能执行。
9. 确认执行结果不会改变 TestCase.Status。

## 十一、阶段外内容

以下内容不在 P3 完成范围内：

- 批量执行页面或版本下全部用例。
- 执行队列、取消执行、并发控制 UI。
- 直接执行 ScriptContent。
- 自动生成 ScriptContent。
- 用自然语言修复失败用例。
- LLMRefinement 记录。
- 高级报告统计和趋势图。
- 多用户项目成员权限。

这些分别进入 P4、P5、P6 或后续稳定化阶段。

## 十二、遗留风险

- 当前 Blueprint schema 是 P3 首版执行最小集，后续 Playbot 生成输出需要逐步收敛到该 schema。
- 现有 executor 定位能力支持多种 identifier，但页面语义快照质量仍会影响执行稳定性。
- 直接脚本执行仍有安全风险，P3 不处理；后续如果启用 ScriptContent，需要单独设计沙箱、权限和审计。
- 截图 artifact 的存储路径和清理策略需要在稳定化阶段补齐。
- P3 仍只保证结构层级归属，完整用户/租户隔离进入 P5。
