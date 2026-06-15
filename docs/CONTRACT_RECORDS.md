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

## P3：用例执行

### 执行层级和执行前校验

契约：
TestCase 执行、执行记录列表和执行记录详情都必须校验 project、version、page、testcase 完整层级归属；执行详情还必须校验 execution 属于当前 TestCase。只有 `active` TestCase 可以执行。层级不匹配、非 active 状态、腐坏 Blueprint、缺少可执行 steps、未知 action、缺少必需字段、超出 timeout 限制等执行前失败，不得调用 runner，也不得创建 TestExecution。

依据：
来自 `docs/P3_TEST_CASE_EXECUTION_DESIGN.md` 的 P3 执行前校验契约，以及 P1/P2 已确认的层级归属规则。执行记录代表真实运行，执行尚未开始的资产错误不应制造历史记录。

当前/历史问题：
P3 之前没有 TestCase 执行 API；如果执行接口只按 testcase_id 或 execution_id 操作，会破坏项目/版本/页面隔离。如果把草稿或不可执行 Blueprint 也落成执行记录，会污染报告历史。

验证：
`TestRunTestCaseRequiresHierarchyBeforeExecutionValidation`、`TestRunTestCaseRejectsNonActiveAndInvalidBlueprintWithoutExecution` 和 `TestRunTestCaseBlueprintSchemaRejectsInvalidStepsWithoutExecution` 覆盖该契约。

### Blueprint 执行事实源

契约：
P3 首版只解释执行 Blueprint，不直接执行 `ScriptContent`，也不把 `ScriptContent` 当作隐藏 fallback。Blueprint 必须包含非空 steps，并且 step action 只允许 `navigate`、`click`、`fill`、`select`、`wait`、`expect_visible`、`expect_text`。`target_hint` 是 `target` 的兼容别名。`wait.duration_ms` 最大 30000，step `timeout_ms` 最大 60000。

依据：
来自核心需求中“Blueprint 是事实来源”和 P3 设计中“脚本执行安全边界后置”的决策。`ScriptContent` 可以作为资产保存和展示，但不能在没有沙箱、权限和审计设计时执行。

当前/历史问题：
如果 Blueprint 不可执行时静默切到脚本，会引入第二套事实源并绕过 P3 红测；如果未知 action 被跳过，会让执行结果看似 passed 但实际漏跑步骤。

验证：
`TestRunTestCaseRejectsNonActiveAndInvalidBlueprintWithoutExecution` 覆盖 ScriptContent 不作为 fallback；`TestRunTestCaseBlueprintSchemaRejectsInvalidStepsWithoutExecution` 覆盖 action、target、value、duration 和 timeout 校验。

### 导航和定位归一化

契约：
默认执行 URL 来自 `ProjectVersion.BaseURL` + `TestPage.Path`。如果第一条 step 不是 `navigate`，runner 先执行默认页面 URL 导航，并在 ReportData 中记录 `initial_navigation.mode = "default"`、`step_index = null`。如果第一条 step 是 `navigate`，runner 跳过默认导航，只执行显式 navigate，并记录 `initial_navigation.mode = "explicit_step"`、`step_index = 0`。定位转换中，target 同时提供 `role` 和 `text` 时必须优先使用 `role + text`，缺少 role 时才退回纯 `text`。

依据：
来自 P3 设计 review 后补充的执行顺序和定位优先级约束。该约束避免双导航，也避免 target 同时有 role/text 时退化成不精确的纯文本定位。

当前/历史问题：
如果实现先默认导航再执行首步 navigate，会产生双导航；如果纯 text 先于 role+text，会降低定位精度并让 role 约束失效。

验证：
`TestRunTestCaseBlueprintCompatibilityAndNavigationReport` 覆盖默认导航、显式首步 navigate、`target_hint` 兼容和 role+text 定位优先。

### 执行状态和资产状态分离

契约：
只要执行已经开始，runner 返回 `passed`、`failed` 或 `error` 都必须保存为 TestExecution，并返回执行报告。断言失败保存 `failed`，运行时或浏览器动作异常保存 `error`，全部步骤通过保存 `passed`。执行结果不得修改 TestCase.Status；TestCase.Status 仍只表示 `active`、`draft`、`archived` 资产状态。

