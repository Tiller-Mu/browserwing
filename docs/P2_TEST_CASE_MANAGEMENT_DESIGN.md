# P2 用例管理详细设计

本文档定义 P2 阶段的业务契约和验收口径。P2 承接 P1 已完成的“生成并保存 TestCase”链路，只补齐测试用例资产的列表、详情、手工创建、编辑、删除和状态管理，不提前实现 P3 用例执行、P4 自然语言修改和 P5 完整用户权限。

## 一、阶段目标

用户可以在业务页面下查看已有测试用例，进入用例详情，手工创建用例，编辑标题、描述、Blueprint、脚本文本和状态，并删除不再需要的用例。

P2 完成后应满足：

- 页面下的 TestCase 有独立列表和详情 API。
- TestCase 详情可以读取完整 Blueprint 和 ScriptContent。
- 用户可以手工创建 TestCase。
- 用户可以保存编辑后的标题、描述、Blueprint、ScriptContent 和状态。
- 用户可以删除指定 TestCase。
- 所有 TestCase 操作都校验 project/version/page/testcase 层级归属。
- Blueprint 仍是步骤和断言的结构化事实来源。
- 失败保存不得污染旧用例。

## 二、现有事实

依据当前代码和 P1 收尾记录：

- `backend/models/testing.go` 已有 `TestCase` 模型，字段包括 `PageID`、`Title`、`Description`、`Blueprint`、`ScriptContent`、`Status`、`CreatedAt`、`UpdatedAt`。
- `TestCase.Blueprint` 当前以字符串 JSON 存储。
- P1 已新增 `POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/generate`。
- P1 生成的 TestCase 默认 `Status = "active"`，`ScriptContent = ""`。
- P1 已确认 project/version/page 层级归属必须校验。
- `frontend/src/pages/TestPageManager.tsx` 已展示页面下的 `test_cases` 数量和卡片，但卡片尚未进入详情页。
- `frontend/src/api/project.ts` 已有 `TestCase` 类型和 `generateTestCases` API，但没有 TestCase CRUD API。
- `frontend/src/App.tsx` 尚未注册 TestCase 详情页路由。

推断：

- P2 可以复用 P1 的层级校验思路，但需要新增 testcase 归属校验，保证 `tcid` 属于指定 page。
- P2 不需要真实调用 Playbot，也不需要主流程录制；手工创建和编辑 TestCase 只要求页面存在并归属正确。

## 三、业务契约

### 1. 层级归属

所有 TestCase 读写删除 API 都必须校验：

- `project_id` 存在。
- `version_id` 属于该 project。
- `page_id` 属于该 version。
- 带 `tcid` 的接口还必须校验 TestCase 属于该 page。

任一层不匹配或不存在，返回 `404`。不得只凭 `tcid` 读取、更新或删除用例。

P2 仍不实现完整用户/租户隔离；P5 会补用户归属和项目成员权限。但 P2 必须先保证项目、版本、页面和用例之间的结构隔离。

### 2. TestCase 状态

P2 将 TestCase 的资产状态限定为：

- `active`：可见、可维护、后续 P3 可作为执行候选。
- `draft`：草稿，可保存不完整 Blueprint，但后续执行前必须补齐。
- `archived`：归档，不删除数据，列表和详情仍可查看。

规则：

- 创建时未传 `status`，默认为 `active`。
- 更新时如果传入 `status`，必须是上述三者之一。
- P2 不使用 `passed`、`failed`、`error` 表达 TestCase 资产状态；这些属于 P3 `TestExecution.Status`。
- 归档不是删除。删除仍通过 DELETE API 完成。

### 3. Blueprint

Blueprint 是测试步骤和断言的结构化事实来源。

P2 约束：

- 创建 TestCase 时必须提供合法 JSON object 作为 Blueprint。
- 更新时如果提供 Blueprint，也必须是合法 JSON object。
- 当 TestCase 状态为 `active` 时，Blueprint 必须包含非空 `steps` 数组。
- 当状态为 `draft` 时，Blueprint 可以暂时缺少 `steps` 或 `steps` 为空。
- Backend 存储前应将 Blueprint 规范化为 JSON 字符串；不得保存非法 JSON、空字符串或无法解析的对象。
- 如果 Blueprint 顶层包含 `title` 或 `description`，后端应使其与 TestCase 的 `Title`、`Description` 保持一致，避免列表显示字段和 Blueprint 元数据漂移。

说明：

