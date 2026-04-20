---
name: browserwing-install
description: How to install, configure, and start BrowserWing, and how to set up Executor Skill and Admin Skill for AI agent integration.
---

# BrowserWing Installation & Setup Guide

This guide walks you through installing BrowserWing, configuring it, starting the service, and installing Skills so that AI agents can manage and use BrowserWing.

---

## 1. Install BrowserWing

Choose one of the following installation methods:

### Method A: One-Line Install Script (Recommended)

**Linux / macOS:**
```bash
curl -fsSL https://raw.githubusercontent.com/browserwing/browserwing/main/install.sh | bash
```

**Windows (PowerShell):**
```powershell
iwr -useb https://raw.githubusercontent.com/browserwing/browserwing/main/install.ps1 | iex
```

The script automatically detects your OS and architecture, downloads the latest release binary, and installs it to `~/.browserwing/`.

### Method B: Install via npm

```bash
npm install -g browserwing
```

### Method C: Download Binary Manually

Download the appropriate binary from: https://github.com/browserwing/browserwing/releases

| Platform | File |
|----------|------|
| Linux x64 | `browserwing-linux-amd64` |
| Linux ARM64 | `browserwing-linux-arm64` |
| macOS Intel | `browserwing-darwin-amd64` |
| macOS Apple Silicon | `browserwing-darwin-arm64` |
| Windows x64 | `browserwing-windows-amd64.exe` |
| Windows ARM64 | `browserwing-windows-arm64.exe` |

After downloading, make it executable (Linux/macOS):
```bash
chmod +x browserwing-linux-amd64
```

### Method D: Build from Source

Requires Go 1.21+ and Node.js 18+ with pnpm.

```bash
git clone https://github.com/browserwing/browserwing.git
cd browserwing

# Install dependencies
make install

# Build embedded version (frontend bundled into backend binary)
make build-embedded

# The binary is at: build/browserwing
```

---

## 2. Install Google Chrome (Required Dependency)

BrowserWing requires Google Chrome (or Chromium) for browser automation.

> **Windows & macOS users:** If you already have Google Chrome installed on your system (most people do), you can **skip this step**. BrowserWing will automatically detect Chrome in its standard installation path:
> - **Windows:** `C:\Program Files\Google\Chrome\Application\chrome.exe` or `C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`
> - **macOS:** `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`
>
> Only if Chrome is not found will you need to install it manually.

### Linux (Typically Needs Manual Install)

Linux servers usually don't have Chrome pre-installed. Install it with:

```bash
wget -q -O - https://dl-ssl.google.com/linux/linux_signing_key.pub | sudo apt-key add -
echo "deb [arch=amd64] http://dl.google.com/linux/chrome/deb/ stable main" | sudo tee /etc/apt/sources.list.d/google-chrome.list
sudo apt-get update
sudo apt-get install -y google-chrome-stable
```

### macOS (Only If Not Already Installed)
```bash
brew install --cask google-chrome
```

### Windows (Only If Not Already Installed)
Download and install from: https://www.google.com/chrome/

### Custom Chrome Path

If Chrome is installed in a non-standard location, tell BrowserWing where to find it via `config.toml` or environment variable:

```toml
# config.toml
[browser]
bin_path = "/path/to/your/chrome"
```

Or:
```bash
export CHROME_BIN_PATH="/path/to/your/chrome"
```

---

## 3. Configure BrowserWing

BrowserWing uses a `config.toml` file. If no config file exists, it uses sensible defaults and auto-detects Chrome.

### Create Config (Optional)

Create a `config.toml` in the same directory as the binary:

```toml
# Server settings
[server]
host = "0.0.0.0"
port = "8080"

# Database
[database]
path = "./data/browserwing.db"

# Browser settings
[browser]
bin_path = ""                    # Leave empty to auto-detect Chrome
user_data_dir = "./chrome_user_data"  # Stores login sessions, cookies, cache
# control_url = ""              # Set this to connect to a remote Chrome instance

# Logging
[log]
level = "info"
file = "./logs/browserwing.log"
```

### Key Configuration Options

| Setting | Description | Default |
|---------|-------------|---------|
| `server.port` | HTTP server port | `8080` |
| `browser.bin_path` | Chrome binary path (auto-detected if empty) | `""` |
| `browser.user_data_dir` | Chrome user data directory for persistent sessions | `./chrome_user_data` |
| `browser.control_url` | Remote Chrome DevTools URL (overrides local Chrome) | `""` |
| `log.file` | Log file path | `./logs/browserwing.log` |

