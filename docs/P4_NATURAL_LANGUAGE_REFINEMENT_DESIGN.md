# P4 自然语言修改用例详细设计

本文档定义 P4 阶段的业务契约和验收口径。P4 承接 P1/P2/P3 已完成的 TestCase 生成、管理和单用例执行能力，只补齐“自然语言提出修改 -> Playbot 生成修改建议 -> 用户确认应用 -> 记录 LLMRefinement 历史”的闭环，不提前实现批量失败自修复、脚本自动生成执行、完整手工修改审计和完整多用户权限。

## 一、阶段目标

用户在 TestCase 详情页输入自然语言修改要求后，系统读取当前已保存的 TestCase Blueprint 和可用页面上下文，调用 Playbot 生成一份修改建议。建议生成后先作为 `LLMRefinement` 草案保存，用户可以查看修改前后 Blueprint，再选择应用或放弃。

P4 完成后应满足：

- 用户可以对指定 TestCase 发起自然语言修改。
- 生成建议时不覆盖原 TestCase。
- 每条建议保存用户 prompt、修改前 Blueprint、修改后 Blueprint、摘要、风险提示和状态。
- 用户只能显式应用仍然有效的草案建议。
- 应用后 TestCase 更新，Refinement 标记为已应用。
- 放弃后 TestCase 不变，Refinement 标记为已放弃。
- 修改失败、Playbot 返回非法结构、建议过期或应用失败时都不污染原 TestCase。
- Refinement 历史可查看，并继续校验 project/version/page/testcase/refinement 层级归属。

## 二、现有事实

依据当前代码和 P3 收尾记录：

- `backend/models/testing.go` 已有 `LLMRefinement` 模型，但当前只有 `TestCaseID`、`UserPrompt`、`RefinedBlueprint`、`CreatedAt`，不足以支撑 P4 的修改前后比较、应用状态和过期防护。
- `TestCase.Blueprint` 是字符串 JSON，P2 已确认它是 TestCase 的结构化事实来源。
- `TestCase.Status` 只允许 `active`、`draft`、`archived`，执行状态由 `TestExecution.Status` 表达。
- P2 已有 TestCase CRUD API 和 `applyTestCaseUpdate`、Blueprint 基础校验、标题描述归一化逻辑。
- P3 已有 TestExecution 列表和详情 API，执行报告存储在 `TestExecution.ReportData`。
- `backend/services/playbot/service.go` 当前只有 `GenerateTestPlan`，Playbot CLI 当前只有生成模式，没有 refine 模式。
- P1/P3 都已确认不能引入隐藏 fallback 或第二套事实来源；P4 修改仍必须以 Blueprint 为事实来源。
- 前端 TestCase 详情页已有标题、描述、状态、Blueprint JSON、ScriptContent、执行按钮、执行历史和最近报告展示。
- LLM 配置读取已由 `ProjectHandlers` 复用 BoltDB 的默认或指定启用配置，不能另建临时配置源。

推断：

- P4 可以复用 P2 的层级校验、TestCase 读取和 Blueprint 归一化规则，但需要新增 refinement 归属校验和应用事务。
- P4 可以把 PageScript 和 TestExecution 作为 Playbot 修改的辅助上下文，但不能要求每个 TestCase 必须有主流程或执行记录；手工创建的用例也应能被自然语言修改。

## 三、业务契约

### 1. 修改建议和应用必须分离

自然语言修改分两步：

1. `refine`：调用 Playbot 生成修改建议，保存为 `LLMRefinement`，状态为 `proposed`，不修改 TestCase。
2. `apply`：用户确认后应用某条仍有效的建议，更新 TestCase，并把该 `LLMRefinement` 标记为 `applied`。

P4 不支持在 `refine` 请求里传 `apply: true` 一步到位应用。这样可以保证用户能先查看修改前后 Blueprint，也避免 LLM 输出直接覆盖核心测试资产。

### 2. 层级归属

所有 Refinement API 都必须校验：

- `project_id` 存在。
- `version_id` 属于该 project。
- `page_id` 属于该 version。
- `tcid` 属于该 page。
- 带 `rid` 的接口还必须校验 `LLMRefinement` 属于该 TestCase。
- 带 `execution_id` 作为上下文时，必须校验 `TestExecution` 属于该 TestCase。

