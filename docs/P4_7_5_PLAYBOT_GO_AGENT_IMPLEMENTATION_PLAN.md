# P4.7.5 Playbot Go Agent 实施计划与红测拆分

本文档是 `docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md` 的实施拆分。它不是新的业务契约来源；如果本文档和详细设计冲突，以详细设计为准并退回规划者修订。

当前状态：待评审。评审通过后，用例编写者先按本文档写契约红测；红测通过审核前，业务开发者不得实现生产代码。

## 一、实施原则

- P4.7.5 只做 Playbot Go agent、后端调用适配、上下文和 Blueprint 质量边界，不做 P4.8 体验收口。
- Go 后端是上下文事实源管理者和裁决者。
- 独立 Go agent 是上下文消费、LLM 编排和 Blueprint 编译者。
- Go runner 是唯一正式执行事实源。
- Python `playbot-engine` 不进入正式生成、优化或执行路径。
- `PlaybotJob` JSON 不得包含 LLM API Key、Cookie、Storage value 或其他密钥明文。
- 后端不使用 LLM 裁决是否保存、是否替换旧资产、是否补上下文或是否重录。
- `docs/CONTRACT_RECORDS.md` 只在红测、实现和审核通过后更新。

当前仓库尚无 `playbot-agent` 目录。P4.7.5 红测可以创建该目录和测试文件，并允许以缺符号、缺包或断言失败的方式打红；不得为了让红测通过而加入 test-only 假实现。

## 二、推荐目录和边界

推荐新增独立模块：

```text
playbot-agent/
  go.mod
  cmd/browserwing-playbot-agent/
  internal/protocol/
  internal/quality/
  internal/contextpack/
  internal/compiler/
  internal/llm/
  internal/agent/
  testdata/
```

推荐后端新增调用适配层：

```text
backend/services/playbotagent/
```

协议层要求：

- `PlaybotJob` / `PlaybotResult` 必须是稳定 JSON 协议。
- 如后端需要复用协议类型，只能依赖无运行时副作用的最小协议包或 JSON schema。
- 后端不得链接 agent runtime、LLM client、prompt engine 或编译器实现。
- `go.work` 当前只包含 `./backend`；P4.7.5 实现阶段如新增独立 Go 模块，应把 `./playbot-agent` 纳入工作区或明确使用独立目录验证命令。

## 三、红测总顺序

红测按以下顺序写，便于审核者分批 review：

1. Go agent 协议和密钥边界红测。
2. Go agent 录制质量和 Blueprint compiler 红测。
3. 后端 agent adapter 和上下文事实源红测。
4. 后端严格保存校验和资产保护红测。
5. optimize / repair proposal 边界红测。
6. 正式执行路径边界和回归红测。

每组红测必须写清：

- 依据：引用 P4.7.5 详细设计的具体规则。
- 当前失败形态：缺符号、缺目录、断言失败或现有宽松行为不满足。
- 验证命令：定向命令优先，必要时补标准入口。

## 四、红测拆分

### 4.1 Go agent 协议和密钥边界

建议测试位置：

- `playbot-agent/internal/protocol/protocol_contract_test.go`
- `playbot-agent/cmd/browserwing-playbot-agent/cli_contract_test.go`

建议覆盖：

- `PlaybotJob.schema_version` 必须存在且可校验。
- `PlaybotResult.schema_version`、`status`、`code`、`context_trace` 必须存在且可校验。
- `status` 只允许 `success`、`failed`、`context_required`。
- `llm_runtime_config` 不得序列化 API Key 明文，只允许 provider、endpoint、model、config id、超时、重试和脱敏摘要。
- `PlaybotJob` JSON、临时 job 文件、fixture 和调试产物不得包含 LLM API Key、Cookie、Storage value。
- LLM API Key 必须从受控 secret channel 读取，例如环境变量、只读 secret file descriptor、进程私有 secret provider 或后续 RPC metadata。
- stdout 只能输出 `PlaybotResult` JSON。
- stderr 只能输出脱敏日志。
- 进程级失败和业务失败必须区分：业务失败进入 `PlaybotResult.status/code`，不是随意写 stderr 后退出。

