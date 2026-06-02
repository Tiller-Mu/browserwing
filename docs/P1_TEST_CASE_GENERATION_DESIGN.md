# P1 生成测试用例链路详细设计

本文档定义 P1 阶段的业务契约和验收口径。P1 只打通“页面主流程录制 -> Playbot 生成用例 -> 保存 TestCase”的最小闭环，不提前实现 P2 用例详情编辑、P3 执行、P4 自然语言修改和 P5 完整多用户权限。

## 一、阶段目标

用户在某个业务页面上点击“智能生成测试用例”后，系统读取该页面已保存的主流程录制和页面快照，调用 Playbot 生成结构化测试用例，并按请求模式保存为 `TestCase`。

P1 完成后应满足：

- 有主流程录制的页面可以生成测试用例。
- 生成结果以 Blueprint JSON 作为事实来源保存。
- 前端页面能看到用例数量增加。
- 没有主流程、LLM 配置缺失、Playbot 失败或返回非法 JSON 时都有明确错误。
- `preview` 不写数据库，`append` 追加，`replace` 原子覆盖。
- 失败路径不破坏已有 TestCase。

## 二、现有事实

依据当前代码：

- 测试平台核心数据使用 SQLite/GORM，模型在 `backend/models/testing.go`。
- `Project -> ProjectVersion -> TestPage -> PageScript/TestCase` 关系已经存在。
- `PageScript.ActionTrace` 和 `PageScript.DOMSnapshot` 是字符串 JSON。
- `SavePageRecording` 当前会先删除同页面旧 `PageScript`，再保存新脚本，因此当前主流程录制是 1:1。
- `TestCase.Blueprint` 是字符串 JSON，`ScriptContent` 可为空，`Status` 默认 `active`。
- 项目页面 API 在 `backend/api/project_handlers.go`，路由在 `backend/api/router.go`。
- 现有 `ProjectHandlers` 只使用全局 `storage.DB`，尚未注入 BoltDB、全局 config 或 LLM manager。
- LLM 配置保存在 BoltDB，读取入口已有 `GetLLMConfig`、`GetDefaultLLMConfig` 和 `/api/v1/llm-configs`。
- `backend/services/playbot/service.go` 已有 `GenerateTestPlan`，但当前 Python 命令硬编码为 `python`。
- `playbot-engine/cli.py` 的 stdout 输出 JSON，stderr 输出过程日志。
- 前端业务页面入口是 `frontend/src/pages/TestPageManager.tsx`，当前“智能生成测试用例”按钮尚未接 API。
- 录制页 `frontend/src/pages/BrowserManager.tsx` 当前保存 `action_trace`，`dom_snapshot` 仍是 `"{}"` 占位。

推断：

- P1 允许先使用当前保存的 `DOMSnapshot` 作为 Playbot `snapshot` 输入；语义快照质量增强属于后续阶段或 P1 后续修正，不作为本设计的阻塞条件。

## 三、业务契约

### 1. 主流程录制

生成接口必须读取指定页面当前有效的主流程录制。当前有效主流程定义为该 `page_id` 下最近一条 `PageScript`；由于保存录制时已删除旧脚本，正常情况下只有一条。

没有主流程时，生成接口返回 `400`，错误信息表达“请先录制主流程”，不得调用 Playbot，也不得写入 TestCase。

### 2. 层级归属校验

生成接口必须校验：

- `project_id` 存在。
- `version_id` 属于该 project。
- `page_id` 属于该 version。

任一层不匹配或不存在，返回 `404`。不能只按 `page_id` 查询后直接生成，避免 ID 猜测造成跨项目数据污染。P5 会补完整用户隔离，但 P1 必须先保证项目、版本、页面的结构归属。

### 3. 生成模式

请求字段 `mode` 支持：

- `append`：默认模式，保留旧用例，追加保存本次生成的新 TestCase。
- `replace`：原子删除该页面旧 TestCase 后保存本次生成的新 TestCase。
- `preview`：只返回 Playbot 生成结果，不写数据库。

非法 `mode` 返回 `400`，不得调用 Playbot。

`replace` 必须使用数据库事务：只有 Playbot 成功、返回合法、转换成功并创建新用例成功后，旧用例才可被删除或覆盖。事务失败时旧用例保持不变。

### 4. LLM 配置

请求可传 `llm_config_id`：

- 传入时，必须读取对应启用的 LLM 配置。
- 未传时，使用默认启用的 LLM 配置。
- 配置不存在、未启用、缺少 API Key、缺少模型或缺少 endpoint/base URL 时返回明确错误。

P1 不新增第二套 LLM 配置来源。后端生成接口应复用 BoltDB/LLM 配置模型；为了做到这一点，`ProjectHandlers` 需要改为带依赖注入的 handler，例如注入 `*storage.BoltDB` 和 `*config.Config`，或把生成接口挂到已有 `Handler` 上。禁止在生成接口里直接读临时配置文件或硬编码 API Key。