任一层不匹配或不存在，返回 `404`。不得只凭 `tcid` 或 `rid` 读取、应用或放弃修改建议。

P4 仍不实现完整用户/租户隔离；P5 会补用户归属和项目成员权限。但 P4 必须继承 P1-P3 已确认的项目、版本、页面、用例和执行记录结构隔离。

### 3. TestCase 适用状态

P4 允许对 `active`、`draft`、`archived` TestCase 生成和应用 Refinement，原因是 P2 已允许编辑这些资产状态。

应用时必须按当前 TestCase 状态校验最终 Blueprint：

- 当前状态为 `active` 时，修改后的 Blueprint 必须包含非空 `steps`。
- 当前状态为 `draft` 或 `archived` 时，允许 Blueprint 不完整，但仍必须是合法 JSON object。

P4 不通过自然语言直接修改 `TestCase.Status`。用户如需启用、草稿或归档，仍使用 P2 的状态编辑入口。

### 4. Blueprint 事实来源

Refinement 只修改 Blueprint 和由 Blueprint 顶层同步出的 TestCase 标题、描述。P4 不执行、不生成、不自动更新 `ScriptContent`。

应用规则：

- `RefinedBlueprint` 必须是合法 JSON object。
- `RefinedBlueprint.title` 必须是非空字符串，应用时同步到 TestCase 外层 `Title`。
- `RefinedBlueprint.description` 字段必须存在且为字符串，允许为空字符串；应用时同步到 TestCase 外层 `Description`，因此清空描述是合法修改。
- 如果 `title` 为空、`description` 缺失或不是字符串，应用失败，TestCase 不变。
- `ScriptContent` 保持不变，不能作为 Blueprint 不可用时的隐藏 fallback。
- `TestCase.Status` 保持不变。
- `TestCase.UpdatedAt` 应在成功应用后刷新。

### 5. 修改前快照和过期防护

创建 Refinement 时必须保存当前 TestCase 的规范化 Blueprint 为 `OriginalBlueprint`。应用时必须确认当前 TestCase 的 Blueprint 仍与该 `OriginalBlueprint` 等价。

等价比较使用规范化 JSON：

1. 解析当前 `TestCase.Blueprint` 和 `LLMRefinement.OriginalBlueprint`。
2. 重新 `json.Marshal` 得到规范化字符串。
3. 比较规范化字符串。

如果 TestCase 在建议生成后被手工编辑、重新生成、其他建议应用或任何路径修改过，应用该建议返回 `409`，并且不得修改 TestCase，也不得把 Refinement 标记为 applied。

这个规则避免旧建议覆盖用户后续保存的新 Blueprint。

### 6. Refinement 状态

`LLMRefinement.Status` 只允许：

- `proposed`：已生成建议，尚未应用或放弃。
- `applied`：建议已应用到 TestCase。
- `discarded`：用户已放弃建议。

规则：

- 新建建议默认 `proposed`。
- 只有 `proposed` 可以应用。
- 只有 `proposed` 可以放弃。
- `applied` 和 `discarded` 都不能再次应用，也不能相互转换。
- 应用成功后写入 `AppliedAt`。
- 放弃不会写 `AppliedAt`。

P4 不要求同时只允许一个 `proposed` 草案；用户可以保留多条建议。但任何草案应用时都必须通过修改前快照校验。

### 7. Playbot 上下文

P4 调用 Playbot refine 时必须至少传入：

```json
{
  "mode": "refine",
  "page_url": "版本 BaseURL + 页面 path",
  "page_description": "页面描述",
  "current_blueprint": {},
  "user_prompt": "增加密码为空的校验"
}
```

可选上下文：

- `snapshot`：最近一条 PageScript 的 DOMSnapshot 或语义快照。
- `intent_plan`：最近一条 PageScript 的 ActionTrace。
- `execution_report`：请求传入 `execution_id` 时对应的 TestExecution.ReportData。
- `context_warnings`：缺少主流程、录制 JSON 非法或执行报告不可用等上下文情况。

P4 与 P1 生成链路不同：没有主流程不应阻止 refine。手工创建的 TestCase 也可以自然语言修改。页面上下文缺失时必须显式传递空上下文和 warning，不能在后端静默伪造一份主流程。

