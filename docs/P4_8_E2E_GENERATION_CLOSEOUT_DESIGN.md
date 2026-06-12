# P4.8 录制到智能生成用例端到端收口详细设计

本文档定义 P4.8 阶段的业务契约、体验边界和红测口径。P4.8 插在 P4.7 LLM 统一配置和录制数据管理之后、P5 多用户权限之前，目标是在已有底座上把真实用户路径收口为连续闭环：创建或进入页面，录制主流程，保存 PageScript，选择 LLM，智能生成 TestCase，并清楚处理失败路径。

P4.8 不新增大块底层存储能力，不做 P5 多用户权限，不改变 P1-P4.7 已确认的 PageScript、TestCase、ProjectAuthState、LLM 配置和 RecordingSession 契约。

## 阶段协作要求

P4.8 必须继续遵守项目契约流程，不能由开发者直接按本文档实现生产代码。

执行顺序：

1. 规划者维护并定稿本文档，明确端到端用户路径、API 编排、错误提示和验收口径。
2. 业务开发者先 review 本文档，只反馈前端页面组织、现有接口复用、浏览器录制状态恢复和 Playbot 调用风险。
3. 用例编写者在设计确认后先写契约红测和前端契约测试，覆盖空项目到生成用例主路径、无 LLM、无主流程、生成失败保护、页面列表入口和登录态路径。
4. 代码审核者先审核红测是否符合本文档，确认没有把固定页面、固定模型、固定测试数据或偶然 DOM 结构写成业务契约。
5. 业务开发者只能在红测审核通过后实现生产代码；实现中如发现本文档不可行，应退回规划者修订。
6. 代码审核者最终复核实现和测试，并按影响面运行后端、前端、Playbot 和必要人工浏览器冒烟验证。
7. 规划者阶段收尾，更新 `docs/DEVELOPMENT_PLAN.md`，并在 P4.8 审核通过后再更新 `docs/CONTRACT_RECORDS.md`。

红测优先级：

- 第一优先级是证明主路径从录制到生成用例真的跑通。
- 第二优先级是证明无录制、无 LLM、Playbot 失败和非法输出不会污染旧资产。
- 第三优先级是证明页面多时仍以列表/表格入口组织工作流，不退回大卡片和多页面跳转。

## 一、前置条件

P4.8 默认建立在以下事实已经成立之上：

- P4.6 已把业务数据统一到 PostgreSQL Store。
- P4.7 已完成 LLM 统一配置和最小系统管理员标识；`is_admin = true` 的用户可以管理 LLM 配置，普通用户可使用启用模型但不能查看 API Key。
- P4.7 已完成 RecordingSession 和 RecordingArtifact 元数据，项目页面录制不再只依赖 `sessionStorage`。
- P4.5 的 `login_flow`、`business_flow + clean`、`business_flow + project_saved` 契约继续有效。
- P1-P4 的生成、管理、执行和自然语言修改契约继续有效。

如果 P4.7 未完成，P4.8 不应直接用临时前端状态或临时 LLM 入口绕过底座缺口。

## 二、阶段目标

P4.8 完成后应满足：

- 用户从项目版本页面可以连续完成页面管理、录制、保存主流程、选择 LLM、智能生成 TestCase。
- 页面列表/表格视图是页面数量增长时的主要工作入口。
- 页面行能展示主流程录制状态、用例数量、最近生成或执行摘要。
- 录制完成后，用户可以直接进入智能生成，不需要在通用浏览器页、页面列表和用例页之间来回寻找入口。
- 无主流程录制时，前端引导用户先录制，后端也拒绝调用 Playbot。
- 无可用 LLM 时，前端引导用户去配置 LLM，后端也拒绝调用 Playbot。
- Playbot 失败、非法 JSON、非法 Blueprint、空 steps、非法 status 时，不创建新 TestCase，不删除旧 TestCase。
- login_flow、business_flow + clean、business_flow + project_saved 三条路径在体验上都可被用户明确选择和理解。

## 三、不在 P4.8 范围内

P4.8 不实现：