依据：
来自 P2 状态边界和 P3 执行状态设计。测试资产是否可维护和一次执行是否通过是两类事实，不能共用字段。

当前/历史问题：
如果执行结果写回 TestCase.Status，会让一次失败覆盖资产管理状态，也会让多次执行历史互相覆盖。

验证：
`TestRunTestCasePersistsExecutionStatusesAndKeepsTestCaseStatusActive` 覆盖 passed、failed、error 保存和 TestCase.Status 不变。

### 执行报告和历史读取

契约：
TestExecution.ReportData 是执行报告事实源，保存为 JSON 字符串，详情响应必须解析为 JSON object。报告至少包含 `source = "blueprint"`、`execution_url`、`initial_navigation`、`summary`、`steps`、`final_url`，失败步骤要保留错误摘要。执行记录列表只返回 summary，不返回完整 `report_data`；列表只包含当前 TestCase 的记录，按 `created_at desc, id desc` 排序，默认 20 条，最多 50 条。腐坏 ReportData 读取详情时返回 `500`，不得静默返回空报告。

依据：
来自 P3 设计中“报告可诊断、列表轻量、详情为报告事实源”的约束，以及 P2 列表/详情边界。

当前/历史问题：
如果列表泄露完整 report_data，会让扫描接口承担报告详情职责；如果腐坏报告静默兜底，会掩盖执行历史损坏。

验证：
`TestListTestCaseExecutionsScopesSortsAndOmitsReportData`、`TestGetTestCaseExecutionRequiresHierarchyAndParsesReportData` 和 `TestGetTestCaseExecutionRejectsCorruptReportData` 覆盖该契约。

### 前端执行入口

契约：
TestCase 详情页提供真实执行入口，只允许 active 用例执行；执行中禁用保存、删除和再次执行；执行完成后刷新执行历史和最近报告。前端展示执行状态、耗时、错误信息、步骤状态、失败步骤和截图链接。页面用例卡片最近执行状态不属于 P3 必交付，不能用 TestCase.Status 伪造执行结果。

依据：
来自 P3 设计和实现后的前端行为。P3 的前端闭环聚焦 TestCase 详情页，不提前做页面列表 latest_execution summary。

当前/历史问题：
P2 详情页没有执行入口；如果 P3 在页面卡片上伪造最近执行状态，会混淆资产状态和执行状态。

验证：
P3 代码审核确认 `frontend/src/pages/TestCaseDetail.tsx` 已接入执行 API、执行历史和报告展示；页面用例卡片 latest_execution 后置。

## P4：自然语言修改

### Refinement 层级和输入边界

契约：
TestCase Refinement 的生成、列表、详情、应用和放弃接口都必须校验 project、version、page、testcase、refinement 完整层级归属。`refine` 请求的 prompt 必须非空；显式传入 execution 上下文时，execution 必须属于当前 TestCase，且 ReportData 必须是合法 JSON。层级错配、prompt 为空或 execution 上下文不合法时不得调用 Playbot，也不得创建 LLMRefinement。

依据：
来自 `docs/P4_NATURAL_LANGUAGE_REFINEMENT_DESIGN.md` 的 P4 层级归属、prompt 和 execution 上下文契约。P4 管理的是核心测试资产的修改建议，必须继承 P1-P3 的结构隔离规则。

当前/历史问题：
P4 之前没有自然语言修改 API；如果只凭 `tcid`、`rid` 或 `execution_id` 操作，会允许跨页面、跨用例读取或应用修改建议，造成测试资产污染。

验证：
`TestRefineTestCaseRequiresHierarchyAndPrompt`、`TestRefineTestCaseAllowsMissingMainFlowAndRequiresOwnedExecutionContext`、`TestListTestCaseRefinementsScopesSortsAndOmitsBlueprints` 和 `TestGetTestCaseRefinementRequiresHierarchyAndParsesBlueprints` 覆盖该契约。

### 建议生成不覆盖原用例

契约：
`refine` 只调用 Playbot 生成修改建议，并保存为 `Status = proposed` 的 LLMRefinement。它必须保存用户 prompt、修改前 Blueprint、修改后 Blueprint、summary 和 risk_notes，但不得修改 TestCase 的 Title、Description、Blueprint、ScriptContent 或 Status。