如果请求传入 `execution_id`，该执行记录必须可读取且 `ReportData` 是合法 JSON；否则返回错误，不调用 Playbot。

### 8. LLM 配置

请求可传 `llm_config_id`：

- 传入时，读取对应启用的 LLM 配置。
- 未传时，使用默认启用的 LLM 配置。
- 配置不存在、未启用、缺少 API Key、缺少模型或缺少 endpoint/base URL 时返回明确错误。

P4 必须复用 P1 已有 LLM 配置读取链路，不新增第二套配置文件、环境变量 API Key 或硬编码模型。日志和错误摘要不得泄露明文 API Key。

### 9. Playbot 输出

Playbot stdout 必须是 JSON。后端只接受以下结构：

```json
{
  "refined_blueprint": {
    "title": "密码为空时提示必填",
    "description": "验证登录页密码为空的表单校验",
    "steps": [
      {
        "action": "fill",
        "target": { "placeholder": "密码" },
        "value": ""
      },
      {
        "action": "expect_text",
        "text": "请输入密码"
      }
    ]
  },
  "summary": "新增密码为空输入和错误提示断言",
  "risk_notes": "需要确认密码输入框和错误提示文本在目标版本中稳定存在",
  "error": null
}
```

规则：

- `refined_blueprint` 必须是 JSON object。
- `refined_blueprint.title` 必须是非空字符串。
- `refined_blueprint.description` 可为空字符串，但字段应存在；后端应用时同步到 TestCase 描述。
- `summary` 必须是非空字符串。
- `risk_notes` 可为空字符串。
- `error` 非空时视为 Playbot 失败。
- 输出不是合法 JSON、缺少 `refined_blueprint`、字段非法或不满足当前状态 Blueprint 校验时，返回错误，不创建 Refinement，不修改 TestCase。

P4 不要求 Playbot 返回 JSON Patch。前端可以根据 `original_blueprint` 和 `refined_blueprint` 自行展示 diff。

## 四、数据模型

当前 `LLMRefinement` 模型需要扩展。建议字段：

```go
type LLMRefinement struct {
    ID                uint       `gorm:"primaryKey" json:"id"`
    TestCaseID        uint       `gorm:"index;not null" json:"test_case_id"`
    UserPrompt        string     `gorm:"type:text;not null" json:"user_prompt"`
    OriginalBlueprint string     `gorm:"type:text;not null" json:"original_blueprint"`
    RefinedBlueprint  string     `gorm:"type:text;not null" json:"refined_blueprint"`
    Summary           string     `gorm:"type:text" json:"summary"`
    RiskNotes         string     `gorm:"type:text" json:"risk_notes"`
    Status            string     `gorm:"size:50;default:'proposed';index" json:"status"`
    AppliedAt         *time.Time `json:"applied_at"`
    CreatedAt         time.Time  `json:"created_at"`
    UpdatedAt         time.Time  `json:"updated_at"`
}
```

字段说明：

- `OriginalBlueprint`：建议生成时的 TestCase Blueprint 快照，用于 diff 和过期防护。
- `RefinedBlueprint`：Playbot 返回并通过后端校验的修改后 Blueprint。
- `Status`：`proposed`、`applied`、`discarded`。
- `Summary` 和 `RiskNotes`：供前端列表和详情展示，不作为业务校验事实源。
- `AppliedAt`：只有成功应用后填写。

P4 不新增 `CreatedByUserID` 作为硬契约；创建人和租户归属随 P5 多用户权限一起补齐。当前如果实现能从上下文取到 `user_id`，可以先记录到扩展字段，但红测不应依赖它。

## 五、API 契约

