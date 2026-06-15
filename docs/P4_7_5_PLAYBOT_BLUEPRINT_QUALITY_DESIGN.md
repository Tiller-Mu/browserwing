# P4.7.5 Playbot Go Agent 与 Blueprint 质量边界重构设计

本文档定义 P4.7.5 阶段的业务契约、组件边界、红测口径和验收标准。P4.7.5 插在 P4.7 LLM 统一配置和录制数据管理之后、P4.8 录制到智能生成用例端到端收口之前，目标是先把 Playbot 的生成能力、上下文边界和输出标准收束清楚，避免 P4.8 把体验闭环建立在不可执行、不可校验或事实源混乱的用例生成能力之上。

P4.7.5 一步到位改为独立 Go Playbot Agent。Go 后端不把 agent 封装进自身进程内，优先通过独立二进制调用，后续可演进为 RPC 常驻服务。Python `playbot-engine` 不进入正式生成、优化或执行路径，只能作为历史实验工具或迁移参考。

## 阶段协作要求

P4.7.5 必须继续遵守项目契约流程，不能由开发者直接按本文档实现生产代码。

执行顺序：

1. 规划者维护并定稿本文档，明确 Playbot 输入事实源、独立 Go agent 边界、输出标准、错误口径和验收标准。
2. 业务开发者先 review 本文档，只反馈现有 Go runner、生成接口、refine 接口、录制数据结构、独立二进制调用和后续 RPC 演进的实现约束。
3. 用例编写者在设计确认后先写契约红测，覆盖录制质量错误、Go agent 协议、Blueprint 可执行校验、保存保护、Go compiler fixture 和能力边界。
4. 代码审核者先审核红测是否符合本文档，确认没有把固定测试页面、固定 DOM、固定 selector、LLM 偶然输出、Python worker 或 agent 隐藏缓存写成业务契约。
5. 业务开发者只能在红测审核通过后实现生产代码；实现中如发现本文档不可行，应退回规划者修订，而不是在代码里改写契约。
6. 代码审核者最终复核实现和测试，并按影响面运行后端、Playbot agent 和必要人工浏览器冒烟验证。
7. 规划者阶段收尾，更新 `docs/DEVELOPMENT_PLAN.md`，并在 P4.7.5 红测、实现和审核通过后再更新 `docs/CONTRACT_RECORDS.md`。

红测优先级：

- 第一优先级是证明 Playbot 最终保存的 active TestCase 一定能被 Go 后端执行归一化逻辑消费。
- 第二优先级是证明录制结果质量不足时会前置失败，不让 LLM 猜测补齐，也不污染旧 TestCase。
- 第三优先级是证明后端、独立 Go agent、Go runner 的边界清晰，Python `playbot-engine` 不进入正式生成、优化或执行路径。

## 一、核实结论

当前 Go 后端 runner 可以执行“有头浏览器中的 BrowserWing Blueprint 用例”，但它不是 Playwright `.spec.ts` 或 `.py` 文件执行器。

已确认的实现事实：

- `RunTestCase` 使用 `testcase_executor.Runner`，执行的是 TestCase Blueprint steps，不是 Playwright 源码文件。
- runner 背后注入的是 Go `executor.NewExecutor(browserMgr)`。
- 浏览器执行器基于 `go-rod/rod` 控制浏览器。
- BrowserManager 本地启动浏览器时使用 `launcher.Headless(headless)`；Windows、macOS 或有 GUI 的 Linux 环境默认可以有头运行。
- 当前 `RunTestCase` 请求体虽然有 `headless` 字段，但该字段未实际传入执行准备链路；有头或无头主要由 BrowserInstance 配置决定。
- 当前 Python `playbot-engine` 主要承担 LLM 编排、semantic IR 和结构化输出，另有一套 Playwright execution engine；P4.7.5 后这些能力都不作为正式产品路径。

因此，P4.7.5 的执行边界成立：Go 后端继续负责正式执行，独立 Go Playbot Agent 只负责生成、优化和修复建议草案。本文档中的“Playwright 用例”如无特别说明，均应理解为“可由 BrowserWing Go runner 在浏览器中执行的自动化 Blueprint”，不是原生 Playwright test 文件。