### 5. Playbot 输入

后端传给 Playbot 的 job JSON 必须包含：

```json
{
  "page_url": "版本 BaseURL + 页面 path",
  "snapshot": {},
  "intent_plan": {},
  "page_description": "页面描述",
  "instruction": "用户额外说明"
}
```

字段规则：

- `page_url` 由 `ProjectVersion.BaseURL` 和 `TestPage.Path` 拼接。若 `Path` 已是完整 URL，则优先使用 `Path`。
- `snapshot` 来自 `PageScript.DOMSnapshot` 解析后的 JSON；空字符串或非法 JSON 应作为错误返回，不静默替换成空对象。当前录制页写入 `"{}"` 时属于合法但弱快照。
- `intent_plan` 来自 `PageScript.ActionTrace` 解析后的 JSON；空字符串或非法 JSON 返回 `400`。
- `page_description` 来自 `TestPage.Description`。
- `instruction` 来自用户请求，允许为空。

### 6. Playbot 输出

Playbot stdout 必须是 JSON。后端只接受以下结构：

```json
{
  "test_cases": [
    {
      "title": "正确登录",
      "description": "验证用户输入正确账号密码后可以登录",
      "steps": []
    }
  ],
  "analysis": {},
  "generated_count": 1,
  "error": null
}
```

转换规则：

- `test_cases` 必须是非空数组。
- 每个用例必须有非空 `title`、非空 `description`、非空 `steps` 数组。
- 每个 TestCase 的 `Blueprint` 保存单个用例的完整 JSON，包含 `title`、`description`、`steps`，后续如有 `page_summary`、`analysis`、`schema_version` 等可一并保存。
- `ScriptContent` 在 P1 允许为空字符串。
- `Status` 保存为 `active`。
- 生成来源暂不新增字段；如要记录来源，只能在 Blueprint 内补 `generated_by`、`generated_at`、`source_page_script_id` 等元数据，不改 schema 也不写第二张表。

如果 stdout 不是合法 JSON、`error` 非空、`test_cases` 为空或字段缺失，生成接口返回错误，不写数据库。

### 7. Python/Playbot 路径

`GenerateTestPlan` 不能硬编码 `python` 或当前工作目录。P1 使用环境变量或配置项：

```text
PLAYBOT_PYTHON=D:\depends\python\venvs\browserwing-playbot\Scripts\python.exe
PLAYBOT_ENGINE_DIR=D:\dpProject\browserwing\playbot-engine
```

规则：

- `PLAYBOT_PYTHON` 缺失时可使用配置文件默认值；如果最终解析不到可执行文件，返回配置错误。
- `PLAYBOT_ENGINE_DIR` 缺失时可使用仓库默认相对路径；如果 `cli.py` 不存在，返回配置错误。
- Playbot stderr 只作为日志和错误摘要返回，不得污染 stdout JSON 解析。

## 四、API 契约

