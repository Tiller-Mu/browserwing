# BrowserWing 测试指南

本文档覆盖自动化单元测试和人工验收测试两部分。

---

## 一、自动化单元测试

### 运行所有测试

```bash
cd backend
go test ./... -v
```

### 按模块运行

```bash
# CLI 模块（14 个用例）
go test ./cli/ -v

# 内置脚本模块（6 个用例）
go test ./builtin/ -v

# Player 模块（17 个用例：ensureReturn + substituteActionVariables）
go test ./services/browser/ -v

# 代理解析
go test ./services/browser/ -run TestParseProxyURL -v

# 图片下载器
go test ./pkg/downloader/ -v
```

### 测试覆盖范围

| 模块 | 文件 | 用例数 | 覆盖内容 |
|------|------|--------|----------|
| cli | `cli/cli_test.go` | 14 | Execute 路由、getBaseURL 环境变量、toRows 数据转换、getKeys、formatTable/CSV 输出、findDisplayData 单键/多键 |
| builtin | `builtin/scripts_test.go` | 6 | 首次加载、幂等性（不覆盖已修改脚本）、必填字段校验、ID 唯一性、MCP 名称唯一性、evaluate action 完整性 |
| player | `services/browser/player_test.go` | 17 | ensureReturn（7 个边界：无 return/有分号/已有 return/多行/函数调用/await）、substituteActionVariables（10 个场景：所有可替换字段 + 多变量 + 未匹配变量保留 + 空 map） |

---

## 二、人工验收测试

### 前置条件

1. 编译并启动 BrowserWing 服务：

```bash
cd backend
go build -o browserwing .
./browserwing
```

2. 确认服务启动成功，终端输出 `🚀 BrowserWing server started`

---

### 测试 1：CLI list 命令

**步骤：**

```bash
# 在新终端中执行
./browserwing list
./browserwing list --format=json
./browserwing list --format=csv
```

**预期：**
- `table` 格式：对齐的表格，含 ID / NAME / DESCRIPTION / STEPS 列
- `json` 格式：标准 JSON 数组
- `csv` 格式：逗号分隔，首行为表头
- 应包含 5 个内置脚本（bilibili-hot, zhihu-hot, weibo-hot, douban-movie-hot, hackernews-top）

---

### 测试 2：CLI run 命令

**步骤：**

```bash
# 运行内置 HackerNews 脚本（纯 API，不需要浏览器 UI）
./browserwing run hackernews-top --format=json

# 模糊名称匹配
./browserwing run bilibili --format=table

# 不存在的脚本
./browserwing run nonexistent
```

**预期：**
- hackernews-top 返回 20 条结构化数据，含 rank/title/score/author/url
- 模糊匹配自动找到 bilibili-hot
- 不存在的脚本输出错误提示并建议 `browserwing list`

---

### 测试 3：CLI help

**步骤：**

```bash
./browserwing help
```

**预期：**
- 输出使用说明，包含 run / list / help 子命令
- 包含 --format 和 --key=value 参数说明
- 包含 BROWSERWING_URL 环境变量说明

---

### 测试 4：内置脚本自动加载

**步骤：**

1. 删除数据库文件 `data/browserwing.db`
2. 启动服务
3. 观察终端日志

**预期：**
- 日志中出现 5 行 `✓ Loaded builtin script: xxx`
- Web UI 脚本管理页能看到"内置脚本"分组下的 5 个脚本

**幂等性验证：**

1. 重启服务
2. 观察日志

**预期：**
- 不再出现 `Loaded builtin script` 日志（已存在则跳过）

---

### 测试 5：evaluate 步骤（Web UI）

**步骤：**

1. 打开 Web UI → 脚本管理
2. 新建脚本
3. 添加步骤 → navigate → URL: `https://api.github.com/zen`
4. 添加步骤 → 高级 → **求值取数**
5. 填入代码：
   ```javascript
   const resp = await fetch('https://api.github.com/zen');
   const text = await resp.text();
   return text;
   ```
6. 变量名填 `zen_quote`
7. 回放脚本

**预期：**
- 执行成功
- 提取数据中包含 `zen_quote` 键，值为 GitHub 随机格言

---

### 测试 6：evaluate + 变量传递

**步骤：**

1. 创建脚本，Step 1:
   - 类型: navigate → URL: `https://www.baidu.com`
2. Step 2:
   - 类型: extract_text → 选择器: `title` → 变量名: `page_title`
3. Step 3:
   - 类型: evaluate → JS 代码: `return "Title is: ${page_title}";`
   - 注意：这里 `${page_title}` 会在执行前被替换为 Step 2 提取的值
   - 变量名: `combined`
4. 回放

**预期：**
- `combined` 的值应为 `Title is: 百度一下，你就知道`（或类似）

---

### 测试 7：CLI 传参覆盖变量

**步骤：**

1. 在 Web UI 创建脚本，名称 `test-vars`
2. 预设变量：`keyword` = `default`
3. 添加步骤：navigate → URL: `https://www.baidu.com/s?wd=${keyword}`
4. 保存

```bash
# 使用默认变量
./browserwing run test-vars

# 覆盖变量
./browserwing run test-vars --keyword=browserwing
```

**预期：**
- 默认运行导航到 `baidu.com/s?wd=default`
- 传参后导航到 `baidu.com/s?wd=browserwing`

---

### 测试 8：结构化输出格式

**步骤：**

```bash
# 表格（默认）
./browserwing run hackernews-top

# JSON（AI Agent 友好）
./browserwing run hackernews-top --format=json | python3 -m json.tool

# CSV（可导入 Excel）
./browserwing run hackernews-top --format=csv > /tmp/hn.csv
cat /tmp/hn.csv
```

**预期：**
- table: 对齐的列，含分隔线
- json: 可被 `python3 -m json.tool` 正确解析
- csv: 首行为 header，数据行逗号分隔

---

### 测试 9：MCP 工具注册

**步骤：**

1. 启动服务
2. 使用 MCP 客户端（如 Claude Desktop）连接 BrowserWing MCP
3. 查看可用工具列表

**预期：**
- 工具列表中包含 `bilibili_hot`、`zhihu_hot`、`weibo_hot`、`douban_movie_hot`、`hackernews_top`
- 每个工具有对应的中/英文描述

---

### 测试 10：BROWSERWING_URL 环境变量

**步骤：**

```bash
# 服务运行在默认端口
./browserwing list

# 指定自定义地址
BROWSERWING_URL=http://localhost:18050 ./browserwing list

# 错误地址
BROWSERWING_URL=http://localhost:19999 ./browserwing list
```

**预期：**
- 默认和正确地址：正常输出脚本列表
- 错误地址：输出连接失败错误信息

---

## 三、快速回归检查清单

用于版本发布前的快速验证：

- [ ] `go test ./... -v` 全部通过
- [ ] `./browserwing help` 输出正常
- [ ] `./browserwing list` 包含 5 个内置脚本
- [ ] `./browserwing run hackernews-top --format=json` 返回数据
- [ ] Web UI 能看到内置脚本分组
- [ ] 添加 evaluate 步骤后能正常回放
- [ ] 重启服务不会重复创建内置脚本
