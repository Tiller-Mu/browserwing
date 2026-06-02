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

// GenerateTestPlan 调用 Python CLI 生成测试用例大纲
func GenerateTestPlan(ctx context.Context, opts GenerateOptions) (string, error) {
	// 1. 准备输入文件
	tmpFile, err := os.CreateTemp("", "playbot_input_*.json")
	if err != nil {
		return "", fmt.Errorf("create input file error: %w", err)
	}
	tmpFileName := tmpFile.Name()
	defer os.Remove(tmpFileName)

	jobData := map[string]interface{}{
		"page_url":         opts.PageURL,
		"snapshot":         opts.Snapshot,
		"intent_plan":      opts.IntentPlan,
		"page_description": opts.PageDescription,
		"instruction":      opts.Instruction,
	}

	data, err := json.Marshal(jobData)
	if err != nil {
		return "", fmt.Errorf("marshal input data error: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		return "", fmt.Errorf("write input file error: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return "", fmt.Errorf("close input file error: %w", err)
	}

	// 2. 组装命令
	pythonPath, err := resolvePythonPath(opts.PythonPath)
	if err != nil {
		return "", err
	}
	engineDir, err := resolveEngineDir(opts.EngineDir)
	if err != nil {
		return "", err
	}
	cliScript := filepath.Join(engineDir, "cli.py")
	args := []string{
		cliScript,
		"--input", tmpFileName,
		"--llm-endpoint", opts.LLMEndpoint,
		"--llm-api-key", opts.LLMAPIKey,
		"--llm-model", opts.LLMModel,
	}

	cmd := exec.CommandContext(ctx, pythonPath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 设置环境变量
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "PYTHONIOENCODING=utf-8")

	logInfo(ctx, "Running Playbot CLI: %s", redactedCommandString(pythonPath, args))

	if err := cmd.Run(); err != nil {
		logError(ctx, "Playbot CLI execution failed: %v\nStderr: %s", err, stderr.String())
		return "", fmt.Errorf("execution failed: %w, stderr: %s", err, stderr.String())
	}

	// stderr 包含了过程日志，可以根据需要打印
	if stderr.Len() > 0 {
		logInfo(ctx, "Playbot CLI stderr log:\n%s", stderr.String())
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
