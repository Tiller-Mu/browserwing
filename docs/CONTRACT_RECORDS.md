# 契约记录

本文档记录各阶段已经确认并通过红测或 review 固化的业务契约。格式承接 `docs/CONTRACT_WORKFLOW.md`。

## P1：生成测试用例链路

### 主流程和层级归属

契约：
生成测试用例前必须先确认 project、version、page 三层归属一致，并且目标页面存在当前有效主流程录制。缺少主流程或层级不匹配时不得调用 Playbot，也不得写入 TestCase。

依据：
来自 `docs/P1_TEST_CASE_GENERATION_DESIGN.md` 的 P1 业务契约，以及项目/版本/页面已有 schema 和公开 API 路由结构。

当前/历史问题：
P1 之前已有 PageScript 保存入口和前端录制入口，但没有生成接口；如果只按 page_id 直接生成，可能污染错误项目版本下的测试资产。

验证：
`TestGenerateTestCasesRejectsPageWithoutMainFlow` 和 `TestGenerateTestCasesRejectsMismatchedProjectVersionPageHierarchy` 覆盖该契约；后续实现和 review 均按此边界验收。

### 生成模式和保存规则

契约：
生成接口支持 `preview`、`append`、`replace` 三种模式。`preview` 只返回生成结果不落库；`append` 保留旧用例并追加新用例；`replace` 成功时以事务覆盖旧用例，保存失败或 Playbot 失败时旧用例必须保持不变。

依据：
来自 `docs/P1_TEST_CASE_GENERATION_DESIGN.md` 对生成模式和可靠性的要求。测试用例是核心资产，生成失败不得破坏已有资产。

当前/历史问题：
P1 之前不存在 TestCase 生成保存入口，前端只有空按钮，无法形成可维护的测试资产闭环。

验证：
`TestGenerateTestCasesPreviewReturnsCasesWithoutPersisting`、`TestGenerateTestCasesAppendKeepsExistingCasesAndAddsGeneratedCases`、`TestGenerateTestCasesReplaceAtomicallyOverwritesExistingCases`、`TestGenerateTestCasesReplaceKeepsOldCasesWhenSavingGeneratedCasesFails` 和 `TestGenerateTestCasesPlaybotFailureDoesNotDamageExistingCases` 覆盖该契约。

### Blueprint 保存和 Playbot 输出校验

契约：
Playbot 返回的 stdout 必须是合法 JSON，并且包含非空 `test_cases`。每个用例至少要有非空标题、描述和步骤。后端保存 TestCase 时以单个用例 Blueprint JSON 作为事实来源，P1 阶段 `ScriptContent` 可以为空。

依据：
来自核心需求中“Blueprint 是事实来源”和 P1 设计中的 Playbot 输出契约。

当前/历史问题：
如果直接保存未校验的 LLM 输出，会把无法查看、无法编辑或无法执行的内容固化为测试资产。

验证：
`TestGenerateTestCasesAppendKeepsExistingCasesAndAddsGeneratedCases` 验证 Blueprint 保存和 `ScriptContent` 为空；`TestGenerateTestCasesRejectsInvalidPlaybotOutputWithoutSaving` 覆盖非法输出拒绝保存。

### LLM 和 Playbot 配置来源

契约：
生成接口必须复用已有 LLM 配置存储读取启用配置；Playbot Python 和 engine 目录必须通过配置或环境变量解析。日志和命令展示不得泄露 LLM API Key。显式配置的无效 engine 目录不能静默回退到默认目录。

依据：
来自 P1 设计中“不能引入第二套 LLM 配置来源”和“Playbot 调用路径配置化”的约束，以及安全要求中“不硬编码密钥、令牌或凭据”。

当前/历史问题：
P1 之前 Playbot service 使用硬编码 `python`，生成接口也不存在可复用的 LLM 配置读取链路。

验证：
`TestResolveEngineDirRejectsInvalidExplicitPathWithoutFallback` 和 `TestResolveEngineDirRejectsInvalidEnvPathWithoutFallback` 覆盖 engine 目录解析契约；P1 实现中 `ProjectHandlers` 注入 BoltDB/config，并在 Playbot 命令日志中脱敏 API Key。

### 前端生成入口

契约：
业务页面已有主流程时，前端应提供真实“智能生成测试用例”入口。用户可以选择生成模式、LLM 配置和额外说明；保存模式成功后刷新页面用例数量，预览模式不改变数量，失败时展示后端错误且不改变当前列表。

依据：
来自 P1 设计中的前端任务和验收标准，以及核心需求中“生成过程有明确 loading 和错误提示”的要求。

当前/历史问题：
P1 之前页面管理中已有按钮外观，但未接真实 API，无法形成页面录制到 TestCase 保存的闭环。

验证：
P1 代码审核确认前端入口已接入生成接口和刷新逻辑；后续 P2 如新增详情页，应继续沿用同一 TestCase 数据来源，不另建前端临时事实源。

## P2：用例管理

### TestCase 管理层级

契约：
TestCase 列表、创建、详情、更新和删除 API 都必须校验 project、version、page、testcase 完整层级归属。不能只凭 `tcid` 读取、修改或删除用例；任一层级不匹配都应返回 404，并且不得修改数据。