## 二、当前问题

P4.7 之后，录制数据、LLM 配置和 RecordingSession 已经具备底座，但 Playbot 的用例能力仍有几个风险：

- 生成用例、优化用例、执行用例和自修复倾向混在一个智能体语义里，难以分别调优。
- Python worker 作为外部进程接收密钥、读取 JSON、输出 stdout，增加部署、调试和协议漂移成本。
- Python 侧可以输出接近语义计划的 JSON，但最终是否符合 Go runner 的可执行 Blueprint 标准缺少强制编译和校验。
- Go 后端保存 TestCase 前，对 Playbot 输出的 active Blueprint 可执行性校验不足，可能把不能归一化的步骤保存为正式资产。
- LLM 可能在录制数据缺失时凭经验补齐 selector、URL、输入值或登录态，导致生成结果看似完整但实际不可复现。
- `target_hint`、`intent_reason`、`navigate.value` 等中间字段和最终执行字段边界不清，容易把内部语义 IR 当作外部标准保存。
- Python execution engine 如果继续留在正式执行语义里，会和 Go 后端 runner 形成第二套执行事实源。
- 如果 agent 长期缓存对话上下文或自行查库，会形成第二套上下文事实源，红测、审计和复现都会变差。

P4.7.5 要先把这些边界压实，再让 P4.8 做录制到生成用例的端到端体验。

## 三、阶段目标

P4.7.5 完成后应满足：

- 页面录制结果是 Playbot 生成用例的输入事实源。
- Go 后端执行 Blueprint 是唯一对外执行标准。
- Playbot agent 改为独立 Go agent，优先提供独立二进制调用能力。
- Python `playbot-engine` 不进入正式生成、优化或执行路径。
- 后端负责上下文事实源管理、权限、事务、脱敏、审计和保存裁决。
- 独立 Go agent 负责单次任务内的上下文打包、LLM 调用、semantic IR、Blueprint compiler、quality diagnostics 和 repair proposal 草案。
- 后端保存 TestCase 前必须确认 Blueprint 同时满足严格最终字段标准和执行归一化消费要求。
- Playbot 能力拆为生成用例、优化用例、执行用例、自修复建议四类。
- 执行用例继续由 Go 后端 runner 负责；独立 Go agent 和 Python execution engine 都不进入正式产品执行路径。
- 自修复暂不自动修改资产，只预留“失败报告 -> 修复建议草案”的边界。
- 录制质量错误必须能区分“录制内容不足”和“Playbot 输出非法”，便于后续回到录制能力优化。

## 四、不在 P4.7.5 范围内

P4.7.5 不实现：

- 原生 Playwright `.spec.ts`、`.spec.py` 或 pytest 文件执行。
- 按单次 `RunTestCase.headless` 请求字段切换有头或无头执行。该能力如需实现，应单独补契约。
- 自动修复并写回 PageScript、TestCase 或录制资产。
- 批量生成、批量优化或批量修复。
- P4.8 的页面入口、弹窗、列表刷新和端到端用户路径体验。
- P5 多用户项目权限和租户隔离。
- 将 Playbot 离线生成 agent 和录制页 live browser AI 引擎合并。
- 让独立 agent 直接访问数据库、绕过后端权限、保存业务资产或缓存业务事实。
- RPC 常驻服务；该形态是独立二进制协议稳定后的演进方向。

## 五、组件边界

P4.7.5 后，系统按三个核心组件拆分。

### 5.1 Go 后端

后端负责事实源和裁决：

- 决定当前有效输入是谁：PageScript，或受保护流程内的 stopped RecordingSession。
- 读取数据库资产：ActionTrace、DOMSnapshot、RecordingMeta、TestCase、ExecutionReport。
- 做权限、状态、事务、锁定、脱敏和审计。
- 选择可用 LLM 配置，并以受控方式传给独立 agent。
- 组装完整 `PlaybotJob`。
- 调用独立 Go agent。
- 消费 agent 的结构化结果。
- 决定保存、拒绝、补上下文重跑、提示重录或进入 repair proposal。
- 最终保存前做严格 Blueprint 字段校验和执行归一化。
- 正式执行仍由 Go runner 负责。

