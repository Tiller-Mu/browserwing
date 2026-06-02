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

## 二、阶段总览

| 阶段 | 目标 | 交付结果 |
|------|------|----------|
| P0 | 环境和仓库稳定 | Git、pnpm、uv、依赖和忽略规则稳定 |
| P1 | 打通生成用例链路 | 页面主流程可以调用 Playbot 生成并保存 TestCase |
| P2 | 用例管理 | 用例列表、详情、编辑、删除、状态管理 |
| P3 | 用例执行 | 单用例执行、保存结果、展示报告 |
| P4 | 自然语言修改 | 用户通过自然语言修改 Blueprint，并记录历史 |
| P5 | 多用户和权限 | 项目数据归属、API 权限校验、用户隔离 |
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

目标：

用户在业务页面上点击“智能生成测试用例”，系统读取该页面主流程录制和语义快照，调用 Playbot，保存生成的 TestCase。

### 后端任务

1. 新增生成用例 API

建议路由：

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/generate
```

请求参数：

```json
{
  "mode": "append",
  "llm_config_id": "optional",
  "instruction": "optional"
}
```

`mode` 支持：

- `append`：追加新用例。
- `replace`：清空旧用例后保存新用例。
- `preview`：只返回结果，不保存。

2. 新增 TestCase 保存逻辑

将 Playbot 返回结果转换为 `models.TestCase`：

- `Title` 对应用例标题。
- `Description` 对应用例说明。
- `Blueprint` 保存完整 JSON。
- `ScriptContent` 先允许为空，后续阶段再生成。
- `Status` 默认为 `active`。

3. Playbot 调用路径配置化

当前 `backend/services/playbot/service.go` 使用 `exec.CommandContext(ctx, "python", args...)`。需要改成从配置读取 Python 可执行文件路径。

建议配置项：

```text
PLAYBOT_PYTHON=D:\depends\python\venvs\browserwing-playbot\Scripts\python.exe
PLAYBOT_ENGINE_DIR=D:\dpProject\browserwing\playbot-engine
```

4. 输入数据标准化

传给 Playbot 的输入应包含：

- `page_url`：版本 BaseURL + 页面 path。
- `snapshot`：页面语义快照。
- `intent_plan`：主流程 ActionTrace。
- `page_description`：页面描述。
- `instruction`：用户额外生成要求。

5. 错误处理

- 没有主流程时返回明确错误。
- Playbot 执行失败时返回 stderr 摘要。
- LLM 配置缺失时返回明确错误。
- `preview` 模式不写数据库。

### 前端任务

1. 给“智能生成测试用例”按钮接入 API。

2. 增加生成弹窗：

- 生成模式：追加、覆盖、仅预览。
- 额外说明输入框。
- LLM 配置选择。

3. 生成中状态：

- 禁用按钮。
- 展示进度或 loading。
- 失败时展示错误。

4. 生成后刷新页面用例列表。

### Playbot 任务

1. 确认 `cli.py` 输出稳定 JSON。

2. 确认生成结果字段和 Go 侧保存逻辑一致。

3. 如果返回字段不稳定，增加兼容转换层。

### 验收

- 有主流程的页面可以生成测试用例。
- 生成结果保存到数据库。
- 前端页面显示用例数量增加。
- 没有主流程时提示用户先录制。
- Playbot 失败不会破坏已有用例。

## 五、P2：用例管理

目标：

用户可以进入用例详情页，查看、编辑、删除测试用例。

### 后端任务

新增 TestCase API：

```text
GET    /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases
POST   /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases
GET    /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid
PUT    /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid
DELETE /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid
```

更新内容：

- 标题。
- 描述。
- Blueprint。
- ScriptContent。
- Status。

校验要求：

- 用例必须属于指定 page。
- page 必须属于指定 version。
- version 必须属于指定 project。
- 后续多用户阶段再补用户归属校验。

### 前端任务

1. 新增用例详情页。

建议路由：

```text
/projects/:projectId/versions/:versionId/pages/:pageId/test-cases/:testCaseId
```

2. 页面结构：

- 顶部：标题、状态、返回按钮、保存按钮、执行按钮。
- 标签页：Blueprint、脚本、执行记录、修改历史。
- Blueprint 用 JSON 编辑器或结构化步骤列表展示。
- ScriptContent 用代码编辑器或 textarea 展示。

3. 页面列表中的用例卡片点击进入详情页。

### 验收

- 可以打开用例详情。
- 可以修改标题、描述、Blueprint、脚本。
- 保存后刷新仍保留。
- 删除用例后列表更新。

## 六、P3：用例执行

目标：

用户可以执行单个测试用例并查看结果。

### 执行策略

优先级：

1. 如果 `ScriptContent` 存在，执行脚本。
2. 如果只有 `Blueprint`，通过执行引擎解释 Blueprint。
3. 如果两者都无法执行，返回明确错误。

首版建议先实现 Blueprint 解释执行，避免直接运行任意 Python 脚本带来的安全和环境复杂度。

### 后端任务

新增执行 API：

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/run
GET  /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/executions
GET  /api/v1/test-executions/:executionId
```