依据：
来自核心需求中“用户可以选择应用或放弃”和 P4 设计中“修改建议和应用必须分离”的要求。LLM 输出不能直接覆盖核心测试资产。

当前/历史问题：
如果自然语言修改接口一步覆盖 TestCase，用户无法比较修改前后 Blueprint，也无法阻止错误 LLM 输出破坏既有用例。

验证：
`TestRefineTestCaseCreatesProposedSuggestionWithoutMutatingTestCase` 覆盖生成建议不修改 TestCase；前端实现中 `refine` 成功后只展示建议，不写入编辑表单。

### Playbot refine 输出和上下文

契约：
P4 调用 Playbot refine 时，至少传入当前 Blueprint、页面 URL、页面描述和用户 prompt；主流程录制和页面快照是可选上下文。没有 PageScript 不阻止 refine，但必须显式传 `context_warnings`，不得伪造空主流程。Playbot stdout 必须是合法 JSON，并包含合法 `refined_blueprint`、非空 summary、risk_notes 和 error 字段。非法 JSON、`error` 非空、缺少 refined_blueprint、summary 为空、active 用例 refined Blueprint 缺少非空 steps 或 title/description 不符合规则时，不创建 LLMRefinement，也不修改 TestCase。

依据：
来自 `docs/P4_NATURAL_LANGUAGE_REFINEMENT_DESIGN.md` 的 Playbot 输入输出契约，以及 P1 已确认的 LLM 配置和 Playbot stdout/stderr 边界。

当前/历史问题：
如果缺少主流程时直接拒绝 refine，会让手工创建用例无法自然语言维护；如果后端接受非法 LLM 输出，会把不可应用或不可执行的修改建议固化到历史里。

验证：
`TestRefineTestCaseAllowsMissingMainFlowAndRequiresOwnedExecutionContext` 覆盖无主流程 warning 和 execution report 传递；`TestRefineTestCaseRejectsInvalidPlaybotOutputWithoutSaving` 覆盖非法 Playbot 输出拒绝保存。

### Refinement 历史列表和详情

契约：
Refinement 列表只返回当前 TestCase 下的轻量摘要，按 `created_at desc, id desc` 排序，不返回完整 OriginalBlueprint 和 RefinedBlueprint。Refinement 详情返回 parsed `original_blueprint` 和 `refined_blueprint`；腐坏 Blueprint 详情读取返回错误，不静默伪造空对象。

依据：
来自 P4 设计中“列表用于扫描、详情用于比较”的边界，也延续 P2/P3 对列表和详情事实源的分离方式。

当前/历史问题：
如果列表泄露完整 Blueprint，会让扫描接口承担详情职责；如果腐坏 Blueprint 被静默兜底，会掩盖修改历史损坏并影响后续应用判断。

验证：
`TestListTestCaseRefinementsScopesSortsAndOmitsBlueprints` 覆盖列表隔离、排序和摘要字段；`TestGetTestCaseRefinementRequiresHierarchyAndParsesBlueprints` 覆盖详情解析、层级和腐坏数据错误。

### 应用、过期防护和放弃

契约：
只有 `proposed` Refinement 可以应用或放弃。应用时必须比较当前 TestCase Blueprint 与 LLMRefinement.OriginalBlueprint 的规范化 JSON；如果不等价，返回 `409` 并保持 TestCase 和 Refinement 不变。应用成功只更新 TestCase 的 Title、Description 和 Blueprint，保留 ScriptContent 和 Status，并把 Refinement 标记为 `applied`、写入 AppliedAt。`description` 可以被同步为空字符串。放弃只把 Refinement 标记为 `discarded`，不修改 TestCase；applied 或 discarded 不能再次应用。

依据：
来自 P4 设计中“用户确认应用”“旧建议不能覆盖新内容”和 P2/P3 已确认的 TestCase 状态与 ScriptContent 边界。自然语言修改只作用于 Blueprint 事实源，不引入脚本 fallback。

当前/历史问题：
如果旧建议能在 TestCase 已被手工编辑后继续应用，会覆盖用户后续保存的新 Blueprint；如果 apply 失败后部分写入，会造成 TestCase 与 Refinement 状态不一致。