- 新的 LLM 配置归属模型。
- 新的 RecordingSession/RecordingArtifact 底层模型。
- 多用户成员、角色权限、租户隔离。
- Playbot 和录制页 AI 引擎合并。
- 批量生成、批量修复或失败报告自动修复。
- 高质量语义快照增强。P4.8 可以展示弱快照风险，但不把快照质量优化作为阻塞目标。

## 四、目标用户路径

P4.8 的主路径如下：

```text
进入项目版本 -> 查看页面列表 -> 创建或选择页面 -> 录制主流程 -> 停止录制 -> 保存 PageScript -> 选择 LLM -> 智能生成 TestCase -> 查看生成结果
```

登录态相关路径如下：

```text
录制登录流程(clean) -> 保存主流程并更新项目登录态 -> 录制业务流程(project_saved) -> 智能生成需要登录态的 TestCase
```

干净业务流程路径如下：

```text
录制业务流程(clean) -> 保存 PageScript -> 智能生成不依赖项目登录态的 TestCase
```

P4.8 必须保证这些路径都能从项目版本页面自然进入，而不是要求用户理解通用浏览器页、脚本库页和测试平台页之间的内部关系。

## 五、页面列表和入口组织

页面列表/表格应继续作为 TestPage 的主要工作台。

每个页面行至少展示：

- 页面名称。
- 页面路径或目标 URL 摘要。
- 主流程录制状态：未录制、已录制、录制中、录制失败。
- 最近一条主流程的录制类型和会话模式摘要。
- TestCase 数量。
- 最近生成或最近执行摘要，若 P4.8 实现成本过高，可只显示 TestCase 数量和最近更新时间。

每个页面行应提供入口：

- 录制登录流程。
- 录制业务流程，支持选择 `clean` 或 `project_saved`。
- 智能生成用例。
- 新建手工用例。
- 查看用例。
- 删除页面。

无项目登录态时：

- `business_flow + project_saved` 入口必须禁用或展示明确引导。
- 用户仍可选择 `business_flow + clean`。
- 用户可先录制登录流程并更新项目登录态。

## 六、录制完成后的下一步

录制保存成功后，前端必须给出明确下一步：

- 继续录制。
- 返回页面列表。
- 立即智能生成用例。

如果用户选择立即智能生成：

1. 前端打开生成弹窗。
2. 弹窗默认使用系统默认启用 LLM。
3. 用户可以选择其他启用 LLM。
4. 用户可以填写额外说明。
5. 成功后刷新 TestCase 列表或页面行摘要。

录制保存成功但没有可用 LLM 时：

- 前端显示“需要配置 LLM 后才能智能生成”的状态。
- `is_admin = true` 的管理员可进入 LLM 配置页。
- 普通用户只能看到不可用说明，不能填写或查看 API Key。

## 七、智能生成弹窗

智能生成弹窗应展示：

- 当前页面名称和路径。
- 当前主流程录制摘要。
- LLM 选择器。
- 生成模式：`append`、`replace`、`preview`。
- 额外说明输入框。
- 生成按钮、取消按钮。
- 错误提示区域。

LLM 选择器：

- 只展示启用配置。
- 默认选中系统默认配置。
- 没有可用配置时禁用生成按钮，并展示配置引导。
- 不展示 API Key。

生成模式：

- `append`：追加生成 TestCase。
- `replace`：事务性替换当前页面 TestCase。
- `preview`：只预览，不写数据库。

这些模式继续遵守 P1 已确认契约。

## 八、后端保护

P4.8 不允许只靠前端禁用按钮保证正确性。后端生成接口必须继续保护：

- project/version/page 层级不匹配时拒绝。
- 缺少当前有效 PageScript 时拒绝。
- PageScript.ActionTrace 为空或非法 JSON 时拒绝。
- PageScript.DOMSnapshot 为空或非法 JSON 时按既有契约处理，不静默伪造真实快照。
- LLM 配置缺失、停用或字段不完整时拒绝。
- Playbot 进程失败、stdout 非法 JSON、返回 error、Blueprint 非法、active 用例 steps 为空时拒绝。
- 失败时不得创建新 TestCase，不得删除旧 TestCase，不得修改 PageScript。

