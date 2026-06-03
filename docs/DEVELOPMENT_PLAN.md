# BrowserWing + Playbot 开发计划文档

本文档基于 `docs/CORE_REQUIREMENTS.md` 制定开发顺序。后续实现尽量按阶段推进，每个阶段都要能独立验证。

## 一、开发策略

开发目标不是一次性重构全项目，而是沿着现有模型和页面补齐闭环。

优先级规则：

- 先打通主流程，再补体验细节。
- 先保留结构化 Blueprint，再生成脚本。
- 先保证单用户正确，再补多用户隔离。
- 先做可执行用例，再做复杂报告。
- 每个阶段都留下可复现验证方式。

每个阶段的协作执行顺序：

1. 规划者主导轻量详细设计：明确业务契约、API、数据流、输入输出和验收口径。
2. 业务开发者反馈技术可行性：指出实现成本、系统约束和风险，但不主导业务契约定稿。
3. 用例编写者写契约红测：先证明期望行为，区分回归保护和红测立法。
4. 代码审核者审核红测：确认测试立法合理，避免把偶然实现写成契约。
5. 业务开发者实现：按通过审核的红测修生产代码，追根因，不压表象。
6. 代码审核者复核：审实现是否根因修复，并按影响面跑最终验证。
7. 规划者收尾：更新计划、契约记录和遗留风险，确认下一阶段是否可以开始。

## 二、阶段总览

| 阶段 | 目标 | 交付结果 |
|------|------|----------|
| P0 | 环境和仓库稳定 | 已完成：Git、pnpm、uv、依赖和忽略规则稳定 |
| P1 | 打通生成用例链路 | 已完成：页面主流程可以调用 Playbot 生成并保存 TestCase |
| P2 | 用例管理 | 已完成：用例列表、详情、编辑、删除、状态管理 |
| P3 | 用例执行 | 已完成：单用例执行、保存结果、展示报告 |
| P4 | 自然语言修改 | 已完成：自然语言生成修改建议，确认后应用并记录历史 |
| P4.5 | 录制体验和项目登录态 | 当前规划：页面列表、短录制流程、完整登录态、清洁会话 |
| P5 | 多用户和权限 | 后续阶段：项目数据归属、API 权限校验、用户隔离 |
| P6 | 稳定化和发布 | 回归测试、文档、打包、发布检查 |

## 三、P0：环境和仓库稳定

当前状态：

- Git 已重新绑定到目标远端。
- pnpm 已重建前端依赖。
- uv、Python、项目虚拟环境已迁移到 `D:\depends\python`。
- `.gitignore` 已补充 Python、Node 和运行产物规则。
- `playbot-engine` 已补 `jinja2` 依赖并生成 `uv.lock`。

验收标准：

- `git status --short` 只包含预期开发改动。
- `go test ./...` 可通过。
- `pnpm install` 可复现前端依赖。
- `uv lock --check` 可通过。
- Playbot 核心依赖可导入。

后续注意：

- Go 服务调用 Playbot 时不能硬编码 `python`，需要配置 Python/uv 路径。
- 前端 pnpm 的 ignored builds 警告需要单独确认是否执行 `pnpm approve-builds`。

## 四、P1：生成用例链路

当前状态：已完成。

阶段提交：

- `b734ea6 docs: add p1 test case generation design`
- `f186921 test: add P1 test case generation contracts`
- `f4db0ab feat: implement P1 test case generation`

目标：

用户在业务页面上点击“智能生成测试用例”，系统读取该页面主流程录制和语义快照，调用 Playbot，保存生成的 TestCase。

已交付：

- 新增生成接口 `POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/generate`。
- 支持 `append`、`replace`、`preview` 三种生成模式。
- 生成前校验 project/version/page 层级归属。
- 没有主流程、录制 JSON 非法、LLM 配置缺失、Playbot 失败或输出非法时返回明确错误。
- Playbot 成功后将结构化 Blueprint 保存为 `TestCase`，P1 阶段 `ScriptContent` 允许为空。
- `preview` 不写数据库，`append` 追加，`replace` 通过事务覆盖。
- Playbot Python 路径和 engine 目录通过配置或环境变量解析，不再硬编码 `python`。
- 前端“智能生成测试用例”按钮已接入生成弹窗，支持模式选择、LLM 配置选择、额外说明、loading、错误提示和结果刷新。
- P1 契约记录已沉淀到 `docs/CONTRACT_RECORDS.md`。

