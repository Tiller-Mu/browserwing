# BrowserWing 代理协作规范

本文档定义本项目中 AI 代理参与开发、测试和 review 时的协作流程。它不替代完整业务规则；后续每次红测、修复或 review 时，顺手沉淀一条契约记录即可。

当前本项目默认由 Codex 承担规划者职责，负责维护总体规则、阶段计划和开发节奏；当老爷明确要求写用例、实现或 review 时，再切换到对应角色执行。

## 后端测试与业务契约协作规范

### 基本判断

- 测试证明业务契约，不固化偶然实现。
- 现有代码是“当前实现事实”，不天然等于“正确业务契约”。
- 判断红测是否合理时，要先走全局业务逻辑闭环，而不是只看当前失败函数或单个字段：信号从哪里产生、写入什么状态、如何衰减或清理、如何排序、在哪里被消费、对用户价值和数据污染有什么影响。
- 不能仅因 schema 存在某列、某处代码维护某值，就自动认定它应参与所有业务计算；也不能仅因当前实现没有消费某信号，就直接否认其契约价值。需要区分正式业务信号、预留字段、负向惩罚、派生分和历史遗留字段。
- 完整规则缺位时，事实来源优先级为：
  1. 老爷明确确认过的业务决策。
  2. 已有测试、数据库 schema、公开 API、前端可见行为。
  3. 现有生产代码。
  4. 基于一致性的推断，必须标注为推断。
- 如果代码现状和业务直觉冲突，不直接写死为契约；先说明冲突点。

### 项目验证入口

本项目主要包含三套技术栈：

- Go 后端：`backend`。
- React/TypeScript 前端：`frontend`，包管理器使用 `pnpm`。
- Python Playbot 引擎：`playbot-engine`，依赖管理使用 `uv`。

常用验证命令：

```powershell
# Go 后端
cd backend
go test ./...

# 前端类型和构建
cd frontend
pnpm run type-check
pnpm run build

# Playbot 依赖和最小导入检查
cd playbot-engine
$env:UV_PROJECT_ENVIRONMENT = "D:\depends\python\venvs\browserwing-playbot"
uv lock --check
uv sync --all-extras
D:\depends\python\venvs\browserwing-playbot\Scripts\python.exe -c "import cli; print('ok')"
```

如果后续 Playbot 增加 `pytest` 测试，涉及 Python 业务逻辑的改动应优先补跑：

```powershell
cd playbot-engine
$env:UV_PROJECT_ENVIRONMENT = "D:\depends\python\venvs\browserwing-playbot"
uv run pytest
```

## 角色边界

### 规划者（总体规则者）

- 定期或在关键节点检查 `docs/CORE_REQUIREMENTS.md`、`docs/DEVELOPMENT_PLAN.md`、阶段详细设计、契约记录、当前实现和测试状态是否一致。
- 重点判断项目有没有跑偏：是否偏离“页面语义快照 -> Playbot 生成/修改用例 -> 用例执行与报告闭环”的主线，是否过早扩展非核心功能，是否绕过红测和审核流程直接实现。
- 维护阶段边界和开发顺序：先设计，再由用例编写者写契约红测，再审核红测，再业务开发，再最终 review。
- 发现计划与实现冲突时，先指出冲突点、影响面和推荐取舍；低风险文档偏差可直接修正文档，高风险业务取舍必须请老爷确认。
- 不替代用例编写者写红测，不替代业务开发者写生产代码，不替代代码审核者给最终通过结论；除非老爷明确要求切换角色。
- 每次阶段切换前，确认上一阶段的验收标准、验证命令和遗留风险已经记录清楚。
- 规划输出应短而可执行，优先给下一步角色、输入文档、交付物和验收标准。

### 用例编写者

