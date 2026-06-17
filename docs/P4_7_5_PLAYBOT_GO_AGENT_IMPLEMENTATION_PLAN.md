# P4.7.5 Playbot Go Agent 实施计划与红测拆分

本文档是 `docs/P4_7_5_PLAYBOT_BLUEPRINT_QUALITY_DESIGN.md` 的实施拆分。它不是新的业务契约来源；如果本文档和详细设计冲突，以详细设计为准并退回规划者修订。

当前状态：实施计划已通过 review，契约红测已落地并通过审核，生产实现已按红测落地并通过定向验证，等待最终审核和阶段收口。

## 一、实施原则

- P4.7.5 只做 Playbot Go agent、后端调用适配、上下文和 Blueprint 质量边界，不做 P4.8 体验收口。
- Go 后端是上下文事实源管理者和裁决者。
- 独立 Go agent 是上下文消费、LLM 编排和 Blueprint 编译者。
- Go runner 是唯一正式执行事实源。
- Python `playbot-engine` 不进入正式生成、优化或执行路径。
- `PlaybotJob` JSON 不得包含 LLM API Key、Cookie、Storage value 或其他密钥明文。
- 后端不使用 LLM 裁决是否保存、是否替换旧资产、是否补上下文或是否重录。
- `docs/CONTRACT_RECORDS.md` 只在红测、实现和审核通过后更新。

当前红测已创建 `playbot-agent` 独立 Go module 骨架、后端 `playbotagent` adapter 测试目录和 P4.7.5 合同测试文件。生产实现落地后，定向 P4.7.5 协议、CLI、adapter、generate/refine 和执行边界测试应转绿；若后续出现失败，应优先定位真实合同偏差、环境 DSN 缺失或缓存权限问题。

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
- `PlaybotEvent` 是可选会话内观察协议，二进制 agent 通过 `--events events.jsonl` 输出 JSONL；stdout 仍只能输出最终 `PlaybotResult`。
- 后端 Playbot run hub 只做内存 TTL 非持久化缓存，stream 使用 `?after_seq=N` 支持断线重连补发，前端用 `fetch + ReadableStream` 读取。
- `visible_summary` / `model_output` 只能作为 run/result/API response 的展示材料，不得写入 `TestCase.Blueprint`。
- 真实 LLM generate 必须返回 `semantic_plan`，最终 Blueprint 只能由锚定到录制事实的 semantic plan 编译产生；`visible_message`、`candidate_steps` 等展示字段不能作为执行事实源。
- 如后端需要复用协议类型，只能依赖无运行时副作用的最小协议包或 JSON schema。
- 后端不得链接 agent runtime、LLM client、prompt engine 或编译器实现。
- `go.work` 已纳入 `./backend` 和 `./playbot-agent`，便于红测和后续实现阶段统一验证。

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
- `backend_approved_context` 是 `context_required` 后端批准重跑的正式输入字段；首次 job 必须省略或为空，二次 job 中每项至少包含 `kind`、`scope`、`source` 和 `payload`。
- `llm_runtime_config` 不得序列化 API Key 明文，只允许 provider、endpoint、model、config id、超时、重试和脱敏摘要。
- `PlaybotJob` JSON、临时 job 文件、fixture 和调试产物不得包含 LLM API Key、Cookie、Storage value。
- LLM API Key 必须从受控 secret channel 读取，例如环境变量、只读 secret file descriptor、进程私有 secret provider 或后续 RPC metadata。
- 录制流程中的 token-like 字符串是业务测试数据，允许出现在 action input、DOM text、page description 和 user instruction 中；测试 token 与生产 token 不一致是使用约束，不由 P4.7.5 后端按 `sk-` 前缀强校验。
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

- `playbot-agent` module 和测试文件已存在，当前应因 CLI main package 或协议、quality、compiler、agent 生产符号缺失而失败。

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
- 后端补上下文重跑时，二次 `PlaybotJob` 必须通过 `backend_approved_context` 携带已批准片段；该字段只允许由后端写入，不能包含 API Key、Cookie、Storage value 或本地绝对路径。
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

- `backend/services/playbotagent` 测试文件已存在，当前应因 adapter 生产符号缺失而失败。
- 现有生成和 refine 接口仍走 Python playbot service，后端 API 红测应先红在缺 Go agent seam 或现有路径不满足 P4.7.5 合同。

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
3. 实现 `backend_approved_context` 协议字段，确保它只在后端批准的 `context_required` 重跑 job 中出现。
4. 实现 secret channel，确保 API Key 不进入 job JSON、fixture、临时 job 文件和普通日志。
5. 实现录制质量校验和机器可判定错误。
6. 实现 Blueprint compiler 和 agent 侧 executable validator。
7. 实现 LLM client 接口和可测试 fake，先让 compiler/协议红测不依赖真实 LLM。
8. 实现 agent generate / optimize / repair_proposal 编排。
9. 新增后端 `playbotagent` adapter，支持独立二进制调用和 fake client 注入。
10. 后端生成接口改为组装 `PlaybotJob` 并调用 Go agent adapter。
11. 后端 refine 接口改为 optimize 模式调用 Go agent adapter。
12. 后端保存前新增严格最终字段校验，再复用现有执行归一化。
13. 后端处理 `context_required`、录制质量错误、agent validation failed 和进程级失败。
14. 确认 `RunTestCase` 仍只走 Go runner，不接入 agent。
15. 移除正式路径对 Python `playbot-engine` 的生成/优化依赖；历史配置可保留为兼容或清理项，但不得被正式入口调用。
16. 更新阶段验证说明；最终审核通过后再写 `docs/CONTRACT_RECORDS.md`。

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
- 是否让 `backend_approved_context` 变成长期缓存、自由上下文通道或密钥泄露通道。
- 是否让 `PlaybotJob` JSON、fixture、临时文件或日志携带密钥明文。
- 是否把 RecordingArtifact 元数据当成生成事实源。
- 是否把现有宽松执行归一化等同于 Playbot 新生成 Blueprint 的保存标准。
- 是否把 stopped RecordingSession 直接拿来生成 TestCase，却没有先保存 PageScript 或受保护事务边界。
- 是否提前把 P4.8 体验、RPC 常驻服务或自动修复写回混入 P4.7.5。

## 八、交付定义

P4.7.5 红测已完成并通过 review，生产实现已落地；下一步是最终审核、阶段验证和契约记录收口。

P4.7.5 实现通过最终审核时必须满足：

- 详细设计、实施计划、红测和实现一致。
- 红测先打红、审核通过，再由生产实现转绿。
- Go agent 协议、compiler、质量错误、secret channel、backend adapter、严格保存校验和执行边界均有契约测试覆盖。
- `docs/DEVELOPMENT_PLAN.md` 更新阶段状态。
- `docs/CONTRACT_RECORDS.md` 沉淀通过审核的新契约。