### POST 生成修改建议

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refine
```

请求：

```json
{
  "prompt": "增加密码为空的校验",
  "llm_config_id": "optional",
  "execution_id": 12
}
```

字段规则：

- `prompt` 必填，trim 后不能为空。
- `llm_config_id` 可选。
- `execution_id` 可选；传入时作为失败报告或历史执行上下文，必须属于当前 TestCase。

响应：

```json
{
  "refinement": {
    "id": 21,
    "test_case_id": 10,
    "user_prompt": "增加密码为空的校验",
    "summary": "新增密码为空输入和错误提示断言",
    "risk_notes": "需要确认错误提示文案稳定",
    "status": "proposed",
    "original_blueprint": {
      "title": "正确登录",
      "description": "验证正确账号密码可以登录",
      "steps": [
        {
          "action": "expect_text",
          "text": "登录"
        }
      ]
    },
    "refined_blueprint": {
      "title": "密码为空时提示必填",
      "description": "验证登录页密码为空的表单校验",
      "steps": [
        {
          "action": "fill",
          "target": { "placeholder": "密码" },
          "value": ""
        },
        {
          "action": "expect_text",
          "text": "请输入密码"
        }
      ]
    },
    "created_at": "2026-06-02T00:00:00Z",
    "updated_at": "2026-06-02T00:00:00Z",
    "applied_at": null
  }
}
```

`refine` 成功只表示建议已保存，不表示 TestCase 已更新。

### GET 修改历史列表

```text
GET /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refinements
```

响应：

```json
{
  "refinements": [
    {
      "id": 21,
      "test_case_id": 10,
      "user_prompt": "增加密码为空的校验",
      "summary": "新增密码为空输入和错误提示断言",
      "risk_notes": "需要确认错误提示文案稳定",
      "status": "proposed",
      "created_at": "2026-06-02T00:00:00Z",
      "updated_at": "2026-06-02T00:00:00Z",
      "applied_at": null
    }
  ],
  "count": 1
}
```

列表用于扫描历史，不返回完整 `OriginalBlueprint` 和 `RefinedBlueprint`。排序按 `created_at desc, id desc`。

### GET 修改建议详情

```text
GET /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refinements/:rid
```

响应同 `POST refine` 的 `refinement` 结构，包含 parsed `original_blueprint` 和 parsed `refined_blueprint`。

如果任一 Blueprint 存储内容腐坏，返回 `500`，不得静默返回空对象。

### POST 应用修改建议

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refinements/:rid/apply
```

请求体可以为空。

响应：

```json
{
  "test_case": {
    "id": 10,
    "page_id": 3,
    "title": "密码为空时提示必填",
    "description": "验证登录页密码为空的表单校验",
    "status": "active",
    "blueprint": {
      "title": "密码为空时提示必填",
      "description": "验证登录页密码为空的表单校验",
      "steps": [
        {
          "action": "fill",
          "target": { "placeholder": "密码" },
          "value": ""
        },
        {
          "action": "expect_text",
          "text": "请输入密码"
        }
      ]
    },
    "script_content": "",
    "created_at": "2026-06-02T00:00:00Z",
    "updated_at": "2026-06-02T00:10:00Z"
  },
  "refinement": {
    "id": 21,
    "test_case_id": 10,
    "status": "applied",
    "applied_at": "2026-06-02T00:10:00Z"
  }
}
```

应用必须在数据库事务中完成：

1. 重新加载 TestCase 和 Refinement。
2. 校验完整层级和 `status = proposed`。
3. 校验当前 TestCase Blueprint 与 `OriginalBlueprint` 等价。
4. 校验 `RefinedBlueprint` 符合当前 TestCase 状态下的 Blueprint 规则。
5. 更新 TestCase 标题、描述、Blueprint 和 `UpdatedAt`。
6. 标记 Refinement 为 `applied` 并写入 `AppliedAt`。

任一步失败，TestCase 和 Refinement 都保持原状。