后端不得：

- 使用 LLM 决定是否保存、是否替换旧资产、是否跳过 auth_context 冲突或是否把 Artifact 当作事实源。
- 把独立 agent 的输出未经严格保存校验就写入 active TestCase。
- 把 Python `playbot-engine` 作为正式生成、优化或执行入口。

### 5.2 独立 Go Playbot Agent

agent 负责生成和模型编排：

- 接收后端传入的 `PlaybotJob`。
- 校验录制质量。
- 在单次任务内裁剪、压缩 ActionTrace 和 DOMSnapshot。
- 组织 prompt。
- 调用 LLM。
- 生成 semantic plan。
- 编译为 Go runner 可执行 Blueprint。
- 做 agent 侧 executable validator。
- 返回 Blueprint、质量错误、上下文请求或 repair proposal 草案。

agent 不得：

- 直接查数据库。
- 自行决定业务事实源。
- 长期缓存大模型对话上下文并影响后续任务。
- 把历史对话当作生成事实源。
- 保存或修改 PageScript、RecordingSession、TestCase、LLMRefinement 或 ExecutionReport。
- 参与正式 `RunTestCase` 执行路径。

### 5.3 Go runner

Go runner 负责正式执行：

- BrowserWing Blueprint 由 Go 后端 runner 执行。
- Go runner 使用 BrowserManager 和 BrowserInstance 管理浏览器。
- 有头执行能力来自 BrowserInstance 的浏览器启动配置。
- P4.7.5 不新增 Playwright spec runner。
- P4.7.5 不把独立 agent 或 Python execution engine 接入正式 `RunTestCase` 路径。

## 六、上下文管理

上下文事实源归后端，不归 agent。

后端拥有：

- durable context。
- source selection。
- permissions。
- transactions。
- audit。
- LLM config selection。

agent 拥有：

- prompt context packing。
- token budget。
- LLM call。
- semantic IR。
- Blueprint compiler。
- quality diagnostics。

agent 不长期缓存每次大模型对话上下文，也不把历史对话作为事实源。P4.7.5 采用无状态调用：每次调用由后端传完整 job，agent 只在单次调用内做临时摘要、裁剪和 prompt packing。

如果 agent 发现上下文不足，它不自己查库，也不凭 LLM 猜，而是返回结构化错误或上下文请求：

```json
{
  "status": "context_required",
  "code": "dom_snapshot_chunk_required",
  "retryable": true,
  "requested_context": [
    {
      "kind": "dom_snapshot",
      "scope": "recorded_action_3",
      "reason": "需要动作 3 附近的候选元素"
    }
  ]
}
```

后端按确定性规则决定是否补上下文重跑。即使后续演进为 RPC 常驻服务，agent 也只能请求上下文片段，不能直接查库，不能缓存业务事实作为后续任务的事实源。

## 七、调用形态与协议

P4.7.5 第一阶段采用独立二进制调用，不先上 RPC 常驻服务。

建议调用形态：

```powershell
browserwing-playbot-agent --mode generate --input job.json
browserwing-playbot-agent --mode optimize --input job.json
browserwing-playbot-agent --mode repair-proposal --input job.json
```

也可以支持 stdin/stdout：

```text
stdin:  PlaybotJob JSON
stdout: PlaybotResult JSON
stderr: 脱敏日志
```

独立二进制要求：

- stdout 只能输出最终 `PlaybotResult` JSON。
- stderr 可以输出脱敏日志。
- 不得在日志、错误或 trace 中泄露 API Key、Cookie、Storage value 或本地绝对路径。
- 退出码只表达进程级成功或失败；业务失败必须通过 `PlaybotResult.status/code` 表达，便于后端按规则处理。
- 后续升级 RPC 时必须复用同一套 `PlaybotJob` / `PlaybotResult` 协议。
- `PlaybotJob` JSON 不得包含 LLM API Key、Cookie、Storage value 或其他密钥明文。
- LLM API Key 必须通过受控 secret channel 传递，例如受控环境变量、只读 secret file descriptor、进程私有 secret provider 或后续 RPC metadata；该 channel 必须可脱敏、可审计，并避免落入 job fixture、临时 job 文件和普通日志。
- 如果实现必须使用临时 secret 文件，文件权限必须限制为当前用户可读写，建议等价于 `0600`，执行后必须清理；普通 `job.json` 仍不得包含密钥。