建议测试名：

- `TestP475ProtocolRejectsSecretsInPlaybotJobJSON`
- `TestP475LLMRuntimeConfigSerializesOnlyRedactedSummary`
- `TestP475CLIStdoutContainsOnlyPlaybotResultJSON`
- `TestP475CLIRedactsSecretChannelFromStderr`
- `TestP475ResultStatusEnumAndSchemaVersion`

建议验证：

```powershell
cd playbot-agent
go test ./internal/protocol ./cmd/browserwing-playbot-agent -run TestP475 -count=1
```

当前预期红态：

- `playbot-agent` 尚不存在，初始红测可以因目录、包或目标符号不存在而失败。

### 4.2 Go agent 录制质量和 Blueprint compiler

建议测试位置：

- `playbot-agent/internal/quality/recording_quality_contract_test.go`
- `playbot-agent/internal/compiler/blueprint_compiler_contract_test.go`

建议覆盖：

- 缺少可执行目标返回 `recording_action_missing_target`。
- 缺少输入值返回 `recording_action_missing_value`。
- 导航缺 URL 返回 `recording_navigation_missing_url`。
- DOMSnapshot 不可用返回 `recording_snapshot_unusable`。
- RecordingMeta 非法返回 `recording_meta_invalid`。
- auth_context 冲突返回 `recording_auth_context_conflict`。
- click 编译为 `click + target`。
- fill/input 编译为 `fill + target + value`。
- navigate 编译为 `navigate + url`。
- `target_hint` 编译为最终 `target`。
- `intent_reason` 编译为 `description`。
- `recorded_selector`、role/text、placeholder 必须保留到 `target`。
- unsupported action 拒绝输出。
- 缺目标的交互步骤拒绝输出。
- 缺 value 的输入、选择或文本断言步骤拒绝输出。

建议测试名：

- `TestP475RecordingQualityErrorsAreMachineReadable`
- `TestP475CompilerConvertsRecordedClickFillNavigateToExecutableBlueprint`
- `TestP475CompilerPreservesRecordedSelectorRoleTextAndPlaceholder`
- `TestP475CompilerRejectsUnsupportedAction`
- `TestP475CompilerRejectsTargetHintOnlyInFinalBlueprint`
- `TestP475CompilerRejectsNavigateValueOnlyInFinalBlueprint`

建议验证：

```powershell
cd playbot-agent
go test ./internal/quality ./internal/compiler -run TestP475 -count=1
```

### 4.3 后端 agent adapter 和上下文事实源

建议测试位置：

- `backend/services/playbotagent/client_contract_test.go`
- `backend/api/project_generation_p475_contract_test.go`

建议覆盖：

- 后端生成接口通过独立 Go agent adapter 调用，不调用 Python `playbot-engine`。
- 后端组装 `PlaybotJob` 时，优先使用当前有效 PageScript。
- stopped RecordingSession 只有先保存为 PageScript，或在同一受保护事务/锁定流程完成 `session -> PageScript -> TestCase` 时，才能参与生成。
- RecordingArtifact 元数据不能单独满足生成前置条件。
- `PlaybotJob` 不包含 LLM API Key、Cookie、Storage value 或本地绝对路径。
- LLM API Key 通过受控 secret channel 传给 agent，不写入 job JSON。
- agent 返回 `context_required + retryable` 时，后端只按确定性规则补上下文并有限重跑。
- agent 返回录制质量硬错误时，后端不调用 LLM 猜测、不创建 TestCase、`replace` 不删除旧 TestCase。
- `context_trace` 必须被后端记录到可审计位置，且不得泄露密钥或本地绝对路径。

建议测试名：

- `TestP475GenerateUsesGoPlaybotAgentAdapter`
- `TestP475PlaybotJobDoesNotContainLLMSecretOrAuthStorage`
- `TestP475RecordingArtifactCannotSatisfyGenerationSource`
- `TestP475StoppedSessionMustBecomePageScriptBeforeGeneratingTestCase`
- `TestP475ContextRequiredRetriesOnlyWithBackendApprovedContext`
- `TestP475RecordingQualityErrorProtectsExistingAssets`