### POST 放弃修改建议

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refinements/:rid/discard
```

请求体可以为空。

响应：

```json
{
  "refinement": {
    "id": 21,
    "test_case_id": 10,
    "status": "discarded",
    "applied_at": null
  }
}
```

放弃只更新 Refinement 状态，不修改 TestCase。只有 `proposed` 状态可以放弃。

### 错误响应

建议状态：

- `400`：请求 JSON 非法、prompt 为空、Refinement 状态非法、Playbot 输出结构非法、Blueprint 校验失败。
- `404`：project/version/page/testcase/refinement/execution 层级不存在或不匹配。
- `409`：应用时 TestCase Blueprint 已不同于 Refinement 的 `OriginalBlueprint`。
- `500`：LLM 配置缺失、Playbot 执行失败、数据库事务失败、已存 Blueprint 或 ReportData 腐坏。

错误响应沿用当前风格：

```json
{
  "error": "Refinement is stale; reload the TestCase and generate a new suggestion"
}
```

## 六、后端设计

### 新增/调整文件建议

- `backend/models/testing.go`
  - 扩展 `LLMRefinement` 字段。
- `backend/api/project_testcase_refinement.go`
  - 新增 Refinement API handlers、DTO、校验和事务逻辑。
- `backend/api/router.go`
  - 注册 refine、list、detail、apply、discard 路由。
- `backend/services/playbot/service.go`
  - 新增 `RefineTestCase` 和 `RefineOptions`，复用 Python/engine 路径解析、API Key 脱敏和 stdout/stderr 边界。
- `playbot-engine/cli.py`
  - 增加 `--mode generate|refine`，默认保持 `generate`，避免破坏 P1。
- `playbot-engine/app/agents/schemas.py`
  - 增加 refine 输入和输出 schema。
- 可选新增 `playbot-engine/app/agents/test_case_refinement_agent.py`
  - 独立承载自然语言修改提示词和结构化输出。

### Go 侧流程

生成建议：

1. 解析 project/version/page/testcase 参数。
2. 校验页面和用例层级。
3. 解析请求，校验 prompt。
4. 读取当前 TestCase，解析并规范化当前 Blueprint。
5. 读取可选 PageScript 上下文；缺失时记录 warning，不失败。
6. 如传入 `execution_id`，读取并解析对应 TestExecution.ReportData。
7. 读取 LLM 配置。
8. 调用 `playbot.RefineTestCase`。
9. 解析 Playbot 输出并按 TestCase 当前状态校验 refined Blueprint。
10. 保存 `LLMRefinement`，状态为 `proposed`。
11. 返回完整 Refinement 详情。

应用建议：

1. 解析 project/version/page/testcase/refinement 参数。
2. 在事务中重新读取 TestCase 和 Refinement。
3. 校验 `refinement.TestCaseID == testCase.ID` 和状态。
4. 校验 stale apply。
5. 使用 P2 同一套 Blueprint 校验和标题描述归一化逻辑构造最终 TestCase。
6. 保存 TestCase。
7. 保存 Refinement 状态和 `AppliedAt`。
8. 返回最新 TestCase 详情和 Refinement 状态摘要。

放弃建议：

1. 校验完整层级。
2. 在事务中确认状态为 `proposed`。
3. 标记 `discarded`。
4. 不读取或修改 TestCase.Blueprint。

### 辅助上下文规则

最近 PageScript 读取规则：

- 按 `created_at desc, id desc` 读取当前 page 下最近一条 PageScript。
- 没有 PageScript：传 `snapshot = null`、`intent_plan = null`，并加入 warning。
- 有 PageScript 但 JSON 非法：不阻止 refine，但加入 warning；不得静默替换成看似真实的空主流程。

执行报告读取规则：

- 只有请求显式传 `execution_id` 时读取。
- 执行记录必须属于当前 TestCase。
- `ReportData` 非法时返回错误，不调用 Playbot。

### Playbot service

建议新增：

```go
type RefineOptions struct {
    PageURL         string
    PageDescription string
    CurrentBlueprint any
    UserPrompt      string
    Snapshot        any
    IntentPlan      any
    ExecutionReport any
    ContextWarnings []string
    LLMEndpoint     string
    LLMAPIKey       string
    LLMModel        string
    PythonPath      string
    EngineDir       string
}

