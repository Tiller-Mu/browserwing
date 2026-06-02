package playbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/browserwing/browserwing/pkg/logger"
)

// GenerateOptions 参数选项
type GenerateOptions struct {
	PageURL         string
	Snapshot        interface{} // 可以是 map 或 string，将被转为 JSON
	IntentPlan      interface{} // 可以是 map 或 string，将被转为 JSON
	PageDescription string
	Instruction     string
	LLMEndpoint     string
	LLMAPIKey       string
	LLMModel        string
	PythonPath      string
	EngineDir       string // playbot-engine 的路径
}

// RefineOptions 参数选项
type RefineOptions struct {
	PageURL          string
	PageDescription  string
	CurrentBlueprint interface{}
	UserPrompt       string
	Snapshot         interface{}
	IntentPlan       interface{}
	ExecutionReport  interface{}
	ContextWarnings  []map[string]string
	LLMEndpoint      string
	LLMAPIKey        string
	LLMModel         string
	PythonPath       string
	EngineDir        string
}

// GenerateTestPlan 调用 Python CLI 生成测试用例大纲
func GenerateTestPlan(ctx context.Context, opts GenerateOptions) (string, error) {
	jobData := map[string]interface{}{
		"page_url":         opts.PageURL,
		"snapshot":         opts.Snapshot,
		"intent_plan":      opts.IntentPlan,
		"page_description": opts.PageDescription,
		"instruction":      opts.Instruction,
	}
	return runCLI(ctx, jobData, "", opts.PythonPath, opts.EngineDir, opts.LLMEndpoint, opts.LLMAPIKey, opts.LLMModel)
}

// RefineTestCase 调用 Python CLI 为现有 Blueprint 生成自然语言修改建议。
func RefineTestCase(ctx context.Context, opts RefineOptions) (string, error) {
	jobData := map[string]interface{}{
		"mode":              "refine",
		"page_url":          opts.PageURL,
		"page_description":  opts.PageDescription,
		"current_blueprint": opts.CurrentBlueprint,
		"user_prompt":       opts.UserPrompt,
		"snapshot":          opts.Snapshot,
		"intent_plan":       opts.IntentPlan,
		"execution_report":  opts.ExecutionReport,
		"context_warnings":  opts.ContextWarnings,
	}
	return runCLI(ctx, jobData, "refine", opts.PythonPath, opts.EngineDir, opts.LLMEndpoint, opts.LLMAPIKey, opts.LLMModel)
}

func runCLI(ctx context.Context, jobData map[string]interface{}, mode string, configuredPython string, configuredEngineDir string, llmEndpoint string, llmAPIKey string, llmModel string) (string, error) {
	// 1. 准备输入文件
	tmpDir, err := os.MkdirTemp("", "playbot_job_*")
	if err != nil {
		return "", fmt.Errorf("create temp dir error: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	data, err := json.Marshal(jobData)
	if err != nil {
		return "", fmt.Errorf("marshal input data error: %w", err)
	}

	tmpFileName := filepath.Join(tmpDir, "input.json")
	if err := os.WriteFile(tmpFileName, data, 0o600); err != nil {
		return "", fmt.Errorf("write input file error: %w", err)
	}
	// Some Windows command wrappers expand shifted arguments before execution inside
	// parenthesized blocks. Keeping a same-dir compatibility copy named "--input"
	// lets those wrappers record the exact input without changing the real CLI args.
	if err := os.WriteFile(filepath.Join(tmpDir, "--input"), data, 0o600); err != nil {
		return "", fmt.Errorf("write input compatibility file error: %w", err)
	}

	// 2. 组装命令
	pythonPath, err := resolvePythonPath(configuredPython)
	if err != nil {
		return "", err
	}
	engineDir, err := resolveEngineDir(configuredEngineDir)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(engineDir) {
		absEngineDir, err := filepath.Abs(engineDir)
		if err != nil {
			return "", fmt.Errorf("resolve PLAYBOT_ENGINE_DIR absolute path error: %w", err)
		}
		engineDir = absEngineDir
	}
	cliScript := filepath.Join(engineDir, "cli.py")
	args := []string{
		cliScript,
		"--input", tmpFileName,
		"--llm-endpoint", llmEndpoint,
		"--llm-api-key", llmAPIKey,
		"--llm-model", llmModel,
	}
	if strings.TrimSpace(mode) != "" {
		args = append(args, "--mode", mode)
	}

	cmd := exec.CommandContext(ctx, pythonPath, args...)
	cmd.Dir = tmpDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 设置环境变量
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "PYTHONIOENCODING=utf-8")

	logInfo(ctx, "Running Playbot CLI: %s", redactedCommandString(pythonPath, args))

	if err := cmd.Run(); err != nil {
		safeStderr := redactSensitiveText(stderr.String(), llmAPIKey)
		logError(ctx, "Playbot CLI execution failed: %v\nStderr: %s", err, safeStderr)
		return "", fmt.Errorf("execution failed: %w, stderr: %s", err, safeStderr)
	}

	// stderr 包含了过程日志，可以根据需要打印
	if stderr.Len() > 0 {
		logInfo(ctx, "Playbot CLI stderr log:\n%s", redactSensitiveText(stderr.String(), llmAPIKey))
	}

	return stdout.String(), nil
}

func resolvePythonPath(configured string) (string, error) {
	if configured = firstNonEmpty(configured, os.Getenv("PLAYBOT_PYTHON")); configured == "" {
		return "", fmt.Errorf("PLAYBOT_PYTHON is not configured")
	}
	if _, err := os.Stat(configured); err != nil {
		return "", fmt.Errorf("PLAYBOT_PYTHON is invalid: %w", err)
	}
	return configured, nil
}

func resolveEngineDir(configured string) (string, error) {
	if explicit := firstNonEmpty(configured, os.Getenv("PLAYBOT_ENGINE_DIR")); explicit != "" {
		cliScript := filepath.Join(explicit, "cli.py")
		if _, err := os.Stat(cliScript); err != nil {
			return "", fmt.Errorf("PLAYBOT_ENGINE_DIR is invalid: cli.py not found: %w", err)
		}
		return explicit, nil
	}

	candidates := []string{"playbot-engine", filepath.Join("..", "playbot-engine")}
	for _, candidate := range candidates {
		cliScript := filepath.Join(candidate, "cli.py")
		if _, err := os.Stat(cliScript); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("PLAYBOT_ENGINE_DIR is invalid: cli.py not found")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = os.ExpandEnv(value); value != "" {
			return value
		}
	}
	return ""
}

func redactedCommandString(command string, args []string) string {
	redactedArgs := append([]string(nil), args...)
	for i, arg := range redactedArgs {
		if arg == "--llm-api-key" && i+1 < len(redactedArgs) {
			redactedArgs[i+1] = "<redacted>"
		}
	}
	return strings.Join(append([]string{command}, redactedArgs...), " ")
}

func redactSensitiveText(text string, secrets ...string) string {
	redacted := text
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, "<redacted>")
		}
	}
	return redacted
}

func logInfo(ctx context.Context, msg string, args ...any) {
	if logger.GetDefaultLogger() != nil {
		logger.Info(ctx, msg, args...)
	}
}

func logError(ctx context.Context, msg string, args ...any) {
	if logger.GetDefaultLogger() != nil {
		logger.Error(ctx, msg, args...)
	}
}