验证：
`TestApplyProposedRefinementUpdatesCaseAndMarksApplied`、`TestApplyRefinementAllowsEmptyDescription`、`TestApplyRefinementRejectsStaleOrNonProposedWithoutMutating`、`TestDiscardRefinementDoesNotMutateCaseAndBlocksApply` 和 `TestApplyRefinementValidationFailureKeepsTransactionAtomic` 覆盖应用、清空描述、过期冲突、非 proposed 拒绝、放弃和事务保护。

### LLM 配置和前端入口

契约：
P4 refine 必须复用既有 BoltDB LLM 配置读取链路；默认配置和指定配置都必须是启用且字段完整的配置。缺失、禁用或字段不完整时返回明确错误，不调用 Playbot，并且响应和日志不得泄露 API Key。前端 TestCase 详情页提供真实自然语言修改入口；有未保存本地编辑时必须阻止 refine 和 apply，避免后端基于旧 Blueprint 生成或应用建议。

依据：
来自 P1 已确认的 LLM 配置事实源和 P4 前端设计。P4 不新增第二套 LLM 配置，也不允许前端绕过 apply API 直接用 P2 更新接口写入 refined Blueprint。

当前/历史问题：
如果 P4 另建 LLM 配置来源，会产生密钥管理和模型选择分裂；如果前端在本地未保存状态下发起 refine/apply，会让用户以为修改基于当前屏幕内容，实际基于后端旧 Blueprint。

验证：
`TestRefineTestCaseReusesExistingLLMConfigSelection` 覆盖 LLM 配置复用和密钥不泄露；P4 代码审核确认 `frontend/src/pages/TestCaseDetail.tsx` 已接入自然语言修改、历史展示、应用/放弃和未保存编辑拦截。

## P4.5：录制体验和项目登录态

### 项目登录态生命周期和脱敏

契约：
`ProjectAuthState` 是某个 `ProjectVersion` 的默认登录态，必须同时按 ProjectID 和 VersionID 校验归属。普通查询、前端展示、Playbot 输入、执行报告和错误响应只能使用摘要，不得返回 Cookie、localStorage 或 sessionStorage 的明文 value。捕获空状态或保存失败时不能替换已有 active 登录态；删除 ProjectVersion 时必须清理对应登录态。

依据：
来自 P4.5 设计中的 ProjectAuthState 模型、敏感字段边界和版本隔离规则。登录态是敏感资产，不能用普通详情接口、报告或日志泄露原始 `StateJSON`。

当前/历史问题：
如果只按 AuthState ID 读取，会造成跨版本串用；如果捕获失败仍覆盖旧状态，会让后续业务录制和执行突然失效；如果响应带出明文 value，会把自动化凭据暴露给前端和报告消费者。

验证：
`TestCaptureProjectAuthStateScopesToProjectVersionAndRedactsValues`、`TestDeleteProjectAuthStateScopesAndMakesProjectSavedRunFail`、`TestDeleteVersionDeletesScopedProjectAuthState` 和 `TestCaptureProjectAuthStateRejectsEmptyStateAndKeepsPreviousOnFailure` 覆盖版本隔离、脱敏、删除和失败不变更。

### 录制会话口径和 PageScript 元数据

契约：
项目页面录制只允许 `login_flow` 和 `business_flow` 两类；`login_flow` 必须使用 `clean`，`business_flow` 可以显式选择 `clean` 或 `project_saved`。`project_saved` 录制在当前版本没有 active 登录态时必须前置失败。项目页面录制必须使用隔离上下文，不能自动加载全局 `browser` Cookie Store。新前端保存 PageScript 必须写入 `recording_meta`，旧前端缺失该字段时按兼容语义视为 `business_flow + clean`。

依据：
来自 P4.5 设计对登录页可录制、业务流程可复用项目登录态以及旧录制兼容的要求。录制元数据是后续 Playbot 生成和 TestCase 执行口径的事实来源。

当前/历史问题：
如果登录流程自动复用项目登录态，会跳过登录页导致登录脚本无法录制；如果业务流程显式选择 `clean` 后生成又改成 `project_saved`，会让没有登录态的项目执行前失败；如果旧 PageScript 缺元数据被默认为 `project_saved`，会破坏 P1-P4 已存在用例。