依据：
来自 `docs/P2_TEST_CASE_MANAGEMENT_DESIGN.md` 的 API 契约，以及 P1 已确认的项目、版本、页面层级归属规则。P2 管理的是用户核心测试资产，必须继承同一归属边界。

当前/历史问题：
P2 之前没有 TestCase 管理接口；如果按裸 `tcid` 操作，会允许跨页面或跨版本误删、误改测试资产。

验证：
`TestGetTestCaseRequiresFullHierarchyAndReturnsDetail`、`TestCreateTestCaseRequiresProjectVersionPageHierarchy`、`TestUpdateTestCaseRequiresFullHierarchyWithoutMutatingExistingCase` 和 `TestDeleteTestCaseRemovesOnlyTargetAndRequiresHierarchy` 覆盖该契约。

### 列表摘要和详情事实源

契约：
TestCase 列表接口只返回轻量摘要，不返回大体积 `Blueprint` 和 `ScriptContent`；详情接口返回完整可编辑资产，并将 `Blueprint` 解析为结构化 JSON 对象。已损坏的 Blueprint 不能静默伪造成空对象或默认步骤，应返回明确错误。

依据：
来自 P2 设计中“列表用于扫描、详情用于编辑”的数据边界，以及核心需求中 Blueprint 是用例事实来源的要求。

当前/历史问题：
如果列表返回完整 Blueprint 和脚本，会让页面扫描接口承担编辑事实源职责；如果详情对损坏 Blueprint 做隐藏兜底，会掩盖资产损坏并污染后续执行。

验证：
`TestListTestCasesReturnsOnlyPageSummaries` 和 `TestGetTestCaseRequiresFullHierarchyAndReturnsDetail` 覆盖列表/详情边界；详情读取腐坏 Blueprint 的失败路径在同组详情契约中验收。

### 手工创建和 Blueprint 校验

契约：
P2 支持手工创建 TestCase，不要求页面已有主流程录制，也不调用 Playbot。创建时标题必填，`status` 缺省为 `active`；`active` 用例必须包含非空 `steps`，`draft` 可以保存不完整草稿。后端不能只依赖前端校验。

依据：
来自 P2 设计中“用例管理独立于生成链路”的约束。P1 生成是上游来源之一，P2 详情管理必须允许用户维护测试资产本身。

当前/历史问题：
如果手工创建复用 P1 的主流程前置条件，会导致非生成来源的测试资产无法维护；如果后端不校验 Blueprint，后续执行阶段会拿到不可执行资产。

验证：
`TestCreateTestCaseManuallyDefaultsActiveWithoutMainFlowOrPlaybot`、`TestCreateTestCaseValidationRejectsInvalidInputWithoutSaving` 和 `TestTestCaseStatusContract` 覆盖该契约。

### 更新安全和字段归一化

契约：
TestCase 更新是部分更新，只变更请求中明确提交的字段。标题和描述更新后必须同步归一化到 Blueprint 顶层；`ScriptContent` 可以独立保存。校验失败时旧 TestCase 必须保持不变，`updated_at` 只在有效更新后变化。

依据：
来自 P2 设计中“编辑保存不能污染既有资产”的要求，以及 Blueprint 作为结构化事实源的约束。

当前/历史问题：
如果更新失败后已部分写入，会产生标题、摘要、Blueprint 和脚本内容不一致；如果标题描述只改外层字段，后续生成、执行或展示可能读到旧 Blueprint 信息。

验证：
`TestUpdateTestCasePartiallyNormalizesBlueprintSavesScriptContentAndUpdatesTimestamp` 和 `TestUpdateTestCaseValidationFailureDoesNotMutateExistingCase` 覆盖该契约。

### 状态边界

契约：
P2 的 TestCase 资产状态只允许 `active`、`draft`、`archived`。`passed`、`failed`、`error` 属于 P3 执行记录状态，不能混入 TestCase 本体状态，也不能在 P2 前端提供假执行结果入口。

依据：
来自 P2 设计中的状态定义，以及 P3 对执行记录模型的阶段边界。测试资产状态和执行结果状态必须分离，避免一个字段表达两类事实。

当前/历史问题：
如果把执行状态写进 TestCase `status`，会导致多次执行、历史报告和资产可用性互相覆盖。

验证：
`TestTestCaseStatusContract` 覆盖状态白名单；P2 代码审核确认前端详情页没有引入 P3/P4 的假执行、假报告或假自然语言修改入口。

### 删除隔离

契约：
删除 TestCase 时只删除目标用例，不影响同页其他用例，也不影响其他页面的用例。层级不匹配时不得删除任何数据。

依据：
来自 P2 设计中“删除为资产级操作”的要求，以及测试资产不能因列表刷新或页面误配被批量破坏的可靠性要求。

当前/历史问题：
如果删除逻辑按 page 或 version 误删，可能破坏已有生成用例和手工维护用例，影响后续执行闭环。

验证：
`TestDeleteTestCaseRemovesOnlyTargetAndRequiresHierarchy` 覆盖该契约。