阶段验证：

- 后端契约红测覆盖：无主流程拒绝生成、preview 不落库、append 追加、replace 覆盖和失败回滚、Playbot 失败不破坏旧用例、层级不匹配拒绝、非法 Playbot 输出拒绝保存。
- Playbot service 单元测试覆盖显式无效 `PLAYBOT_ENGINE_DIR` 不应静默回退。
- 代码审核已完成，P1 可以进入规划者收尾状态。

遗留风险：

- 当前录制页保存的 `dom_snapshot` 仍可能是弱快照，生成质量更多依赖 ActionTrace；高质量语义快照增强后续单独处理。
- P1 只完成生成和保存，不提供 TestCase 详情、编辑、删除和执行闭环；这些进入 P2/P3。
- P1 已做项目、版本、页面层级校验，但完整用户/租户数据隔离仍属于 P5。

后续衔接：

P1 已收尾，后续生成入口和保存规则继续作为 P2/P3 的上游契约。

## 五、P2：用例管理

当前状态：已完成。

阶段提交：

- `1f3dc23 docs: add p2 test case management design`
- `f7f1afb test: add P2 test case management contract tests`
- `55a7547 feat: add P2 test case management`

目标：

用户可以进入用例详情页，查看、编辑、删除测试用例。

已交付：

- 新增 TestCase 列表、创建、详情、更新、删除 API。
- 所有 TestCase 管理接口都校验 project、version、page、testcase 完整层级归属。
- 列表接口返回轻量 summary，不返回大体积 `Blueprint` 和 `ScriptContent`。
- 详情接口返回可编辑的 TestCase 资产，`Blueprint` 以结构化 JSON 对象返回；腐坏 Blueprint 返回错误，不静默伪造空对象。
- 手工创建 TestCase 不依赖主流程录制或 Playbot，`status` 默认 `active`。
- `active` 用例必须有非空 `steps`；`draft` 允许保存不完整草稿。
- 更新接口支持标题、描述、`Blueprint`、`ScriptContent`、`Status` 的部分更新，并把标题和描述同步归一化到 Blueprint 顶层。
- 更新失败不污染既有 TestCase，删除只删除目标用例，不影响同页或其他页面用例。
- 前端新增 TestCase 详情页，支持从页面用例列表进入，编辑 JSON Blueprint、脚本内容、状态，并执行保存和删除。
- P2 契约记录已沉淀到 `docs/CONTRACT_RECORDS.md`。

阶段 API：

```text
GET    /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases
POST   /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases
GET    /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid
PUT    /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid
DELETE /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid
```

阶段验证：

- 后端契约红测覆盖 TestCase 列表摘要、详情读取、手工创建、状态校验、部分更新、失败不变更、删除隔离和层级校验。
- 前端实现已提供真实详情页和管理入口，没有引入 P3/P4 的假执行、假报告或假自然语言修改入口。
- 代码审核已完成，P2 已由规划者收尾。

遗留风险：

- P2 的 TestCase `status` 只表示资产管理状态，执行结果状态仍留给 P3 的 `TestExecution`。
- 当前前端 Blueprint 编辑仍以 JSON 文本为主，结构化步骤编辑器可在后续体验优化中补充。
- 完整用户/租户隔离仍属于 P5；P2 只保证项目、版本、页面、用例层级一致。

后续衔接：

P2 的 TestCase 资产管理契约已经作为 P3 执行输入继续沿用。

## 六、P3：用例执行

当前状态：已完成。

阶段提交：

- `03afc26 docs: add p3 test case execution design`
- `9267811 docs: refine p3 execution design review findings`
- `ee68c89 test: add P3 test case execution contract tests`
- `430af9c feat: add P3 test case execution`

阶段设计：

- `docs/P3_TEST_CASE_EXECUTION_DESIGN.md`