验证：
`TestStartLoginFlowRecordingUsesCleanContext`、`TestStartBusinessFlowRecordingRequiresAndRestoresProjectAuthState`、`TestSavePageRecordingPersistsRecordingMetaAndValidatesAuthContext` 和 `TestSavePageRecordingAllowsLegacyMissingRecordingMetaAsClean` 覆盖录制口径、项目登录态恢复、元数据持久化、非法元数据拒绝和旧前端兼容。

### 生成和执行的 auth_context 继承

契约：
从 PageScript 生成 TestCase 时，后端必须优先继承 `recording_meta.auth_context`；`business_flow + clean` 生成结果必须保持 `clean`，旧 PageScript 缺元数据按 `clean` 处理。Playbot 输出中的非法 `auth_context` 必须拒绝保存，`replace` 模式下不得删除旧 TestCase。执行 TestCase 时，请求可显式覆盖 `auth_context`；未传请求值时读取 Blueprint 顶层；旧 Blueprint 缺字段时按 `clean` 执行并记录 legacy 来源。

依据：
来自 P4.5 设计中“录制选择是生成事实源”和“旧用例兼容不改变 P1-P4 行为”的边界。Playbot 不负责登录态恢复，后端负责把合法会话口径落入 Blueprint 和执行输入。

当前/历史问题：
如果只按 `recording_kind` 推断，会丢失用户显式选择的 clean 业务流程；如果非法 Playbot 输出在 replace 前不校验，会删除旧用例后保存失败；如果旧 Blueprint 缺字段被当成 project_saved，会让历史用例在未保存登录态时突然不可执行。

验证：
`TestGenerateTestCasesCarriesAuthContextWithoutSendingAuthSecretsToPlaybot`、`TestGenerateTestCasesRejectsInvalidBlueprintAuthContext`、`TestGenerateTestCasesRejectsInvalidRecordingMetaAuthContextBeforePlaybot` 和 `TestRunTestCaseCleanOrLegacyAuthContextDoesNotRestoreAuthState` 覆盖生成继承、非法输出拒绝、生成前校验、clean 与 legacy 执行不恢复登录态。

### project_saved 执行恢复和报告

契约：
显式 `project_saved` 执行必须在 runner 之前找到当前版本 active 登录态，并在首次导航前恢复；缺少登录态时返回执行前错误，不调用 runner，不创建 TestExecution。执行报告只记录 `auth_context`、来源、登录态摘要和导航摘要，不记录敏感明文。

依据：
来自 P4.5 对自动化执行登录状态复用、缺登录态前置失败和敏感字段不出报告的要求。恢复顺序必须早于默认导航或首步 navigate，否则业务用例会在未登录状态下打开目标页。

当前/历史问题：
如果缺少登录态时静默降级 clean，会掩盖用户配置错误；如果先导航再恢复，会在目标页打开时仍处于未登录状态；如果执行报告带明文值，会把登录凭据写入历史记录。

验证：
`TestRunTestCaseProjectSavedRequiresAuthStateBeforeRunner` 和 `TestRunTestCaseProjectSavedRestoresAuthStateBeforeNavigation` 覆盖前置失败、runner 不调用、TestExecution 不创建、恢复顺序、默认导航和首步 navigate 的优先关系。

### 前端列表入口和短录制页

契约：
页面管理页使用列表或表格展示页面，不再用大卡片承载主要工作流；列表顶部展示当前版本登录态摘要，页面行内提供登录流程、项目登录态业务流程、干净业务流程、智能生成、新建用例、查看和删除入口。没有项目登录态时，`business_flow + project_saved` 必须禁用并引导用户更新登录态或选择干净业务录制。项目录制页必须从通用浏览器页收敛，只展示项目录制需要的操作；登录流程保存时提供“保存主流程并更新项目登录态”“只保存主流程”“只更新登录态”。

依据：
来自 P4.5 的核心体验目标：压缩录制路径，并把登录态保存纳入录制闭环。前端不能只把字段传通，还要减少用户在通用浏览器、页面列表和录制保存之间来回切换。

当前/历史问题：
如果页面仍用大卡片，页面数量变多后很难扫描；如果项目录制页仍展示通用脚本库、配置和独立 Cookie 管理，流程压缩没有落地；如果登录流程结束后不能同步保存登录态，用户仍要回列表手动补一次。