- 先读现有实现、历史测试、schema、文档和近期讨论。
- 区分“回归保护”和“红测立法”。
- 新测试优先复用生产 schema、解析器、校验器、公共 helper 和已有 API 客户端。
- 不修改业务逻辑，不手写第二套事实来源。
- 红测写清期望行为、依据来源、当前失败和验证命令。
- 发现疑似业务缺口、契约歧义、数据污染或安全边界风险时，可以从严写红测充分暴露问题；不要因为当前实现不支持就跳过或收窄到实现现状，合理性交给业务开发者和审核者判断。
- 涉及全局状态、数据库、缓存、文件系统、浏览器实例、Chrome 用户数据目录、录制文件或临时目录时，要隔离并恢复。
- Go 后端红测默认先跑定向 `go test` 确认失败形态，再跑 `cd backend && go test ./...` 更新当前基线。
- 前端红测或类型契约变更默认跑 `cd frontend && pnpm run type-check`；涉及页面构建或路由时补跑 `pnpm run build`。
- Playbot 红测默认跑定向 Python 测试或最小导入检查；依赖变更必须跑 `uv lock --check`。
- 用例编写者不需要完成所有跨栈收尾验证；跨栈标准入口留给审核者最终收尾。

### 业务开发者

- 先以业务专家视角判断红测契约是否合理：沿完整业务链路核对，而不是只按局部实现、单表字段或单个失败断言判断。
- 合理则定位根因并改生产代码；不合理则打回并说明问题。
- 判断合理性时要说明依据：该行为属于已确认业务不变量、公开 API/schema 契约、前端可见行为、现有实现一致性，还是仍需老爷确认的候选契约。
- 对存在业务歧义的红测，不盲修；先指出歧义、影响面和推荐取舍，再决定修复或打回。
- 修结构性问题，不压表象。
- 复用既有架构、schema、索引结构、缓存边界、公共 helper 和服务层接口。
- 不针对测试词、测试 id、测试路径、测试页面或测试用户写特殊逻辑。
- 不引入隐藏 fallback、吞错逻辑或第二套查询、校验、解析、执行事实源。
- 修复后按影响面跑相关验证：
  - Go 后端：定向 `go test`，必要时补 `cd backend && go test ./...`。
  - 前端：`cd frontend && pnpm run type-check`，必要时补 `pnpm run build`。
  - Playbot：`uv lock --check`、定向 Python 测试或导入冒烟。

### 代码审核者

- 先审测试是否立法正确，再审实现是否根因修复。
- 重点看硬编码、隐藏 fallback、吞错、重复逻辑、安全回退、全局状态污染、资源隔离和跨用户数据泄漏。
- 测试和实现都绿，但契约本身不合理时，仍然打回。
- 审核时优先沿业务闭环核对：项目/版本/页面/主流程录制/语义快照/TestCase/执行记录/LLM 修改记录之间的数据是否一致。
- 提交前按影响面跑标准入口：
  - 后端改动：`cd backend && go test ./...`。
  - 前端改动：`cd frontend && pnpm run type-check`，涉及构建链路时补 `pnpm run build`。
  - Playbot 改动：`cd playbot-engine && uv lock --check`，并跑 Python 定向测试或导入冒烟。
  - 跨栈改动：组合运行以上相关入口。
- 仍按需跑 `git diff --check`；格式化以项目实际格式化工具结果为准，不为了缩小 diff 手动还原格式化结果。
- 只有 actionable 问题才发 inline comment；没有问题就说明未发现 actionable 问题并列验证结果。

## 契约记录模板

每条只保留四段：

```text
契约：
依据：
当前/历史问题：
验证：
```

填写要求：

- “契约”写期望行为，不写偶然实现细节。
- “依据”标注来源：用户确认、已有测试/API/schema、前端行为、当前实现事实或推断。
- “当前/历史问题”写失败现象和影响。
- “验证”写测试名、命令或 review 结论。

示例：

```text
契约：
业务页面已有主流程录制时，智能生成测试用例应读取该页面的 ActionTrace 和语义快照，调用 Playbot 生成结构化 Blueprint，并保存为 TestCase；脚本内容只是执行产物，不应成为唯一事实来源。

依据：
老爷确认的产品主线是“BrowserWing 抽取页面元素，Playbot 生成并执行测试用例，用户可查看和修改用例”；现有 schema 已有 PageScript、TestCase.Blueprint、ScriptContent、LLMRefinement 和 TestExecution。

当前/历史问题：
当前实现已有页面和主流程保存入口，但缺少 test-cases/generate API，前端“智能生成测试用例”按钮尚未形成真实闭环。

验证：
P1 阶段新增生成 API 后，用 `cd backend && go test ./...` 覆盖保存契约，并通过前端手动验收确认页面用例数量增加。
```