### Environment Variables

- `CHROME_BIN_PATH` — Override Chrome binary location

---

## 4. Start BrowserWing

### Basic Start
```bash
./browserwing --port 8080
```

### With Custom Config
```bash
./browserwing --config ./config.toml --port 8080
```

### Verify the Service is Running

**Option A: Built-in Doctor Command (Recommended)**
```bash
browserwing doctor
```

This checks server connectivity, Chrome detection, script loading, system info, and config — all in one command.

**Option B: Manual Health Check**
```bash
curl http://localhost:8080/health
```

Expected response:
```json
{"status": "ok"}
```

### Access the Web UI

Open in your browser: http://localhost:8080

The Web UI provides a visual interface for managing scripts, browser instances, and AI features.

---

## 5. CLI Mode — Run Scripts from Terminal

BrowserWing includes a built-in CLI for running automation scripts directly from the terminal. This is designed for **AI agents** and **shell pipelines** — no browser window appears (headless by default).

### Basic Usage

```bash
# Show help
browserwing help

# List all available scripts (compact JSON for agents)
browserwing ls --format=json

# Run a built-in script (outputs JSON to stdout)
browserwing run bilibili-hot
browserwing run github-trending
browserwing run hackernews-top
```

### Passing Parameters

Some scripts accept parameters. Use `browserwing ls --format=json` to discover which scripts have a `params` field, then pass them as `--key=value`:

```bash
# Search JD.com for a product
browserwing run jd-search --keyword="机械键盘"

# Search Taobao
browserwing run taobao-search --keyword="蓝牙耳机"
```

### Output Formats

```bash
# JSON (default, best for AI agents and piping)
browserwing run bilibili-hot --format=json

# Table (human-readable)
browserwing run github-trending --format=table

# CSV (for spreadsheets)
browserwing run sinafinance-rank --format=csv > stocks.csv
```

### Options

| Flag | Description |
|------|-------------|
| `--format=json\|table\|csv` | Output format (default: json) |
| `--no-headless` | Show the browser window (for debugging) |
| `--port=<port>` | Server port (auto-detected from config.toml) |
| `--url=<url>` | Full server URL |
| `--<key>=<value>` | Pass parameters to the script |

### Built-in Scripts (30+)

BrowserWing ships with 30+ ready-to-use scripts covering popular platforms:

| Category | Scripts |
|----------|---------|
| **Tech** | github-trending, hackernews-top, v2ex-hot, stackoverflow-hot, linux-do-hot, juejin-hot |
| **Chinese Social** | bilibili-hot, zhihu-hot, weibo-hot, douyin-hot, tieba-hot, xiaohongshu-hot |
| **News** | 36kr-hot, toutiao-hot |
| **Finance** | sinafinance-rank, eastmoney-hot, xueqiu-hot |
| **Entertainment** | douban-movie-hot, douban-top250, imdb-trending, hupu-hot |
| **Shopping** | jd-search, taobao-search, smzdm-hot |
| **International** | reddit-popular, producthunt-hot, twitter-trending |
| **Jobs** | boss-recommend, linkedin-jobs |

Scripts marked with 🔐 require login — use the Web UI to log in first, and the CLI will reuse the session.

### Diagnostics

```bash
# Check everything is working
browserwing doctor
```

Output:
```
  [OK] Server running at http://localhost:8080
  [OK] Google Chrome 136.0.6935.0 (/usr/bin/google-chrome)
  [OK] 32 scripts loaded
  [OK] linux/amd64, Go go1.23.0
  [OK] config.toml found (port=8080)

  All checks passed. BrowserWing is ready.
```

### Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | General error |
| 2 | Server connection error |
| 3 | Script not found |
| 4 | Script execution failed |
| 64 | Bad arguments |

Set `BROWSERWING_JSON_ERRORS=1` for machine-readable JSON error output on stderr.

### AI Agent Integration Example

For AI agents that can invoke shell commands, the typical workflow is:

```bash
# Step 1: Discover available scripts
browserwing ls --format=json
# Returns: [{"id":"...","name":"bilibili-hot","description":"...","params":{...}}, ...]

# Step 2: Run a script
browserwing run bilibili-hot
# Returns: [{"rank":1,"title":"...","url":"...","play":"..."}, ...]

# Step 3: Parse and present the structured data to the user
```

The CLI auto-detects the server port from `config.toml`, so no manual configuration is needed.

---

## 6. Set Up LLM (Required for AI Features)

AI-powered features (AI Explorer, Agent chat, smart extraction) need an LLM configuration.