建议验证：

```powershell
cd backend
go test ./services/playbotagent -run TestP475 -count=1
go test ./api -run TestP475 -count=1
```

当前预期红态：

- `backend/services/playbotagent` 尚不存在。
- 现有生成接口仍走 Python playbot service。

### 4.4 后端严格保存校验和资产保护

建议测试位置：

- `backend/api/project_generation_p475_contract_test.go`
- 可按需要新增执行归一化专用测试文件，例如 `backend/api/project_execution_normalization_p475_contract_test.go`。

建议覆盖：

- agent success 输出后，后端仍执行严格最终字段校验。
- `navigate` 只给 `value` 不给 `url` 时不得保存。
- 交互步骤只有 `target_hint`、没有最终 `target` 时不得保存。
- unsupported action 不得保存。
- active TestCase 无 steps 不得保存。
- `replace` 模式下任何严格保存校验或执行归一化失败都不得删除旧 TestCase。
- `preview` 模式失败只返回错误，不落库。
- `append` 模式失败不追加新 TestCase。
- 错误响应不得泄露 API Key、Cookie、Storage value 或本地绝对路径。
- 当前 `RunTestCase` 对历史 Blueprint 的宽松兼容不等于 Playbot 新生成保存标准。

建议测试名：

- `TestP475GeneratedBlueprintMustPassStrictFinalFieldValidation`
- `TestP475GenerateRejectsNavigateValueOnlyBeforeSaving`
- `TestP475GenerateRejectsTargetHintOnlyBeforeSaving`
- `TestP475GenerateRejectsUnsupportedActionBeforeSaving`
- `TestP475ReplaceKeepsOldTestCaseWhenGeneratedBlueprintInvalid`
- `TestP475PreviewAndAppendProtectAssetsOnValidationFailure`

建议验证：

```powershell
cd backend
go test ./api -run TestP475Generate -count=1
```

### 4.5 optimize 和 repair proposal 边界

建议测试位置：

- `backend/api/project_testcase_refinement_p475_contract_test.go`
- `playbot-agent/internal/agent/optimize_contract_test.go`
- `playbot-agent/internal/agent/repair_proposal_contract_test.go`

建议覆盖：

- optimize 调用独立 Go agent，不调用 Python `playbot-engine`。
- optimize 只创建 proposed LLMRefinement，不直接修改 active TestCase。
- proposed Blueprint 必须满足可执行输出标准。
- proposed 校验失败时不污染旧 TestCase。
- repair_proposal 只返回修复建议草案，不自动修改 PageScript、RecordingSession、TestCase 或 RecordingArtifact。
- repair_proposal 不能在没有录制事实支撑时编造可执行步骤。

建议测试名：

- `TestP475OptimizeUsesGoAgentAndCreatesOnlyProposedRefinement`
- `TestP475OptimizeRejectsInvalidProposedBlueprintWithoutChangingActiveCase`
- `TestP475RepairProposalDoesNotApplyAssetChanges`
- `TestP475RepairProposalCannotInventStepsWithoutRecordedFacts`

建议验证：

```powershell
cd backend
go test ./api -run TestP475Optimize -count=1

cd ..\playbot-agent
go test ./internal/agent -run TestP475 -count=1
```

### 4.6 正式执行路径边界和回归

建议测试位置：

- `backend/api/project_testcase_execution_p475_contract_test.go`
- `backend/services/testcase_executor/p475_runner_contract_test.go`

建议覆盖：

- `RunTestCase` 正式路径继续使用 Go `testcase_executor.Runner`。
- 独立 Go agent 不参与正式 `RunTestCase` 路径。
- Python execution engine 不参与正式 `RunTestCase` 路径。
- P4.7.5 不新增原生 Playwright spec runner。
- 有头执行以 Go BrowserManager/BrowserInstance 为准。
- 当前 `RunTestCase.headless` 字段未实际成为按次切换有头/无头事实源；P4.7.5 不依赖它。

建议测试名：