### POST 生成测试用例

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/generate
```

请求：

```json
{
  "mode": "append",
  "llm_config_id": "optional",
  "instruction": "optional"
}
```

响应（append/replace）：

```json
{
  "mode": "append",
  "saved": true,
  "generated_count": 2,
  "test_cases": [
    {
      "id": 1,
      "page_id": 10,
      "title": "正确登录",
      "description": "验证正确账号密码登录",
      "blueprint": "{...}",
      "script_content": "",
      "status": "active",
      "created_at": "2026-06-02T00:00:00Z",
      "updated_at": "2026-06-02T00:00:00Z"
    }
  ]
}
```

响应（preview）：

```json
{
  "mode": "preview",
  "saved": false,
  "generated_count": 2,
  "test_cases": [
    {
      "title": "正确登录",
      "description": "验证正确账号密码登录",
      "blueprint": {...}
    }
  ]
}
```

建议错误状态：

- `400`：请求 JSON 非法、mode 非法、缺少主流程、录制 JSON 非法、Playbot 输出结构非法。
- `404`：project/version/page 层级不存在或不匹配。
- `500`：Playbot 配置缺失、Python 执行失败、数据库事务失败。

错误响应统一沿用当前风格：

```json
{
  "error": "请先录制主流程后再生成测试用例"
}
```

## 五、后端设计

### 新增/调整文件建议

- `backend/api/project_handlers.go`
  - 增加 `GenerateTestCases`。
  - 或将 `ProjectHandlers` 改成带依赖的结构体，能读取 BoltDB 和 config。
- `backend/api/router.go`
  - 注册 `POST /projects/:id/versions/:vid/pages/:pid/test-cases/generate`。
- `backend/services/playbot/service.go`
  - 为 `GenerateOptions` 增加 `PythonPath`、`PageDescription`、`Instruction`。
  - 使用 `opts.PythonPath` 执行 CLI。
  - job JSON 写入 `page_description` 和 `instruction`。
- 可选新增 `backend/services/playbot/types.go`
  - 定义 Playbot 输出结构和转换函数，便于测试。

### 事务规则

建议生成流程分两段：

1. 事务外读取页面上下文、主流程、LLM 配置并调用 Playbot。
2. Playbot 成功后进入事务保存：
   - `preview`：不进入事务写库。
   - `append`：只创建新 TestCase。
   - `replace`：先删除该 page 下旧 TestCase，再创建新 TestCase。

这样可避免长时间 LLM 调用占用数据库事务。

`replace` 的删除和创建必须在同一个事务里完成。

## 六、前端设计

### API 封装

在 `frontend/src/api/project.ts` 增加：

```ts
generateTestCases(projectId, versionId, pageId, data)
```

并补充最小类型：

- `TestCase`
- `GenerateTestCasesRequest`
- `GenerateTestCasesResponse`

### 页面交互

在 `frontend/src/pages/TestPageManager.tsx` 中：

- 主流程已就绪时显示“智能生成测试用例”按钮。
- 点击后弹出生成弹窗。
- 弹窗字段：
  - 模式：追加、覆盖、仅预览。
  - LLM 配置：可选，默认使用后端默认配置。
  - 额外说明：可选。
- 生成中禁用按钮并展示 loading。
- `append/replace` 成功后关闭弹窗、刷新页面列表。
- `preview` 成功后展示标题、描述和步骤数量，不刷新数量。
- 失败时展示后端错误，不改变已有列表。

P1 只要求页面列表看到数量变化；用例详情跳转属于 P2。

## 七、契约红测建议

用例编写者应优先写后端契约红测：

1. 无主流程拒绝生成。
   - 期望：返回 `400`，TestCase 数量不变，Playbot 不被调用。
2. `preview` 不落库。
   - 期望：返回 generated cases，但数据库 TestCase 数量不变。
3. `append` 追加。
   - 期望：旧用例保留，新用例保存，Blueprint 为合法 JSON。
4. `replace` 原子覆盖。
   - 期望：成功时旧用例被替换；保存失败时旧用例仍存在。
5. Playbot 失败不破坏旧用例。
   - 期望：返回错误，旧 TestCase 数量和内容不变。
6. 层级不匹配拒绝。
   - 期望：`project_id/version_id/page_id` 任一错配时返回 `404`。
7. Playbot 输出非法 JSON 拒绝保存。
   - 期望：返回 `400` 或明确错误，TestCase 数量不变。

测试不应真实调用 LLM。应通过可注入的 Playbot 调用接口、临时 fake service 或命令路径替身来控制 stdout/stderr。红测不得针对固定页面名、固定测试词或测试用户写特殊逻辑。

前端契约或集成用例后置补充：

- 按钮点击后能提交生成请求。
- 生成中按钮禁用。
- 成功后刷新页面数量。
- 失败后保留当前列表并展示错误。

## 八、验收方式

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

Playbot 环境验证：

```powershell
cd playbot-engine
$env:UV_PROJECT_ENVIRONMENT = "D:\depends\python\venvs\browserwing-playbot"
uv lock --check
uv sync --all-extras
D:\depends\python\venvs\browserwing-playbot\Scripts\python.exe -c "import cli; print('ok')"
```

人工验收：

1. 创建项目和版本。
2. 创建业务页面。
3. 从页面进入录制，保存主流程。
4. 回到页面管理，点击智能生成。
5. 选择追加模式，生成成功后看到用例数量增加。
6. 再选择仅预览，确认数量不变。
7. 删除或新建一个无主流程页面，确认生成按钮或生成请求提示先录制。

## 九、阶段外内容

以下内容不在 P1 完成范围内：

- TestCase 详情页、编辑、删除和状态管理。
- Blueprint 解释执行和 TestExecution 保存。
- 自然语言修改用例与 LLMRefinement。
- 完整多用户项目归属和成员权限。
- 高质量语义快照抽取改造。
- 脚本内容生成与脚本安全执行沙箱。

这些内容分别进入 P2-P5 或后续稳定化阶段处理。

## 十、遗留风险

- 当前录制页 `dom_snapshot` 是 `"{}"`，生成质量可能依赖 ActionTrace，元素定位稳定性不足。
- Playbot schema 来自 Python Pydantic，Go 侧需要最小校验，否则容易保存无法执行的 Blueprint。
- `ProjectHandlers` 当前未注入 BoltDB/config，若实现时继续使用无依赖结构，会导致 LLM 配置读取困难或诱发第二套事实源。
- LLM API Key 当前会从 BoltDB 配置读取，日志中不得打印明文 key。
- P1 只做项目/版本/页面层级校验，不解决用户/租户隔离；P5 必须补齐。