`PlaybotJob` 至少包含：

- `schema_version`。
- `mode`：`generate` / `optimize` / `repair_proposal`。
- `request_id`。
- `project_scope`。
- `page_context`。
- `recording_source`。
- `backend_approved_context`。该字段仅在 agent 返回 `context_required + retryable` 后的受控重跑 job 中出现，首次 job 必须省略或为空。
- `existing_blueprint`。
- `execution_report`。
- `user_instruction`。
- `llm_runtime_config`。该字段只能包含 provider、endpoint、model、config id、超时、重试和脱敏后的配置摘要，不得包含 API Key 明文。
- `limits`。

`backend_approved_context` 是后端补上下文的唯一协议载体，不是 agent 的长期记忆，也不是新的业务事实源。字段形态为数组，每一项至少包含：

- `kind`：补充上下文类型，例如 `dom_snapshot`、`action_trace`、`recording_meta` 或 `page_context`。
- `scope`：对应 `requested_context.scope`，例如录制动作 ref id、DOM 片段 id 或页面范围。
- `source`：上下文来源，例如 `page_script.dom_snapshot` 或受保护流程内的 `recording_session.actions`。
- `payload`：后端按确定性规则裁剪后的上下文片段。

约束：

- `backend_approved_context` 只能由后端写入，agent 不得在结果中要求直接落库该字段。
- 每一项必须对应上一轮 `requested_context` 中后端允许补充的请求，不能携带未被请求或未被后端批准的自由上下文。
- 内容必须来自有效 PageScript，或受保护流程内的 stopped RecordingSession；不得来自 RecordingArtifact 元数据单独推导。
- 内容不得包含 LLM API Key、Cookie、Storage value 或本地绝对路径。
- 二次重跑仍必须受次数上限约束；重复请求同一上下文不得无限循环。

`PlaybotResult` 至少包含：

- `schema_version`。
- `status`：`success` / `failed` / `context_required`。
- `code`。
- `test_cases` 或 `refined_blueprint`。
- `quality_errors`。
- `requested_context`。
- `context_trace`。
- `warnings`。
- `repair_proposal`。

`context_trace` 用于审计：

```json
{
  "source_page_script_id": "ps_123",
  "source_recording_session_id": "",
  "source_hash": "sha256:...",
  "used_fields": ["action_trace", "dom_snapshot", "recording_meta"],
  "truncated": ["dom_snapshot"]
}
```

## 八、后端确定性裁决

后端不需要，也不应该用 LLM 决定保存、重录、补上下文或替换旧资产。

后端只消费 agent 的结构化结果，按规则状态机处理：

```text
success
  -> 严格保存校验
  -> 执行归一化
  -> preview / append / replace

context_required + retryable
  -> 后端判断是否存在可补上下文
  -> 通过 backend_approved_context 补 job 后重跑一次或有限次数

recording_quality_error
  -> 不调用 LLM 猜测
  -> 不保存 TestCase
  -> replace 不删除旧 TestCase
  -> 提示重录或优化录制

blueprint_validation_failed
  -> 不保存 TestCase
  -> 可生成 repair_proposal 草案
  -> 不自动应用
```

示例规则：

- `recording_action_missing_target`：如果后端还有未传入的完整 ActionTrace/DOMSnapshot，可以补上下文重跑；如果录制本身没有目标信息，拒绝生成并提示重录或优化录制。
- `recording_snapshot_unusable`：如果有可用 PageScript 快照版本，可以补快照重跑；如果没有，拒绝生成。
- `recording_auth_context_conflict`：不重跑，不让 LLM 猜；拒绝生成，要求用户重新选择登录态或重录。
- `blueprint_validation_failed`：不保存 TestCase，可进入 repair proposal 草案，但不自动改资产。