## 九、错误提示口径

前端错误提示应面向用户动作：

- 无主流程：提示先录制主流程，并提供录制入口。
- 无 LLM：提示需要管理员配置可用 LLM；`is_admin = true` 的管理员可进入配置页。
- 项目登录态缺失：提示先录制登录流程或选择干净业务录制。
- Playbot 失败：提示生成失败，保留原录制和旧用例。
- 输出非法：提示模型返回内容不符合用例格式，保留原资产。

错误提示不得包含：

- LLM API Key。
- Cookie 或 Storage value。
- 本地绝对路径。
- Playbot job JSON 原文中的敏感字段。

## 十、给测试者的红测要求

### 10.1 主路径红测

必须覆盖：

- 从空项目开始，创建项目、版本、页面后，可以录制主流程并保存 PageScript。
- 保存 PageScript 后可以打开智能生成弹窗。
- 有可用 LLM 和合法 PageScript 时，`append` 生成 TestCase 成功并刷新页面摘要。
- `preview` 不写数据库。
- `replace` 事务性替换当前页面 TestCase。

### 10.2 LLM 缺失红测

必须覆盖：

- 没有启用 LLM 时，前端生成按钮不可用或提示配置 LLM。
- 没有启用 LLM 时，后端生成接口拒绝调用 Playbot。
- 普通用户不能通过生成弹窗看到 API Key。
- `is_admin = true` 的管理员能从提示进入 LLM 配置入口，非管理员不能进入管理页或执行管理动作。

### 10.3 主流程缺失红测

必须覆盖：

- 页面没有 PageScript 时，前端提示先录制主流程。
- 页面没有 PageScript 时，后端生成接口拒绝调用 Playbot。
- 拒绝生成时不创建 TestCase。

### 10.4 失败保护红测

必须覆盖：

- Playbot 失败不改变旧 TestCase。
- Playbot 返回非法 JSON 不改变旧 TestCase。
- Playbot 返回空 steps 的 active 用例不改变旧 TestCase。
- `replace` 模式下生成失败不删除旧 TestCase。
- 错误响应和前端提示不泄露 API Key、Cookie、Storage value 或本地绝对路径。

### 10.5 登录态路径红测

必须覆盖：

- login_flow 必须使用 clean。
- business_flow 可以选择 clean。
- business_flow 选择 project_saved 时，没有 active 项目登录态必须前置失败。
- business_flow + clean 生成出的 TestCase 不强制依赖项目登录态。
- business_flow + project_saved 生成出的 TestCase 继承 `auth_context = project_saved`。

### 10.6 页面列表体验红测

必须覆盖：

- 页面列表/表格展示多个页面时仍可扫描，页面行包含录制状态和 TestCase 数量。
- 页面行内有录制、智能生成、新建用例、查看用例入口。
- 录制保存成功后，页面行主流程状态刷新。
- 生成成功后，页面行 TestCase 数量刷新。

## 十一、验收标准

P4.8 收口时必须满足：

- 后端契约红测通过。
- 前端契约测试、type-check 和 build 通过。
- Playbot generate/refine 最小验证通过。
- 人工冒烟至少覆盖：
  - 配置或选择 LLM。
  - 创建页面。
  - 录制主流程。
  - 保存 PageScript。
  - 立即生成 TestCase。
  - 查看生成结果。
- `docs/DEVELOPMENT_PLAN.md` 已更新阶段状态。
- `docs/CONTRACT_RECORDS.md` 只在审核通过后更新，不在规划阶段提前写入。

## 十二、进入 P5 的条件

只有 P4.8 满足以下条件后，才进入 P5：

- 录制到智能生成 TestCase 的主路径已跑通。
- LLM 配置入口和选择逻辑已统一。
- RecordingSession 和 RecordingArtifact 已为权限过滤预留 scope。
- 页面列表是项目页面工作的主入口。
- 关键失败路径不会污染旧资产。

P5 在此基础上实现用户、项目成员、角色权限和跨用户隔离。