- P2 不定义完整 Blueprint schema；P3 执行阶段会进一步定义 step action、定位策略和断言格式。
- P2 只校验 JSON object 和 active 状态下的基本 `steps` 可用性。

### 4. ScriptContent

`ScriptContent` 是可选的脚本文本产物，不是 P2 的执行事实来源。

P2 规则：

- 可以为空。
- 可以在详情页手工编辑保存。
- P2 不执行脚本，不校验脚本语法，不生成脚本。
- 后续 P3 如执行脚本，必须重新定义安全边界和执行策略。

### 5. 创建

用户可以在页面下手工创建 TestCase。

创建规则：

- 不要求页面已有主流程录制。
- `title` 必填，trim 后不能为空。
- `description` 可为空。
- `blueprint` 必填，必须符合 Blueprint 规则。
- `script_content` 可为空。
- `status` 可为空，默认 `active`。
- 创建成功后返回完整 TestCase 详情。

手工创建不得调用 Playbot，不得写入 PageScript。

### 6. 更新

用户可以更新 TestCase 的以下字段：

- `title`
- `description`
- `blueprint`
- `script_content`
- `status`

更新规则：

- PUT 采用部分更新语义：只更新请求中出现的字段，未出现字段保持不变。
- 如果请求中出现 `title`，trim 后不能为空。
- 如果请求中出现 `blueprint`，必须先通过校验，再写入数据库。
- 如果请求中出现 `status`，必须先通过状态校验。
- 如果任一字段校验失败，整次更新失败，旧 TestCase 保持不变。
- 成功更新后刷新 `updated_at`。

### 7. 删除

用户可以删除指定 TestCase。

删除规则：

- 删除前必须通过 project/version/page/testcase 层级归属校验。
- 删除是硬删除，后续如有 Refinement 和 Execution 记录，按模型级联关系清理。
- 删除成功后页面用例列表数量应减少。
- 删除不存在或不属于该页面的 TestCase 返回 `404`。
- 删除不影响同页面其他 TestCase，也不影响其他页面 TestCase。

### 8. 列表和详情

列表用于页面内扫描和进入详情，不应承载大型 Blueprint。

列表返回字段：

- `id`
- `page_id`
- `title`
- `description`
- `status`
- `created_at`
- `updated_at`

详情返回字段：

- 列表字段。
- `blueprint`，以 JSON object 返回给前端。
- `script_content`。

排序规则：

- 列表默认按 `updated_at desc, id desc` 排序。

## 四、API 契约

### GET 列表

```text
GET /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases
```

响应：

```json
{
  "test_cases": [
    {
      "id": 10,
      "page_id": 3,
      "title": "正确登录",
      "description": "验证正确账号密码可以登录",
      "status": "active",
      "created_at": "2026-06-02T00:00:00Z",
      "updated_at": "2026-06-02T00:00:00Z"
    }
  ],
  "count": 1
}
```