后端补上下文时必须把批准后的片段写入下一次 `PlaybotJob.backend_approved_context`，并保留 agent 原始 `requested_context` 作为裁决依据。`backend_approved_context` 只能补足后端已经掌握、允许传给 agent 的录制事实片段，不能补 Cookie/Storage 明文，也不能让 agent 通过反复请求扩大到不相关页面或资产。

LLM 可以参与建议层，例如解释录制质量差的原因、生成重录建议、生成 `repair_proposal` 草案，但不能参与裁决层。

## 九、输入事实源

Playbot 生成用例必须优先基于当前页面有效 PageScript。若需要使用 stopped RecordingSession 产出的录制结果，必须先把该 session 保存为 PageScript，或在同一受保护事务/锁定流程中完成 `session -> PageScript -> TestCase`，不能产生“TestCase 已生成但主流程事实源未落库”的状态。

输入事实源包括：

- `ActionTrace`。
- `DOMSnapshot`。
- `RecordingMeta`。
- 录制动作中的 selector、role、text、placeholder、DOM fragment。
- 录制时 URL。
- 录制时登录态上下文。
- 录制时关键页面状态。

事实源优先级：

1. 已保存的当前页面有效 PageScript。
2. stopped RecordingSession 产出的录制结果，但前提是先保存为 PageScript，或在同一受保护事务/锁定流程中完成 `session -> PageScript -> TestCase`。
3. 用户 prompt 只能补充测试意图、断言重点和命名偏好，不能替代录制事实。

RecordingArtifact 元数据只作为诊断、溯源或附件摘要，不能单独满足生成前置条件，也不能替代 `ActionTrace`、`DOMSnapshot` 或 `RecordingMeta` 作为动作和 DOM 事实源。没有有效 PageScript 或受保护流程中的 stopped RecordingSession 时，不得仅凭截图、下载文件、录屏或其元数据生成 TestCase。

如果录制结果缺少生成可执行 Blueprint 所需的信息，不允许让 LLM 猜测补齐。系统应返回录制质量错误，不创建 TestCase，不删除旧 TestCase。

## 十、录制质量错误

录制质量错误是 P4.7.5 的正式错误类别，用于区分“录制数据本身不足”和“Playbot/LLM 输出非法”。

至少需要支持以下错误 code：

- `recording_action_missing_target`：录制动作缺少可执行定位信息，例如没有 selector、role/text、placeholder、label、DOM fragment 或 ref id。
- `recording_action_missing_value`：录制输入、选择或文本断言类动作缺少必要值。
- `recording_navigation_missing_url`：导航动作缺少可执行 URL，且无法从录制上下文推导。
- `recording_snapshot_unusable`：DOMSnapshot 为空、结构非法或无法支持生成定位信息。
- `recording_meta_invalid`：RecordingMeta 缺失、结构非法、recording_kind/auth_context 不合法或与当前页面不匹配。
- `recording_auth_context_conflict`：录制元数据中的登录态上下文与当前页面、项目登录态或生成请求冲突。

错误处理要求：

- 录制质量错误不得调用 LLM 继续猜测。
- 录制质量错误不得创建新的 TestCase。
- `replace` 模式下录制质量错误不得删除旧 TestCase。
- 错误响应应包含可区分 code，便于前端提示“需要重新录制或优化录制功能”。
- 错误响应不得泄露 API Key、Cookie、Storage value 或本地绝对路径。

## 十一、Blueprint 对外标准

最终保存的 active TestCase 必须使用 Go runner 可执行 Blueprint 作为对外标准。

标准 step 示例：

```json
{
  "action": "click",
  "description": "点击保存按钮",
  "target": {
    "role": "button",
    "text": "保存",
    "recorded_selector": "button.save"
  },
  "timeout_ms": 10000
}
```

字段规则：

- `navigate` 使用 `url`。
- `fill`、`select`、`expect_text` 使用 `value`。
- 定位信息使用 `target`。
- `target_hint` 只能作为 agent 内部兼容输入或中间信息，不能成为最终唯一事实源。
- `intent_reason` 只能作为 agent 内部解释或中间信息，最终应转成 `description` 或丢弃。
- `auth_context` 继承 PageScript recording meta，不能由 Playbot 自行改写为非法值。
- unsupported action 不得保存为 active TestCase。
- 缺少可执行定位字段的交互步骤不得保存为 active TestCase。