目标：

用户可以执行单个测试用例并查看结果。

已交付：

- 新增 TestCase 执行 API、执行记录列表 API 和执行记录详情 API。
- 执行前继续校验 project、version、page、testcase 完整层级归属。
- 只有 `active` TestCase 可以执行；`draft`、`archived` 和不可执行 Blueprint 在执行前拒绝，不创建 TestExecution。
- P3 首版只解释执行 Blueprint，不直接执行 `ScriptContent`，也不把 `ScriptContent` 当隐藏 fallback。
- 支持 `navigate`、`click`、`fill`、`select`、`wait`、`expect_visible`、`expect_text` 首版动作。
- 支持 `target` / `target_hint`，并按 RefID、role+text、recorded selector、CSS、XPath、label、placeholder、text 等定位线索归一化。
- 明确默认页面导航和首步 `navigate` 的优先关系，并在报告中记录 `initial_navigation`。
- 执行结果保存为 `TestExecution`，状态只允许 `passed`、`failed`、`error`。
- `TestCase.Status` 不因执行结果变化，仍只表示资产状态。
- 执行记录列表按 `created_at desc, id desc` 排序，默认 20 条，最多 50 条，列表不返回完整 `report_data`。
- 执行记录详情返回 parsed `report_data`，腐坏报告返回错误，不静默伪造空报告。
- 前端 TestCase 详情页新增执行按钮、执行中状态、执行历史、最近报告、步骤状态、错误信息和截图链接。
- 页面用例卡片最近执行状态后置，没有在 P3 用 TestCase.Status 伪造执行结果。
- P3 契约记录已沉淀到 `docs/CONTRACT_RECORDS.md`。

阶段 API：

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/run
GET  /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/executions
GET  /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/executions/:eid
```

阶段验证：

- 后端契约红测覆盖执行层级校验、非 active 拒绝、不可执行 Blueprint 拒绝、无 ScriptContent fallback、执行状态保存、TestCase.Status 不变、默认/显式导航、role+text 定位优先、执行列表隔离排序、详情解析和腐坏报告错误。
- P3 实现和代码审核已完成。

遗留风险：

- P3 只完成单用例执行；页面/版本批量执行、队列、取消、并发控制仍未实现。
- 直接执行 `ScriptContent` 仍属于安全敏感能力，后续必须单独设计沙箱、权限、文件访问和审计边界。
- Blueprint schema 目前是首版执行最小集，后续 Playbot 生成输出还需要继续收敛。
- 执行 artifact 的长期存储、清理和访问控制仍需稳定化阶段补齐。
- 完整用户/租户隔离仍属于 P5。

后续衔接：

P3 的执行记录和报告可作为 P4 自然语言修改的可选上下文，但 P4 不自动执行失败修复，也不执行 `ScriptContent`。

## 七、P4：自然语言修改

当前状态：已完成。

阶段提交：

- `72334be docs: add p4 refinement design`
- `29047e7 test: add P4 refinement contract tests`
- `64d10de feat: add P4 test case refinement`

阶段设计：

- `docs/P4_NATURAL_LANGUAGE_REFINEMENT_DESIGN.md`

目标：

用户用自然语言修改用例，Playbot 返回修改后的 Blueprint 建议，用户查看修改前后差异后再确认应用。

已交付：

- 新增 TestCase Refinement API，支持生成建议、列表、详情、应用和放弃。
- `refine` 只生成 `proposed` 修改建议，不覆盖 TestCase。
- `LLMRefinement` 已扩展，保存用户 prompt、修改前 Blueprint、修改后 Blueprint、摘要、风险提示、状态和应用时间。
- 所有 Refinement API 都校验 project、version、page、testcase、refinement 完整层级归属。
- 显式传入 execution 上下文时，执行记录必须属于当前 TestCase；腐坏执行报告会拒绝 refine。
- 没有主流程录制不阻止自然语言修改；缺少或腐坏页面上下文会作为 warning 传给 Playbot，不静默伪造上下文。
- Playbot refine 输出必须是合法 JSON，包含合法 `refined_blueprint`、非空 summary 和风险提示字段；非法输出不创建 Refinement，也不修改 TestCase。
- `apply` 只允许应用 `proposed` 建议，并通过修改前 Blueprint 快照防止旧建议覆盖新内容。
- `apply` 成功后更新 TestCase 标题、描述和 Blueprint；`ScriptContent` 和 `Status` 保持不变。
- `description` 允许同步为空字符串，支持自然语言清空描述。
- `discard` 只标记建议为 `discarded`，不修改 TestCase。
- Playbot CLI 新增 `--mode refine`，Go service 复用既有 Python/engine 路径解析、LLM 配置读取和 API Key 脱敏。
- 前端 TestCase 详情页新增自然语言修改面板，支持 prompt、可选执行报告上下文、建议详情、修改前后对比、应用、放弃和历史列表。
- 前端在本地表单存在未保存修改时阻止 refine/apply，避免后端基于旧 Blueprint 生成或应用建议。
- P4 契约记录已沉淀到 `docs/CONTRACT_RECORDS.md`。

阶段 API：

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refine
GET  /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refinements
GET  /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refinements/:rid
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refinements/:rid/apply
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refinements/:rid/discard
```