### POST 创建

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases
```

请求：

```json
{
  "title": "密码为空时提示必填",
  "description": "验证登录页密码为空的表单校验",
  "status": "draft",
  "blueprint": {
    "title": "密码为空时提示必填",
    "description": "验证登录页密码为空的表单校验",
    "steps": []
  },
  "script_content": ""
}
```

响应：

```json
{
  "test_case": {
    "id": 11,
    "page_id": 3,
    "title": "密码为空时提示必填",
    "description": "验证登录页密码为空的表单校验",
    "status": "draft",
    "blueprint": {
      "title": "密码为空时提示必填",
      "description": "验证登录页密码为空的表单校验",
      "steps": []
    },
    "script_content": "",
    "created_at": "2026-06-02T00:00:00Z",
    "updated_at": "2026-06-02T00:00:00Z"
  }
}
```

### GET 详情

```text
GET /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid
```

响应：

```json
{
  "test_case": {
    "id": 11,
    "page_id": 3,
    "title": "密码为空时提示必填",
    "description": "验证登录页密码为空的表单校验",
    "status": "draft",
    "blueprint": {
      "title": "密码为空时提示必填",
      "description": "验证登录页密码为空的表单校验",
      "steps": []
    },
    "script_content": "",
    "created_at": "2026-06-02T00:00:00Z",
    "updated_at": "2026-06-02T00:00:00Z"
  }
}
```

### PUT 更新

```text
PUT /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid
```

请求示例：

```json
{
  "title": "密码为空时提示必填",
  "status": "active",
  "blueprint": {
    "title": "密码为空时提示必填",
    "description": "验证登录页密码为空的表单校验",
    "steps": [
      { "action": "fill", "target_hint": { "placeholder": "密码" }, "value": "" },
      { "action": "expect_text", "value": "请输入密码" }
    ]
  }
}
```

响应同详情。

### DELETE 删除

```text
DELETE /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid
```

响应：

```json
{
  "message": "TestCase deleted successfully"
}
```

### 错误响应

建议状态：

- `400`：请求 JSON 非法、标题为空、状态非法、Blueprint 非法、active 用例缺少 steps。
- `404`：project/version/page/testcase 层级不存在或不匹配。
- `500`：数据库写入失败。

错误响应沿用当前风格：

```json
{
  "error": "Blueprint JSON invalid"
}
```

## 五、后端设计

### 新增/调整文件建议

- `backend/api/project_testcase.go`
  - 新增 TestCase CRUD handlers。
  - 复用或抽取 P1 的 project/version/page 层级校验 helper。
  - 新增 testcase 归属校验 helper。
- `backend/api/router.go`
  - 注册 TestCase CRUD 路由。
- `backend/api/project_testcase_test.go`
  - 写 P2 契约红测。

### DTO 和存储

后端 API 不直接把 GORM `TestCase` 原样作为详情响应，因为模型中的 `Blueprint` 是字符串。建议使用响应 DTO：

```go
type testCaseDetailResponse struct {
    ID            uint           `json:"id"`
    PageID        uint           `json:"page_id"`
    Title         string         `json:"title"`
    Description   string         `json:"description"`
    Status        string         `json:"status"`
    Blueprint     map[string]any `json:"blueprint"`
    ScriptContent string         `json:"script_content"`
    CreatedAt     time.Time      `json:"created_at"`
    UpdatedAt     time.Time      `json:"updated_at"`
}
```

列表 DTO 不返回 Blueprint 和 ScriptContent。

### Blueprint 处理

建议使用结构化 JSON 解析，不做字符串拼接。

保存前流程：

1. 将请求中的 `blueprint` 解析为 `map[string]any`。
2. 校验 object 非空。
3. 根据请求或最终 TestCase 状态判断是否必须包含非空 `steps`。
4. 将 `title`、`description` 写回 Blueprint 顶层字段。
5. `json.Marshal` 后保存到 `TestCase.Blueprint`。

详情返回流程：

1. 从数据库读取 `TestCase.Blueprint` 字符串。
2. 解析为 JSON object。
3. 解析失败时返回 `500`，并提示数据已损坏；不得静默返回空对象。

### 更新事务

更新和删除都应在数据库事务中执行：

- 更新时先加载并校验目标 TestCase。
- 合并请求字段和旧字段，得到最终 TestCase。
- 所有校验通过后一次性保存。
- 任一校验失败不得写入任何字段。

单行写入理论上是原子的，但事务能让失败路径和 P1 的资产保护契约保持一致。

## 六、前端设计

### API 封装

在 `frontend/src/api/project.ts` 增加：

```ts
listTestCases(projectId, versionId, pageId)
createTestCase(projectId, versionId, pageId, data)
getTestCase(projectId, versionId, pageId, testCaseId)
updateTestCase(projectId, versionId, pageId, testCaseId, data)
deleteTestCase(projectId, versionId, pageId, testCaseId)
```

补充类型：

- `TestCaseSummary`
- `TestCaseDetail`
- `CreateTestCaseRequest`
- `UpdateTestCaseRequest`

### 路由

新增详情页路由：

```text
/projects/:projectId/versions/:versionId/pages/:pageId/test-cases/:testCaseId
```

手工创建可以采用以下二选一：

- 在页面管理卡片中打开“新建用例”弹窗。
- 或使用 `testCaseId = new` 的详情页创建模式。

建议优先用弹窗创建最小草稿，然后进入详情页编辑，避免详情页同时承担过多初始化逻辑。

### 页面管理页

`TestPageManager` 中的用例卡片：

- 点击进入 TestCase 详情页。
- 用例数量在创建、删除、生成后刷新。
- 状态展示使用 `active`、`draft`、`archived`，不得继续把 `passed/failed` 当作 TestCase 状态。
- 有主流程时继续保留智能生成入口。
- 可以新增“新建用例”按钮；该按钮不要求主流程录制。

### 用例详情页

新增页面建议命名：

```text
frontend/src/pages/TestCaseDetail.tsx
```

页面结构：

- 顶部：返回页面列表、标题输入、状态选择、保存、删除。
- 基本信息区：标题、描述、状态。
- Blueprint 区：JSON textarea 或轻量编辑器，保存前前端先 `JSON.parse` 校验。
- Script 区：textarea 编辑 `script_content`。

P2 不展示以下空入口：

- 执行按钮。
- 执行记录 tab。
- 自然语言修改面板。
- 修改历史 tab。

这些入口分别等 P3/P4 有真实后端契约后再出现。

### 前端保存规则

- 保存前校验标题非空。
- 保存前校验 Blueprint 是合法 JSON object。
- 如果状态是 `active`，保存前提示或阻止缺少非空 `steps` 的 Blueprint。
- 后端返回错误时展示错误，不覆盖本地旧详情。
- 保存成功后以响应内容刷新页面状态。
- 删除前弹确认；删除成功后返回页面管理页并刷新列表。

## 七、契约红测建议

用例编写者应优先写后端契约红测：

1. 列表只返回指定 page 下的 TestCase。
   - 期望：跨 page/version/project 的 TestCase 不出现。
   - 期望：列表项不返回大型 Blueprint 和 ScriptContent。
2. 详情读取必须校验完整层级。
   - 期望：正确层级返回完整详情和 parsed Blueprint。
   - 期望：错配 project/version/page/testcase 返回 `404`。
3. 手工创建 TestCase。
   - 期望：标题、Blueprint、状态合法时保存成功。
   - 期望：未传 status 默认 `active`。
   - 期望：手工创建不要求主流程，也不调用 Playbot。
4. 创建校验。
   - 期望：空标题、非法 status、非法 Blueprint、active 缺少 steps 返回 `400` 且不落库。
5. 更新 TestCase。
   - 期望：部分更新只改请求字段。
   - 期望：更新 Blueprint 后保存为合法 JSON，并保持 title/description 同步。
   - 期望：更新后 `updated_at` 变化。
6. 更新失败不污染旧用例。
   - 期望：非法 Blueprint、非法 status、空标题时旧 TestCase 完全不变。
7. 删除 TestCase。
   - 期望：正确层级删除目标用例。
   - 期望：删除不影响同页面其他用例或其他页面用例。
   - 期望：层级错配返回 `404` 且不删除。
8. 状态契约。
   - 期望：`active`、`draft`、`archived` 可保存。
   - 期望：`passed`、`failed`、`error` 不能作为 TestCase.Status 保存。

测试约束：

- 不真实调用 Playbot。
- 复用生产 `models.TestCase`、GORM schema、路由和公共 helper。
- 不手写第二套 Blueprint 校验事实源；如后端提供校验 helper，测试应走 API 或 helper。
- 涉及数据库状态的测试必须隔离并恢复全局 `storage.DB`。
- 红测写清期望行为、依据来源、当前失败形态和验证命令。

前端契约或集成用例后置补充：

- 用例卡片点击进入详情页。
- 新建用例弹窗或创建流程能创建草稿。
- 详情页保存成功后展示后端返回内容。
- 保存失败时仍保留原详情。
- 删除成功后返回页面管理并刷新数量。

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

人工验收：

1. 创建项目、版本、页面。
2. 在页面下手工新建一个 draft 用例。
3. 打开详情页，编辑标题、描述、Blueprint、ScriptContent、状态。
4. 保存后刷新页面，确认内容保留。
5. 将状态改为 active，确认缺少 steps 时不能保存。
6. 为 active Blueprint 补充 steps 后保存成功。
7. 删除该用例，确认页面列表数量减少。
8. 尝试通过错误 page/version/project URL 访问该用例，确认返回错误或无法展示。

## 九、阶段外内容

以下内容不在 P2 完成范围内：

- 执行 TestCase。
- 保存 TestExecution 和展示执行报告。
- 从 Blueprint 解释执行动作。
- 自然语言修改用例。
- LLMRefinement 历史。
- 完整手工修改审计记录。
- 多用户项目成员权限。
- 高质量语义快照增强。

这些分别进入 P3、P4、P5 或后续稳定化阶段。

## 十、遗留风险

- 当前模型没有独立的 TestCase 来源字段，也没有完整修改历史表；P2 不应伪造“来源/历史已完成”，后续需在 P4 或稳定化阶段补齐。
- `TestPageManager` 当前通过 `ListPages` 预加载 `TestCases`，P2 详情和管理应使用专用 TestCase API，避免页面列表承载大型 Blueprint。
- `TestCase.Status` 需要从前端执行状态展示中剥离；P3 执行状态应来自 `TestExecution`，不能继续混用。
- 现有 Blueprint schema 尚未完整定义；P2 只做基础 JSON object 和 active steps 校验，P3 必须补执行级 schema。