最终 active Blueprint 的 action 集合以 Go 后端执行归一化逻辑为准。P4.7.5 不要求前端或 agent 重新定义第二套 action 标准。

## 十二、能力边界

P4.7.5 后，Playbot 能力按四类边界理解。

### 12.1 generate

职责：

- 基于录制结果、页面快照和用户说明生成新的 TestCase Blueprint。
- 输出必须经过录制质量校验、semantic plan 归一化、executable Blueprint compiler 和 executable Blueprint validator。

不得：

- 基于 LLM 猜测补齐关键 URL、selector、输入值或登录态。
- 输出只能被 agent 理解、不能被 Go runner 归一化的 active Blueprint。
- 在录制质量不足时创建 TestCase。

### 12.2 optimize

职责：

- 基于现有 Blueprint 和用户 prompt 生成 proposed 优化版本。
- proposed 版本也必须遵守 executable Blueprint 输出标准。
- 只创建 `LLMRefinement` 或等价 proposed 记录，等待用户确认。

不得：

- 直接修改 active TestCase。
- 绕过 Go 后端执行归一化能力。
- 把用户 prompt 当作新的录制事实源。

### 12.3 execute

职责：

- 正式执行由 Go 后端 runner 负责。
- 执行输入来自已保存 TestCase 的 executable Blueprint。
- 有头或无头浏览器能力以 Go BrowserManager/BrowserInstance 为准。

不得：

- 让独立 Go agent 或 Python execution engine 成为正式产品执行路径。
- 在 P4.7.5 引入原生 Playwright spec runner。
- 因为 agent 能生成或校验 Blueprint，就跳过 Go 后端 runner 的归一化和执行校验。

### 12.4 repair_proposal

职责：

- 基于失败执行报告、失败步骤、页面错误摘要和已有录制事实生成修复建议草案。
- 输出可以是候选 selector、断言调整、重录建议或优化说明。
- 暂时只作为预留边界，不自动应用。

不得：

- 自动修改 PageScript。
- 自动修改 active TestCase。
- 自动替换 RecordingSession 或 RecordingArtifact。
- 在没有录制事实支撑时编造可执行步骤。

## 十三、Go Agent 输出管线

独立 Go agent 输出前必须经过以下管线：

```text
PlaybotJob
-> recording quality validation
-> context packing / token budget
-> normalized semantic plan
-> LLM generated / optimized semantic cases
-> executable Blueprint compiler
-> executable Blueprint validator
-> PlaybotResult JSON
```

compiler 必须处理：

- `navigate.value` 或 semantic navigate target 转为最终 `navigate.url`。
- `target_hint` 转为最终 `target`。
- `intent_reason` 转为 `description`。
- unsupported action 拒绝输出。
- 缺少可执行定位字段的交互步骤拒绝输出。
- 缺少输入值、选择值或文本断言值的步骤拒绝输出。
- 无法从录制结果推导的关键动作不得由 LLM 凭空补齐。

agent 输出要求：

- `PlaybotResult` 中 active TestCase 的 `steps` 必须是 Go 标准可执行步骤。
- `PlaybotResult` 不得要求后端理解 agent 私有执行字段才能保存。
- `PlaybotResult` 可以保留非执行摘要字段，例如生成说明、风险提示、source summary、context trace，但这些字段不能替代 active Blueprint。
- agent 内部 semantic IR 可以存在，但不得直接作为外部标准保存。

## 十四、后端保存保护

后端生成接口保存前必须执行两层校验：先按 P4.7.5 最终 Blueprint 字段标准做严格保存校验，再复用现有执行归一化能力确认 runner input 可消费。不能只调用当前宽松执行归一化，因为现有归一化为了兼容历史执行路径，允许 `navigate` 缺 `url` 时回退页面 URL，也允许 `target_hint` 作为定位兼容输入；这些兼容能力不得成为 Playbot 新生成 active Blueprint 的保存标准。

任一 active Blueprint 无法通过严格保存校验或执行归一化时：