验证：
`frontend/src/p45_recording_ui_contract.test.ts` 覆盖前端契约 helper、页面列表视图、录制入口、无登录态引导和保存录制元数据；代码审核确认 `frontend/src/pages/TestPageManager.tsx` 已接入列表式页面管理和登录态入口，`frontend/src/pages/BrowserManager.tsx` 已接入项目录制短流程和登录流程保存登录态选项。

## P4.6：PostgreSQL 统一存储迁移

### 统一 Store 和防遗漏清单

契约：
P4.6 必须把 BoltDB 和 SQLite/GORM 双存储统一为 PostgreSQL/GORM 单一业务数据库。生产模块只能依赖 `storage.Store`、细分领域接口或受控 TestingPlatform GORM 入口，不得继续依赖 `*storage.BoltDB`、全局 `storage.DB`、BoltDB 初始化或 SQLite driver。每个 Store 方法都必须登记到 Store Operation Inventory，并绑定真实存在的 `TestP46*` 契约测试名。

依据：
来自 `docs/P4_6_POSTGRES_STORAGE_MIGRATION_DESIGN.md` 对单一存储事实源、操作清单完整性、编译断言和静态守门的要求。P4.6 的核心风险是迁移遗漏旧入口，而不是单纯能连接 PostgreSQL。

当前/历史问题：
当前生产代码仍同时存在 BoltDB、SQLite/GORM 全局入口和多个直接 `storage.DB` 调用点。如果没有 Inventory、静态扫描和真实测试名绑定，开发者可能只替换启动链路，遗漏 SDK、API、Agent、Scheduler、Browser、MCP 或 TestingPlatform 的旧存储路径。

验证：
`TestP46StoreOperationInventoryMatchesStoreInterface`、`TestP46StoreOperationInventoryHasContractTestNames`、`TestP46LegacyBoltMethodsAreCoveredByStoreInventory`、`TestP46ProductionCodeDoesNotUseBoltDBOrSQLite`、`TestP46MainInitializesOnlyPostgresStore` 和 `TestP46SDKDoesNotOpenBoltDB` 覆盖该契约。当前红测在旧实现上按预期打红。

### PostgreSQL 配置和密钥安全

契约：
P4.6 的业务数据库名称固定为 `PlayBot`，配置必须使用 `[database] type = "postgres"` 和 PostgreSQL DSN；真实 DSN 不提交仓库。LLM API Key 不得以明文保存到 PostgreSQL 原始字段，必须使用独立的 `[security] llm_api_key_encryption_key` 或 `BROWSERWING_LLM_API_KEY_ENCRYPTION_KEY` 加密密钥，环境变量优先，格式为 base64 编码 32 字节随机密钥。运行时 Store 可以返回解密后的可用 API Key，但普通 API 响应、日志、Playbot job JSON、错误摘要和数据库原始字段不得出现明文。

依据：
来自核心需求中“不在数据库或日志中明文保存 LLM API Key”的安全要求，以及 P4.6 设计对 `PlayBot` 数据库名、配置入口和 Playbot 密钥传递边界的确认。P4.6 不改变当前受控 CLI 参数传递 `--llm-api-key` 的方式，但必须保持日志和错误摘要脱敏。

当前/历史问题：
如果 PostgreSQL 表直接使用 `api_key` 明文字段，会把密钥落库并违反核心安全边界；如果加密密钥入口不固定，测试者和开发者会各自定义来源；如果把“Playbot 输入”泛化理解为禁止 CLI 参数，会和 P1/P4 既有 Playbot 调用契约冲突。

验证：
`TestP46ExampleConfigUsesPostgresPlayBotDSN`、`TestP46ConfigModelDefinesPostgresAndLLMEncryptionFields`、`TestP46StartupRejectsDSNWithoutPlayBotDatabase`、`TestP46StartupRejectsInvalidLLMAPIKeyEncryptionKey`、`TestP46LLMAPIKeyEncryptionKeySourcePrecedence`、`TestP46StartupDoesNotReuseAuthAppKeyForLLMEncryption`、`TestP46LLMConfigPersistenceModelDoesNotExposePlainAPIKeyColumn`、`TestP46PostgresStoreLLMDefaultUniquenessAndEncryptedAtRest` 和 `TestP46PlaybotJobJSONDoesNotContainPostgresLLMAPIKey` 覆盖该契约。

### Store 行为和 P1-P4.5 回归边界