- `TestP475RunTestCaseUsesGoRunnerOnly`
- `TestP475RunTestCaseDoesNotInvokePlaybotAgent`
- `TestP475RunTestCaseDoesNotInvokePythonExecutionEngine`
- `TestP475NoPlaywrightSpecRunnerIsIntroduced`

建议验证：

```powershell
cd backend
go test ./api -run TestP475RunTestCase -count=1
go test ./services/testcase_executor -run TestP475 -count=1
```

## 五、红测 review 通过后的实现顺序

业务开发者按以下顺序实现，避免先接 LLM 后补安全和保存边界：

1. 新增 `playbot-agent` 独立 Go module、CLI 入口和协议类型。
2. 实现 `PlaybotJob` / `PlaybotResult` JSON schema、状态码和脱敏日志框架。
3. 实现 secret channel，确保 API Key 不进入 job JSON、fixture、临时 job 文件和普通日志。
4. 实现录制质量校验和机器可判定错误。
5. 实现 Blueprint compiler 和 agent 侧 executable validator。
6. 实现 LLM client 接口和可测试 fake，先让 compiler/协议红测不依赖真实 LLM。
7. 实现 agent generate / optimize / repair_proposal 编排。
8. 新增后端 `playbotagent` adapter，支持独立二进制调用和 fake client 注入。
9. 后端生成接口改为组装 `PlaybotJob` 并调用 Go agent adapter。
10. 后端 refine 接口改为 optimize 模式调用 Go agent adapter。
11. 后端保存前新增严格最终字段校验，再复用现有执行归一化。
12. 后端处理 `context_required`、录制质量错误、agent validation failed 和进程级失败。
13. 确认 `RunTestCase` 仍只走 Go runner，不接入 agent。
14. 移除正式路径对 Python `playbot-engine` 的生成/优化依赖；历史配置可保留为兼容或清理项，但不得被正式入口调用。
15. 更新阶段验证说明；最终审核通过后再写 `docs/CONTRACT_RECORDS.md`。

## 六、验证矩阵

红测阶段建议命令：

```powershell
cd backend
go test ./api -run TestP475 -count=1
go test ./services/playbotagent -run TestP475 -count=1
go test ./services/testcase_executor -run TestP475 -count=1

cd ..\playbot-agent
go test ./... -run TestP475 -count=1
```

实现收口建议命令：

```powershell
cd backend
go test ./api -run TestGenerateTestCases -count=1
go test ./api -run TestRunTestCase -count=1
go test ./api -run TestRefineTestCase -count=1
go test ./...

cd ..\playbot-agent
go test ./...
go build ./...

cd ..\frontend
pnpm run type-check
pnpm run build
```

当前通用发布验证仍保留 `playbot-engine` 入口；只有 P4.7.5 实现并审核通过后，发布验证才切换到 `playbot-agent`。

## 七、评审检查点

评审本文档时重点看：

- 是否仍把 Python worker 或 Python execution engine 放进正式路径。
- 是否把 agent 做成第二个后端，直接查库或缓存业务事实。
- 是否让后端用 LLM 做保存、重录、补上下文或替换资产的裁决。
- 是否让 `PlaybotJob` JSON、fixture、临时文件或日志携带密钥明文。
- 是否把 RecordingArtifact 元数据当成生成事实源。
- 是否把现有宽松执行归一化等同于 Playbot 新生成 Blueprint 的保存标准。
- 是否把 stopped RecordingSession 直接拿来生成 TestCase，却没有先保存 PageScript 或受保护事务边界。
- 是否提前把 P4.8 体验、RPC 常驻服务或自动修复写回混入 P4.7.5。

## 八、交付定义

P4.7.5 实施计划通过评审后，才进入用例编写者红测阶段。

P4.7.5 实现通过最终审核时必须满足：

- 详细设计、实施计划、红测和实现一致。
- 红测先打红、审核通过，再由生产实现转绿。
- Go agent 协议、compiler、质量错误、secret channel、backend adapter、严格保存校验和执行边界均有契约测试覆盖。
- `docs/DEVELOPMENT_PLAN.md` 更新阶段状态。
- `docs/CONTRACT_RECORDS.md` 沉淀通过审核的新契约。