- `preview` 返回错误。
- `append` 不创建新 TestCase。
- `replace` 不删除旧 TestCase。
- 错误响应不得泄露 API Key、Cookie、Storage value 或本地绝对路径。

后端保存前校验至少包括：

- JSON 必须可解析。
- 生成结果必须包含非空 TestCase。
- 每个 active TestCase 必须包含非空 steps。
- 每个 step 必须是对象。
- 严格保存校验要求 `navigate` 必须使用 `url`，不能只提供 `value`，也不能依赖执行归一化默认页面 URL。
- 严格保存校验要求交互类步骤必须有最终 `target`，不能只靠 `target_hint` 兼容输入。
- unsupported action 必须拒绝。
- `auth_context` 必须继承录制 meta 或保持合法值。
- 执行归一化失败时不得改变旧资产。

该校验不替代 Go runner，而是保存前的最低门槛：能保存的 active Blueprint，必须同时满足 P4.7.5 最终字段标准，并能被 Go 后端执行归一化逻辑消费。P4.7.5 不要求立即移除 `RunTestCase` 对历史 Blueprint 的兼容执行能力，只要求 Playbot 新生成并保存的 active Blueprint 更严格。

## 十五、Go runner 和有头执行边界

P4.7.5 继续沿用 Go 后端 runner 作为正式执行入口。

执行边界：

- BrowserWing Blueprint 由 Go 后端 runner 执行。
- Go runner 使用 BrowserManager 和 BrowserInstance 管理浏览器。
- 有头执行能力来自 BrowserInstance 的浏览器启动配置。
- P4.7.5 不新增 Playwright spec runner。
- P4.7.5 不把独立 Go agent 或 Python execution engine 接入正式 `RunTestCase` 路径。

关于 `headless` 字段：

- 当前 `RunTestCase` 请求体已有 `headless` 字段。
- 当前实现未把该字段实际传入执行准备链路。
- P4.7.5 不依赖该字段实现按次切换有头或无头。
- 如果后续要支持按次切换，需要单独补契约、红测和实现。

## 十六、红测要求

后端生成接口红测必须覆盖：

- 后端通过独立 Go agent 二进制或等价 adapter 生成结果，不调用 Python `playbot-engine`。
- 后端组装 `PlaybotJob` 时只使用有效 PageScript，或受保护流程内的 stopped RecordingSession。
- RecordingArtifact 元数据不能单独满足生成前置条件。
- 录制结果缺少目标时，拒绝生成并保持旧资产不变。
- 录制结果缺少输入值时，拒绝生成并保持旧资产不变。
- 录制导航缺少 URL 时，拒绝生成并保持旧资产不变。
- DOMSnapshot 不可用时，拒绝生成并保持旧资产不变。
- RecordingMeta 非法时，拒绝生成并保持旧资产不变。
- auth_context 与录制上下文冲突时，拒绝生成并保持旧资产不变。
- agent 返回 `context_required + retryable` 时，后端只按确定性规则补上下文并有限重跑。
- 后端补上下文重跑时，二次 `PlaybotJob` 必须通过 `backend_approved_context` 携带已批准片段；首次 job 不得预置该字段。
- agent 返回录制质量硬错误时，后端不调用 LLM 猜测、不创建 TestCase、`replace` 不删除旧 TestCase。
- agent 返回 success 时，后端仍必须执行严格保存校验和执行归一化。
- Playbot 生成的 active TestCase 必须能通过 Go 后端执行归一化。
- `navigate` 只给 `value` 不给 `url` 时不得保存。
- unsupported action 不得保存。
- `replace` 模式下可执行校验失败不得删除旧 TestCase。
- 错误响应不得泄露 API Key、Cookie、Storage value 或本地绝对路径。

Go agent 红测必须覆盖：