契约：
PostgreSQL Store 必须保持 P1-P4.5 已确认业务语义，不迁移旧数据、不新增 P5 多用户权限、不改变 API 响应语义、前端流程、Playbot job JSON 契约和 P4.5 登录态业务规则。Script 的 `description`、`mcp_command_description` 必须作为结构化列保存；复杂载荷如 actions、cookies、tool parameters、MCP schema、执行结果使用 JSONB roundtrip；默认 LLM、浏览器配置和浏览器实例必须保持唯一语义；TestingPlatform 的 Project、Version、Page、PageScript、TestCase、LLMRefinement、TestExecution、ProjectAuthState 必须通过 PostgreSQL Store 路径保持原有层级、事务和级联删除行为。

依据：
来自 P1-P4.5 已沉淀契约和 P4.6 设计对“只统一存储，不改变业务逻辑”的边界。P4.6 是存储基础设施阶段，不能把实现便利写成新的业务规则。

当前/历史问题：
如果只覆盖 BoltDB 原方法，可能漏掉原 SQLite/GORM TestingPlatform 主体；如果脚本描述字段未结构化迁移，会影响脚本详情和 MCP 命令展示；如果默认唯一、分页排序、级联删除和事务边界改变，会造成用户资产和执行历史回归。

验证：
`TestP46ScriptStructuredFieldsHavePostgresColumnTags`、`TestP46PostgresStoreScriptRoundTrip`、`TestP46PostgresStoreToolConfigByScriptDeletion`、`TestP46PostgresStoreSchedulerPaginationAndFilters`、`TestP46PostgresStoreBrowserDefaultsAndCookieRoundTrip`、`TestP46PostgresStorePromptSystemUpgrade`、`TestP46PostgresStoreAgentCascadeAndMCPServiceToolsRoundTrip`、`TestP46PostgresStoreAuthLookupAndUniqueness`、`TestP46PostgresStoreExecutionAndRecordingDomains` 和 `TestP46PostgresStoreTestingPlatformBusinessDataAccess` 覆盖该契约。

## P4.7：LLM 统一配置和录制数据管理

### 系统管理员和 LLM 配置事实源

契约：
P4.7 新增最小系统管理员标识 `users.is_admin`，只用于 LLM 配置管理，不表示项目 owner、editor、viewer 或租户权限。默认 seed 用户必须是管理员；普通新用户默认不是管理员。创建、更新、删除、测试、启用、停用和设置默认 LLM 配置必须要求管理员权限。普通用户只能读取启用配置摘要，不能看到停用配置或 API Key；管理员读取配置列表和详情时也不能通过普通 API 响应看到 API Key 明文。

依据：
来自 P4.7 设计对“全局管理员维护 LLM 配置、普通用户只能使用启用模型”的约束，以及 P5 前不得提前引入完整项目权限模型的边界。LLM 配置是系统级能力，但密钥仍是敏感资产。

当前/历史问题：
如果没有最小管理员标识，测试者和开发者会无法判断 LLM 配置管理入口由谁维护；如果普通用户能看到停用配置或 API Key，会破坏 P4.7 的使用边界和 P4.6 的密钥安全契约。

验证：
`TestP47UserModelDefinesSystemAdminFlag`、`TestP47LLMConfigAdminPermissionsAndRedaction` 和 `TestP47PostgresSeedDefaultUserCreatesAndPreservesAdminAccess` 覆盖管理员字段、默认 seed、非管理员拒绝写操作、普通用户只见启用摘要、响应不泄露 API Key。

### 统一 LLM 运行时解析

契约：
Playbot 生成 TestCase、自然语言 refine、录制页 AI 自动提取/表单填充和 AI Explorer 必须共享同一套 LLM 配置解析与可用性校验。默认配置缺失或未启用、显式配置不存在或停用、配置缺少 API Key、model 或 base URL 时，必须前置失败，不调用 Playbot、AI Explorer 或录制页 LLM 执行器，不创建 TestCase 或 LLMRefinement。错误响应必须包含可区分的错误 code，并保持密钥脱敏。

依据：
来自 P1/P4 已确认的“不能引入第二套 LLM 配置来源”，以及 P4.7 设计中 Playbot、refine、录制页 AI 和 AI Explorer 共用 LLM 配置策略的要求。P4.7 不合并离线 Playbot 引擎和 live browser AI 引擎，只统一配置事实源和安全边界。