func RefineTestCase(ctx context.Context, opts RefineOptions) (string, error)
```

命令建议：

```text
python cli.py --mode refine --input <tmp.json> --llm-endpoint ... --llm-api-key ... --llm-model ...
```

`--mode` 缺省为 `generate`，保证 P1 生成调用向后兼容。stdout 仍只输出最终 JSON；stderr 可以输出流式过程日志。

### Playbot refine 输入输出

输入 JSON：

```json
{
  "mode": "refine",
  "page_url": "https://example.com/login",
  "page_description": "登录页",
  "current_blueprint": {},
  "user_prompt": "增加密码为空的校验",
  "snapshot": {},
  "intent_plan": {},
  "execution_report": null,
  "context_warnings": []
}
```

输出 JSON：

```json
{
  "refined_blueprint": {
    "title": "密码为空时提示必填",
    "description": "验证登录页密码为空的表单校验",
    "steps": [
      {
        "action": "fill",
        "target": { "placeholder": "密码" },
        "value": ""
      },
      {
        "action": "expect_text",
        "text": "请输入密码"
      }
    ]
  },
  "summary": "新增密码为空输入和错误提示断言",
  "risk_notes": "需要确认错误提示文案稳定",
  "error": null
}
```

Playbot 不直接写数据库，不返回 TestCase ID，不决定是否应用。应用权只在 BrowserWing 后端和用户确认动作里。

## 七、前端设计

### API 封装

在 `frontend/src/api/project.ts` 增加：

```ts
refineTestCase(projectId, versionId, pageId, testCaseId, data)
listTestCaseRefinements(projectId, versionId, pageId, testCaseId)
getTestCaseRefinement(projectId, versionId, pageId, testCaseId, refinementId)
applyTestCaseRefinement(projectId, versionId, pageId, testCaseId, refinementId)
discardTestCaseRefinement(projectId, versionId, pageId, testCaseId, refinementId)
```

补充类型：

- `TestCaseRefinementStatus`
- `TestCaseRefinementSummary`
- `TestCaseRefinementDetail`
- `RefineTestCaseRequest`
- `ListTestCaseRefinementsResponse`

### TestCase 详情页

在 `frontend/src/pages/TestCaseDetail.tsx` 增加自然语言修改区域：

- prompt 输入框。
- 可选“引用最近执行记录”或在执行历史中选择一条记录作为上下文。
- 生成建议按钮。
- 当前建议详情：摘要、风险提示、状态、创建时间。
- 修改前 Blueprint 和修改后 Blueprint 对比。
- 应用按钮。
- 放弃按钮。
- 历史列表。

前端规则：

- 本地表单存在未保存修改时，不允许直接发起 refine 或 apply；应提示用户先保存或放弃本地改动。因为 refine 使用的是后端已保存 Blueprint。
- 生成建议、应用、放弃过程中禁用保存、删除、执行和重复提交按钮。
- `refine` 成功后不改当前编辑表单，只展示建议。
- `apply` 成功后用后端返回的 TestCase 详情刷新表单，并刷新 Refinement 历史。
- `discard` 成功后只刷新历史和当前建议状态，不改表单。
- stale apply 返回 `409` 时，提示用户重新加载用例并重新生成建议。

### Diff 展示

P4 不强制后端返回 JSON Patch。前端可以：

- 将 `original_blueprint` 和 `refined_blueprint` 格式化为 JSON 文本。
- 使用轻量文本 diff 展示修改位置。
- 或首版先左右并排展示，再在后续体验优化中增加高亮 diff。

无论采用哪种展示，应用按钮必须只调用 apply API，不能在前端直接把 refined Blueprint 写入 P2 更新接口绕过 Refinement 状态记录。

## 八、契约红测建议

用例编写者应优先写后端契约红测：

1. `refine` 必须校验完整层级。
   - 期望：project/version/page/testcase 任一错配时返回 `404`，不调用 Playbot，不创建 Refinement。
2. `prompt` 为空时拒绝。
   - 期望：返回 `400`，不调用 Playbot，不创建 Refinement。
3. `refine` 只创建建议，不覆盖 TestCase。
   - 期望：保存 `OriginalBlueprint`、`RefinedBlueprint`、`Summary`、`RiskNotes`、`Status = proposed`；原 TestCase Blueprint、Title、Description、Status、ScriptContent 均不变。
4. 无主流程也可 refine。
   - 期望：页面没有 PageScript 时仍调用 Playbot，并在输入中携带上下文 warning；TestCase 可生成 proposed Refinement。
5. 显式 execution 上下文必须归属当前 TestCase。
   - 期望：匹配 execution 时将 report 传给 Playbot；错配 execution 返回 `404`，不调用 Playbot。
6. Playbot 失败或输出非法不落库。
   - 期望：stdout 非 JSON、`error` 非空、缺少 `refined_blueprint`、refined Blueprint 非法时返回错误，不创建 Refinement，不修改 TestCase。
7. 列表只返回摘要。
   - 期望：按 `created_at desc, id desc` 返回当前 TestCase 的历史，不返回完整 `OriginalBlueprint` 和 `RefinedBlueprint`，不泄露其他 TestCase 的记录。
8. 详情返回修改前后 Blueprint。
   - 期望：完整层级正确时返回 parsed Blueprint；`rid` 属于其他 TestCase 时返回 `404`；腐坏 Blueprint 返回 `500`。
9. 应用 proposed 建议。
    - 期望：应用后 TestCase Blueprint、Title、Description 更新；ScriptContent 和 Status 不变；Refinement 标记 `applied` 并写入 `AppliedAt`。
10. 应用建议可以清空描述。
    - 期望：`RefinedBlueprint.description` 为 `""` 时应用成功，TestCase.Description 同步为空字符串，Blueprint 顶层 description 也为空字符串。
11. 应用过期建议冲突。
    - 期望：建议生成后如果 TestCase Blueprint 已被修改，apply 返回 `409`；TestCase 不变，Refinement 仍是 `proposed`。
12. 非 proposed 不能应用。
    - 期望：`applied` 或 `discarded` 状态再次 apply 返回错误，TestCase 不变。
13. 放弃建议。
    - 期望：`discard` 把 proposed 标记为 `discarded`，不修改 TestCase；discarded 不能再 apply。
14. 应用失败事务保护。
    - 期望：RefinedBlueprint 不满足当前 TestCase 状态校验或保存失败时，TestCase 和 Refinement 都保持原状。
15. LLM 配置复用。
    - 期望：默认配置和指定配置读取走既有 BoltDB 入口；缺失/禁用/字段不完整时返回明确错误，不调用 Playbot。

测试约束：

- 不真实调用 LLM。
- 通过可注入的 Playbot refine 调用接口、临时 fake service 或命令替身控制 stdout/stderr。
- 不针对固定页面名、固定 prompt 文案、测试用户或测试路径写特殊逻辑。
- 不手写第二套 Blueprint 事实源；如需要校验，应走生产 API 或复用生产校验 helper。
- 涉及数据库状态的测试必须隔离并恢复全局 `storage.DB`。
- 红测写清期望行为、依据来源、当前失败形态和验证命令。

前端契约或集成用例后置补充：

- 输入 prompt 后能提交 refine 请求。
- 有未保存本地编辑时阻止 refine/apply。
- refine 成功后展示摘要、风险提示和修改前后 Blueprint。
- 未确认前当前表单不被覆盖。
- apply 成功后用后端返回的 TestCase 刷新详情表单。
- discard 成功后建议状态变化但 TestCase 表单不变。
- stale apply 展示明确错误并提示重新加载。

## 九、验收方式

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

1. 创建项目、版本、页面和 TestCase。
2. 在 TestCase 详情页输入“增加密码为空的校验”。
3. 生成建议后确认当前 TestCase 内容未变化。
4. 查看建议摘要、风险提示和修改前后 Blueprint。
5. 应用建议，确认 TestCase 标题、描述和 Blueprint 更新，ScriptContent 和 Status 不变。
6. 再生成一条建议后手工编辑并保存 TestCase。
7. 尝试应用旧建议，确认提示过期冲突且 TestCase 不变。
8. 生成一条建议并放弃，确认历史状态为 discarded，TestCase 不变。
9. 尝试通过错误 project/version/page/testcase/refinement URL 访问或应用，确认失败。

## 十、阶段外内容

以下内容不在 P4 完成范围内：

- 批量执行失败后的自动修复队列。
- 一键读取最近失败并自动发起修复。
- 直接生成或执行 `ScriptContent`。
- 完整手工编辑审计记录。
- 多用户创建人、成员权限和租户隔离。
- 结构化步骤可视化编辑器。
- JSON Patch 服务端 diff。
- Refinement 版本回滚树或多分支比较。

这些分别进入 P5、P6 或后续体验优化阶段。

## 十一、遗留风险

- 当前 `LLMRefinement` 模型字段不足，P4 必须结构性扩展模型，不能把修改前快照、状态和应用时间塞进摘要文本。
- P4 允许没有主流程时 refine，可能降低 Playbot 修改质量；前端应展示风险提示，不应伪造上下文已就绪。
- 过期防护依赖 Blueprint 规范化比较；实现时必须避免字符串空白、字段顺序导致误判。
- P4 只记录自然语言修改历史，P2 手工编辑历史仍未记录；完整审计需要后续单独设计。
- API Key 仍从 LLM 配置读取，日志中不得打印明文 key。
- P4 仍只有结构层级隔离，完整用户权限必须在 P5 收口。