阶段验证：

- 后端契约红测覆盖层级和 prompt 校验、refine 不覆盖原用例、无主流程仍可 refine、execution 上下文归属、Playbot 非法输出拒绝、历史列表摘要、详情解析、应用成功、清空描述、stale apply、非 proposed 拒绝、discard、事务保护和 LLM 配置复用。
- 验证命令已通过：`go test ./...`、`pnpm run type-check`、`pnpm run build`、`D:\depends\python\venvs\browserwing-playbot\Scripts\python.exe -c "import cli; print('ok')"`。
- 代码审核已通过，P4 已由规划者收尾。

遗留风险：

- P4 只完成单用例自然语言修改；批量失败自修复、一键从失败报告生成修复建议和队列化修复后置。
- `ScriptContent` 仍不参与自然语言修改和执行，不提供脚本自动更新。
- P4 只记录自然语言修改历史，手工编辑审计仍未补齐。
- 页面语义快照质量仍影响 Playbot 修改质量，缺少主流程时只能通过 warning 告知风险。
- 完整用户/租户隔离仍属于 P5。

下一步：

进入 P4.5。规划者先编写并定稿 P4.5 录制体验与项目登录态详细设计，明确页面列表、短录制流程、项目登录态、清洁会话、执行登录态恢复和红测口径。

## 八、P4.5：录制体验和项目登录态

当前状态：规划中。

阶段设计：

- `docs/P4_5_RECORDING_EXPERIENCE_AND_AUTH_STATE_DESIGN.md`

目标：

压缩“创建页面 -> 进入录制 -> 打开浏览器 -> 录制 -> 保存 -> 生成用例”的人工路径，同时补齐项目登录态。用户可以保存项目版本的登录状态，后续录制登录后的业务流程和执行测试用例时显式复用；录制登录流程时必须使用干净会话，不得被已保存登录态跳过登录页。

计划交付：

- 页面管理从大卡片改为列表模式，适合页面数量变多后的扫描和批量操作。
- 页面行内提供录制登录流程、录制业务流程、更新登录态、智能生成、新建用例、查看用例和删除入口。
- 新增项目版本默认登录态，覆盖 Cookie、localStorage、sessionStorage，并只返回摘要给前端。
- 页面录制模式从通用浏览器页收敛出短流程，只展示项目页面录制需要的操作。
- 新增 `clean` 和 `project_saved` 两种会话模式；项目页面录制和 TestCase 执行不得无条件加载全局 `browser` Cookie Store。
- PageScript 保存录制元数据，TestCase Blueprint 顶层保存 `auth_context`。
- 执行 TestCase 时在首次导航前按 `auth_context` 恢复项目登录态；只有显式 `project_saved` 且缺少登录态时才执行前失败且不创建 TestExecution。
- Playbot 只接收非敏感会话口径，不接收 Cookie、localStorage、sessionStorage 明文。

协作顺序：