```bash
curl -X POST 'http://localhost:8080/api/v1/llm-configs' \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "my-llm",
    "provider": "openai",
    "api_key": "sk-your-api-key",
    "model": "gpt-4o",
    "base_url": "https://api.openai.com/v1",
    "is_active": true,
    "is_default": true
  }'
```

**Supported providers:** `openai`, `anthropic`, `deepseek`, or any OpenAI-compatible endpoint.

Test the connection:
```bash
curl -X POST 'http://localhost:8080/api/v1/llm-configs/test' \
  -H 'Content-Type: application/json' \
  -d '{"name": "my-llm"}'
```

---

## 7. Install Skills for AI Agent Integration

BrowserWing provides two types of Skills that can be installed into AI agents (e.g., Claude, Cursor, or any agent that supports SKILL.md files):

### Skill 1: Admin Skill (Full Platform Management)

The Admin Skill gives an AI agent the ability to manage BrowserWing end-to-end: configure LLM, create/edit/delete scripts, run AI exploration, execute scripts, export skills, and troubleshoot.

**Download via API (dynamic, uses your actual host):**
```bash
curl -o SKILL_ADMIN.md 'http://localhost:8080/api/v1/admin/export/skill'
```

**Or use the static file included in the repository:**
```bash
cp SKILL_ADMIN.md /path/to/your/agent/skills/
```

### Skill 2: Executor Skill (Browser Control API)

The Executor Skill gives an AI agent direct browser control capabilities: navigate pages, click elements, type text, extract data, take screenshots, run JavaScript, and more.

**Download via API:**
```bash
curl -o SKILL_EXECUTOR.md 'http://localhost:8080/api/v1/executor/export/skill'
```

**Or use the static file included in the repository:**
```bash
cp SKILL_EXECUTOR.md /path/to/your/agent/skills/
```

### Skill 3: Script Skill (Your Custom Scripts)

Export your automation scripts as a Skill so AI agents can discover and execute them:

**Export all scripts:**
```bash
curl -X POST 'http://localhost:8080/api/v1/scripts/export/skill' \
  -H 'Content-Type: application/json' \
  -d '{"script_ids": []}' \
  -o SKILL_SCRIPTS.md
```

**Export selected scripts:**
```bash
curl -X POST 'http://localhost:8080/api/v1/scripts/export/skill' \
  -H 'Content-Type: application/json' \
  -d '{"script_ids": ["script-id-1", "script-id-2"]}' \
  -o SKILL_SCRIPTS.md
```

### Where to Place Skill Files

Place the downloaded `.md` files into your AI agent's skill/knowledge directory:

| Agent | Skill Directory |
|-------|----------------|
| Cursor | Project root or `.cursor/skills/` directory |
| Claude Desktop | Upload as project knowledge |
| Custom Agent | Wherever your agent reads tool/skill definitions |

---

## 8. Quick Verification Checklist

After installation and setup, verify everything works:

```bash
# 1. Check service health
curl http://localhost:8080/health

# 2. Check if Chrome is accessible (browser auto-starts on first use)
curl http://localhost:8080/api/v1/browser/instances

# 3. List LLM configs (should show your configured LLM)
curl http://localhost:8080/api/v1/llm-configs

# 4. Test browser automation
curl -X POST 'http://localhost:8080/api/v1/executor/navigate' \
  -H 'Content-Type: application/json' \
  -d '{"url": "https://example.com"}'

# 5. Get page snapshot (verify browser is working)
curl http://localhost:8080/api/v1/executor/snapshot

# 6. List available scripts
curl http://localhost:8080/api/v1/scripts
```

If all checks pass, BrowserWing is fully operational and ready for AI agent integration.

---

## Troubleshooting

### Chrome won't start
```bash
# Check if Chrome is installed
google-chrome --version

# Check for stale lock files
ls -la ./chrome_user_data/SingletonLock 2>/dev/null
rm -f ./chrome_user_data/SingletonLock ./chrome_user_data/SingletonCookie ./chrome_user_data/SingletonSocket

# Kill lingering Chrome processes
pkill -f chrome
```

### Port already in use
```bash
# Check what's using port 8080
lsof -i :8080
# or
netstat -tlnp | grep 8080

# Use a different port
./browserwing --port 9090
```

### View logs
```bash
tail -f ./logs/browserwing.log
```

### Service not responding
```bash
# Check if process is running
ps aux | grep browserwing

# Restart the service
pkill browserwing
./browserwing --port 8080
```
