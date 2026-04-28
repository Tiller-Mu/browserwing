package playbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/browserwing/browserwing/pkg/logger"
)

// GenerateOptions 参数选项
type GenerateOptions struct {
	PageURL      string
	Snapshot     interface{} // 可以是 map 或 string，将被转为 JSON
	IntentPlan   interface{} // 可以是 map 或 string，将被转为 JSON
	LLMEndpoint  string
	LLMAPIKey    string
	LLMModel     string
	EngineDir    string // playbot-engine 的路径
}

// GenerateTestPlan 调用 Python CLI 生成测试用例大纲
func GenerateTestPlan(ctx context.Context, opts GenerateOptions) (string, error) {
	// 1. 准备输入文件
	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, fmt.Sprintf("playbot_input_%d.json", os.Getpid()))
	
	jobData := map[string]interface{}{
		"page_url":    opts.PageURL,
		"snapshot":    opts.Snapshot,
		"intent_plan": opts.IntentPlan,
	}
	
	data, err := json.Marshal(jobData)
	if err != nil {
		return "", fmt.Errorf("marshal input data error: %w", err)
	}
	
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return "", fmt.Errorf("write input file error: %w", err)
	}
	defer os.Remove(tmpFile)

	// 2. 组装命令
	cliScript := filepath.Join(opts.EngineDir, "cli.py")
	args := []string{
		cliScript,
		"--input", tmpFile,
		"--llm-endpoint", opts.LLMEndpoint,
		"--llm-api-key", opts.LLMAPIKey,
		"--llm-model", opts.LLMModel,
	}

	cmd := exec.CommandContext(ctx, "python", args...)
	
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	
	// 设置环境变量
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "PYTHONIOENCODING=utf-8")

	logger.Info(ctx, "Running Playbot CLI: %s", cmd.String())
	
	if err := cmd.Run(); err != nil {
		logger.Error(ctx, "Playbot CLI execution failed: %v\nStderr: %s", err, stderr.String())
		return "", fmt.Errorf("execution failed: %w, stderr: %s", err, stderr.String())
	}
	
	// stderr 包含了过程日志，可以根据需要打印
	if stderr.Len() > 0 {
		logger.Info(ctx, "Playbot CLI stderr log:\n%s", stderr.String())
	}

	return stdout.String(), nil
}