1. 规划者定稿 P4.5 设计。
2. 业务开发者 review 设计可行性。
3. 用例编写者先写契约红测，覆盖登录态捕获、清洁会话、项目登录态恢复、录制元数据、Blueprint `auth_context`、执行前校验和前端列表入口。
4. 代码审核者先审红测。
5. 业务开发者按通过审核的红测实现。
6. 代码审核者复核实现并运行验证。
7. 规划者收尾并沉淀契约记录。

遗留风险：

- P4.5 首要保证 Cookie、localStorage、sessionStorage；IndexedDB、CacheStorage 和 Service Worker Cache 的完整恢复仍可能需要后续稳定化扩展。
- 登录态是敏感资产，P4.5 必须保证 API、日志、Playbot 输入和执行报告不泄露明文；发布前仍应补齐加密存储和审计。
- 如果底层浏览器控制仍只有全局 profile，开发实现需要先补隔离上下文，否则干净会话契约不成立。

## 九、P5：多用户和权限

目标：

项目数据和执行数据按用户或租户隔离。

### 数据模型任务

建议增加：

- `Project.OwnerUserID`。
- 可选 `Project.TenantID`。
- `ProjectMember` 表，用于多人协作。

`ProjectMember` 字段：

- ProjectID。
- UserID。
- Role：owner、admin、editor、viewer。

### API 任务

1. 所有 project 查询必须按当前用户过滤。

2. 所有 page/version/testcase/execution 操作必须校验项目权限。

3. 管理员用户可管理用户，但不能绕过项目权限边界，除非明确设计为系统管理员。

### 前端任务

- 项目设置页增加成员管理。
- 根据权限隐藏或禁用按钮。

### 验收

- 用户不能看到无权限项目。
- 用户不能通过猜 ID 访问别人的页面或用例。
- viewer 不能修改或执行用例。
- editor 可以编辑和执行。
- owner/admin 可以管理成员。

## 十、P6：稳定化和发布

目标：

完成测试、文档和发布准备。

### 自动化验证

后端：

```bash
cd backend
go test ./...
```

前端：

```bash
cd frontend
pnpm install
pnpm run type-check
pnpm run build
```

Playbot：

```powershell
cd playbot-engine
$env:UV_PROJECT_ENVIRONMENT = "D:\depends\python\venvs\browserwing-playbot"
uv lock --check
uv sync --all-extras
D:\depends\python\venvs\browserwing-playbot\Scripts\python.exe -c "import cli; print('ok')"
```

### 人工验收

需要覆盖：

- 创建项目。
- 创建版本。
- 创建页面。
- 保存项目登录态。
- 用干净会话录制登录流程。
- 录制主流程。
- 生成用例。
- 编辑用例。
- 执行用例。
- 查看报告。
- 自然语言修改用例。
- 克隆版本并确认用例被复制。
- 多用户权限隔离。

### 文档

需要更新：

- README 中文说明。
- 安装说明。
- 测试指南。
- Playbot 环境配置说明。
- API 文档。

## 十一、建议开发顺序

第一轮：

1. 规划者编写 `docs/P1_TEST_CASE_GENERATION_DESIGN.md`，明确生成用例链路的业务契约、API、Playbot 输入输出、保存规则和验收用例。
2. 业务开发者 review P1 设计可行性，只反馈实现成本、接口约束和风险。
3. 用例编写者基于 P1 设计写红测，优先覆盖：无主流程拒绝生成、preview 不落库、append 追加、replace 覆盖、Playbot 失败不破坏旧用例。
4. 代码审核者审核红测合理性，确认测试没有固化偶然实现或写第二套事实来源。
5. 业务开发者实现 Playbot Python 路径配置、生成用例 API 和 TestCase 保存逻辑，直到红测转绿。
6. 用例编写者补前端契约或集成用例，覆盖“智能生成测试用例”按钮的主要交互路径。
7. 业务开发者接入前端生成按钮和结果刷新。
8. 代码审核者复核 P1 全链路，运行后端、前端和 Playbot 相关验证。
9. 规划者更新计划、契约记录和遗留风险。

第二轮：