执行记录保存：

- `Status`：passed、failed、error。
- `ErrorMessage`。
- `DurationMs`。
- `ReportData`：JSON 字符串，包含步骤日志、截图路径、最终页面 URL。

### 执行引擎任务

1. 定义 Blueprint step 到 BrowserWing action 的转换。

2. 支持基础动作：

- navigate。
- click。
- fill。
- select。
- wait。
- expect_visible。
- expect_text。

3. 支持定位策略：

- recorded_selector。
- role + text。
- placeholder。
- label。
- CSS selector。
- XPath。

4. 执行失败时保存失败步骤。

### 前端任务

1. 用例详情页增加执行按钮。

2. 执行后展示：

- 状态。
- 耗时。
- 错误信息。
- 步骤日志。
- 截图链接。

3. 用例列表展示最近执行状态。

### 验收

- 单个用例可执行。
- 执行成功保存 passed。
- 断言失败保存 failed。
- 执行异常保存 error。
- 前端能看到最近执行结果。

## 七、P4：自然语言修改

目标：

用户用自然语言修改用例，Playbot 返回修改后的 Blueprint，用户确认后应用。

### 后端任务

新增 refinement API：

```text
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refine
GET  /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refinements
POST /api/v1/projects/:id/versions/:vid/pages/:pid/test-cases/:tcid/refinements/:rid/apply
```

请求参数：

```json
{
  "prompt": "增加密码为空的校验",
  "apply": false
}
```

保存内容：

- 用户 prompt。
- 修改前 Blueprint。
- 修改后 Blueprint。
- 是否应用。

### Playbot 任务

新增 refine 能力：

输入：

- 当前 Blueprint。
- 页面语义快照。
- 主流程轨迹。
- 用户 prompt。

输出：

- 修改后的 Blueprint。
- 修改说明。
- 风险提示。

### 前端任务

1. 用例详情页增加自然语言修改面板。

2. 展示修改建议：

- 修改摘要。
- Blueprint diff。
- 应用按钮。
- 放弃按钮。

### 验收

- 用户输入自然语言后能得到修改建议。
- 未确认前不覆盖原用例。
- 应用后 TestCase 更新。
- Refinement 历史可查看。

## 八、P5：多用户和权限

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

## 九、P6：稳定化和发布

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

## 十、建议开发顺序

第一轮：

1. 后端 TestCase CRUD。
2. 后端生成用例 API。
3. Playbot Python 路径配置。
4. 前端生成按钮接入。
5. 前端用例详情页。

第二轮：

1. Blueprint 执行解释器。
2. 单用例执行 API。
3. 执行记录 API。
4. 前端执行结果展示。

第三轮：

1. Playbot refine 能力。
2. Refinement API。
3. 前端自然语言修改面板。

第四轮：

1. 项目权限数据模型。
2. 所有项目相关 API 权限收口。
3. 成员管理页面。

第五轮：

1. 回归测试。
2. 修复前端 type-check 问题。
3. 发布文档。

## 十一、当前已知风险

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

## 十二、阶段完成定义

每个阶段完成时必须满足：

- 相关 API 有最小测试或可复现人工验收步骤。
- 前端没有明显空按钮或假入口。
- 数据不会越权或误删。
- 失败路径有明确错误提示。
- `git diff` 中没有无关环境产物。
