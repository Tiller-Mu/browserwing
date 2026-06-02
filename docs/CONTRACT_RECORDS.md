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