当前/历史问题：
如果各入口各自读取默认 LLM，会导致生成、refine、录制页 AI 和 Explorer 对启用状态、默认配置和缺字段的判断不一致；如果缺 LLM 时进入半执行状态，会留下空资产或错误历史。

验证：
`TestGenerateTestCasesUsesUnifiedLLMRuntimeConfigErrors`、`TestRefineTestCaseReusesExistingLLMConfigSelection`、`TestP47ExplorerRequiresUsableLLMConfigBeforeStarting` 和 `TestP47RecorderAIUsesSelectedEnabledLLMConfigAndRedactsSecrets` 覆盖统一解析、错误 code、下游不调用和密钥脱敏。

### RecordingSession 生命周期和 PageScript 生成

契约：
项目页面录制开始时必须创建 `RecordingSession(status = recording)`，写入 project/version/page、recording_kind、auth_context、target_url 和可选 created_by。录制中必须按 session 持久化完整 actions 数组、action_count 和 last_synced_at；浏览器 `sessionStorage` 只能作为页面内临时缓存，不是唯一事实源。停止录制后会话更新为 `stopped`；只有 `stopped` 会话可以保存为当前页面主流程 `PageScript`，保存时在事务中替换旧主流程并把会话更新为 `saved`。`saved/cancelled/failed` 后不得继续同步 actions 或再次保存。

依据：
来自 P4.7 设计对录制数据数据库事实源的要求。P1-P4.6 的生成、管理、执行和 refine 仍以当前页面保存的 `PageScript` 为主流程事实源；P4.7 只是把录制过程变成可恢复、可审计、可保护的会话。

当前/历史问题：
如果录制动作只存在 `sessionStorage`，刷新或重连会丢失草稿；如果 active 会话可以直接保存，会把未停止的录制状态固化为主流程；如果 stopped/saved/cancelled 边界不清，会污染旧 PageScript 或产生重复主流程。

验证：
`TestP47RecordingSessionAndArtifactProductionModels`、`TestP47RecordingSessionStartValidatesScopeAndAuthContext`、`TestP47RecordingSessionSummaryAndStopUseProductionRoutes`、`TestP47RecordingSessionSyncPersistsActionsAndRejectsTerminalStates`、`TestP47SaveRecordingSessionReplacesPageScriptWithRecordingMeta`、`TestP47SaveRecordingSessionRequiresStoppedSession` 和 `TestP47RecorderSyncLoopPersistsRecordingSessionDraft` 覆盖模型、作用域、开始、同步、停止、保存和草稿持久化。

### 取消、失败和录制产物保护

契约：
项目录制取消必须调用后端 cancel 入口，把 `RecordingSession.status` 更新为 `cancelled`；如果取消发生在仍在录制的会话上，必须先停止绑定的浏览器录制生命周期。取消、失败或非法 `recording_meta` 不得删除或替换旧 `PageScript`。取消后的会话必须拒绝继续 sync 或 save。录屏、截图和下载文件等二进制不写入 PostgreSQL；数据库只保存 `RecordingArtifact` 元数据，下载必须经过受控 artifact id 和 project/version/page scope 校验，不返回任意本地绝对路径。

依据：
来自 P4.7 设计中“取消失败不污染旧主流程”和“裸 `/files/recordings` 不作为新业务依赖”的要求。P4.7 为 P5 权限控制预留清晰的 project/version/page 和 artifact scope。

当前/历史问题：
如果前端取消只清本地状态，数据库会残留 `recording` 或 `stopped` 会话，刷新后状态混乱；如果下载直接暴露本地路径，后续多用户权限无法收口，也有本地文件泄露风险。

验证：
`TestP47CancelRecordingSessionCancelsActiveSessionAndProtectsPageScript`、`TestP47CancelRecordingSessionAllowsStoppedUnsavedSession`、`TestP47CancelledFailedAndInvalidMetaDoNotReplacePageScript`、`TestP47RecordingArtifactMetadataAndScopedDownload`、`TestP47DownloadedFileStorageKeyUsesControlledRelativePath` 和 `frontend/src/p45_recording_ui_contract.test.ts` 覆盖取消状态、取消后拒绝 sync/save、旧 PageScript 不变、产物元数据和前端先调用后端再清本地状态。