- 独立二进制 stdout 只输出 `PlaybotResult` JSON，stderr 只输出脱敏日志。
- `PlaybotJob` / `PlaybotResult` schema version 必须可校验。
- `backend_approved_context` 只允许在后端批准的 `context_required` 重跑 job 中出现，且不得泄露密钥、Cookie、Storage value 或本地绝对路径。
- `PlaybotJob` JSON、临时 job 文件、测试 fixture 和调试产物不得包含 LLM API Key 明文。
- LLM API Key 必须通过受控 secret channel 传递；日志、错误、context trace 和 `PlaybotResult` 不得泄露密钥。
- 如实现使用临时 secret 文件，必须验证权限受限且执行后清理。
- agent 不直接查数据库，不保存业务资产。
- agent 不长期缓存对话上下文作为后续任务事实源。
- agent 能把录制 click/input/navigate 编译为 Go 标准 Blueprint。
- `recorded_selector`、role/text、placeholder 优先保留到 `target`。
- `target_hint` 编译为 `target`。
- `intent_reason` 编译为 `description`。
- unsupported action 拒绝输出。
- 缺少可执行定位字段的交互步骤拒绝输出。
- 缺少必要 value 的输入或断言步骤拒绝输出。
- `context_trace` 包含 source id、source hash、used fields 和 truncation 信息。

Go 执行边界红测必须覆盖：

- `RunTestCase` 正式路径继续使用 Go `testcase_executor.Runner`。
- Python execution engine 不参与正式 `RunTestCase` 路径。
- 独立 Go agent 不参与正式 `RunTestCase` 路径。
- 有头执行以 Go BrowserManager/BrowserInstance 为准。
- P4.7.5 不新增 Playwright spec runner。

optimize 红测必须覆盖：

- optimize 只创建 proposed LLMRefinement，不直接修改 active TestCase。
- proposed Blueprint 也必须满足可执行输出标准。
- proposed 校验失败时不污染旧 TestCase。

## 十七、验收标准

P4.7.5 收口时必须满足：

- Go 后端生成契约测试通过。
- Go 后端执行归一化契约测试通过。
- Go Playbot agent 协议和 compiler fixture 测试通过。
- 独立 Go agent 二进制可被后端调用，stdout/stderr 契约稳定。
- Python `playbot-engine` 不在正式生成、优化或执行路径中。
- 录制质量错误能明确区分生成问题和录制问题。
- 现有 P1-P4.7 生成、refine、run、LLM 配置和 RecordingSession 契约不回归。
- `docs/DEVELOPMENT_PLAN.md` 更新 P4.7.5 状态。
- `docs/CONTRACT_RECORDS.md` 只在红测和实现审核通过后沉淀新契约。

建议验证入口：

```powershell
cd backend
go test ./api -run TestGenerateTestCases -count=1
go test ./api -run TestRunTestCase -count=1
go test ./api -run TestRefineTestCase -count=1

cd ..\playbot-agent
go test ./...
go build ./...
```

阶段收尾建议补跑：

```powershell
cd backend
go test ./...

cd ..\frontend
pnpm run type-check
pnpm run build
```

## 十八、后续衔接

P4.7.5 完成后，P4.8 才继续做录制到智能生成用例的端到端体验收口。

P4.8 可以依赖以下结果：

- 当前页面有效 PageScript 是生成输入事实源；stopped RecordingSession 只有在先保存为 PageScript，或同一受保护事务/锁定流程完成 `session -> PageScript -> TestCase` 时，才能参与生成。
- 后端是上下文事实源管理者和裁决者。
- 独立 Go Playbot Agent 是上下文消费、LLM 编排、Blueprint 编译者。
- PlaybotResult 已包含 Go 可执行 Blueprint，不需要前端理解 agent semantic IR。
- 后端保存前会拒绝无法通过严格最终字段校验或执行归一化的 active Blueprint。
- optimize 只产生 proposed 版本，用户确认前不修改 active TestCase。
- 正式执行路径唯一：Go 后端 runner 执行 BrowserWing Blueprint。
- Python `playbot-engine` 不再是正式产品路径。

如果 P4.8 发现生成质量差，优先判断是录制数据质量不足、context packing 缺失、compiler 映射缺失、LLM prompt 不稳定，还是 Go runner 执行能力缺口。只有录制内容本身不足时，才回到录制功能优化；不能通过让 LLM 猜测、让后端用 LLM 裁决或引入第二套正式执行路径绕过问题。