1. 规划者编写 P2 用例管理详细设计。
2. 业务开发者 review P2 设计可行性。
3. 用例编写者写 TestCase CRUD、详情读取、编辑保存、删除隔离的契约用例。
4. 代码审核者审核红测。
5. 业务开发者实现后端 TestCase CRUD 和前端用例详情页。
6. 代码审核者复核并跑相关验证。
7. 规划者更新计划、契约记录和遗留风险。

第三轮：

1. 规划者编写 P3 用例执行详细设计。
2. 业务开发者 review P3 设计可行性。
3. 用例编写者写 Blueprint 执行、单用例执行 API、执行记录保存的契约用例。
4. 代码审核者审核红测。
5. 业务开发者实现 Blueprint 执行解释器、单用例执行 API、执行记录 API 和前端结果展示。
6. 代码审核者复核并跑相关验证。
7. 规划者更新计划、契约记录和遗留风险。

第四轮：

1. 规划者编写 P4 自然语言修改详细设计。
2. 业务开发者 review P4 设计可行性。
3. 用例编写者写 Refinement API、未确认不覆盖、应用后记录历史的契约用例。
4. 代码审核者审核红测。
5. 业务开发者实现 Playbot refine、Refinement API 和前端自然语言修改面板。
6. 代码审核者复核并跑相关验证。
7. 规划者更新计划、契约记录和遗留风险。

第五轮：

1. 规划者编写并定稿 P4.5 录制体验和项目登录态详细设计。
2. 业务开发者 review P4.5 设计可行性，重点反馈浏览器隔离、Storage 捕获和恢复、敏感存储风险。
3. 用例编写者先写登录态捕获、清洁会话、项目登录态恢复、录制元数据、Blueprint `auth_context` 和前端列表入口红测。
4. 代码审核者审核红测，确认没有固化当前全局 Cookie Store 或偶然实现。
5. 业务开发者实现页面列表、短录制模式、项目登录态 API、录制元数据和执行登录态恢复。
6. 代码审核者复核并跑相关验证。
7. 规划者更新计划、契约记录和遗留风险。

第六轮：

1. 规划者编写 P5 多用户权限详细设计。
2. 业务开发者 review P5 设计可行性。
3. 用例编写者写项目归属、越权访问、角色权限的契约用例。
4. 代码审核者审核红测。
5. 业务开发者实现项目权限数据模型、API 权限收口和成员管理页面。
6. 代码审核者复核并跑相关验证。
7. 规划者更新计划、契约记录和遗留风险。

第七轮：

1. 用例编写者补全跨栈回归用例和人工验收清单。
2. 业务开发者修复前端 type-check、构建、Playbot 依赖和发布文档问题。
3. 代码审核者做发布前最终 review 和标准入口验证。

## 十二、当前已知风险

### Playbot 生成结果不稳定

应对：

- 强制结构化输出。
- Go 侧增加 schema 校验。
- 保存失败原始输出，便于调试。

### 元素定位不稳定

应对：

- 优先 recorded_selector。
- 再使用 role/text/placeholder/context。
- 执行失败时重新获取 snapshot 并尝试二次定位。

### 直接执行脚本有安全风险

应对：

- 首版优先解释 Blueprint。
- 脚本执行放到受控环境。
- 不允许脚本访问越权项目数据。

### 登录态串用或泄露

应对：

- 项目录制和 TestCase 执行显式区分 `clean` 和 `project_saved`。
- 登录态摘要可以展示，Cookie 和 Storage value 不进入前端、日志、Playbot 输入和执行报告。
- 显式 `project_saved` 缺少项目登录态时执行前失败，不静默降级。

### 多用户改造影响面大

应对：

- 先加 Project 归属。
- 再统一封装权限校验。
- 最后处理成员角色。

### 前端已有类型不一致

应对：

- 先修复 Project / ProjectVersion 类型和后端模型不一致。
- 补足 TestCase、Execution、Refinement 类型。
- 每阶段跑 type-check。

## 十三、阶段完成定义

每个阶段完成时必须满足：

- 相关 API 有最小测试或可复现人工验收步骤。
- 前端没有明显空按钮或假入口。
- 数据不会越权或误删。
- 失败路径有明确错误提示。
- `git diff` 中没有无关环境产物。
