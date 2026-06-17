package api

import (
	"context"
	"strings"
	"testing"

	"github.com/browserwing/browserwing/services/playbotagent"
)

type p475HolderFakePlaybotAgent struct{}

func (p475HolderFakePlaybotAgent) Run(context.Context, map[string]any) (map[string]any, error) {
	return map[string]any{
		"schema_version": "p4.7.5",
		"status":         "success",
	}, nil
}

func TestP475PlaybotAgentHolderPreservesBinaryClientInitializationError(t *testing.T) {
	t.Setenv("BROWSERWING_PLAYBOT_AGENT_PATH", "python")

	handler := &Handler{}
	holder := handler.ensurePlaybotAgentHolder()

	_, err := holder.run(context.Background(), map[string]any{
		"schema_version": "p4.7.5",
		"mode":           "generate",
	}, playbotagent.SecretChannel{})
	if err == nil || !strings.Contains(err.Error(), "playbot_agent_python_engine_forbidden") {
		t.Fatalf("holder run error = %v, want playbot_agent_python_engine_forbidden", err)
	}
	if strings.Contains(err.Error(), "playbot_agent_client_unavailable") {
		t.Fatalf("holder run error = %v, must preserve binary client initialization error", err)
	}

	handler.SetPlaybotAgentClientForTest(p475HolderFakePlaybotAgent{})
	result, err := holder.run(context.Background(), map[string]any{
		"schema_version": "p4.7.5",
		"mode":           "generate",
	}, playbotagent.SecretChannel{})
	if err != nil {
		t.Fatalf("holder run after test override: %v", err)
	}
	if result["status"] != "success" {
		t.Fatalf("holder run status = %v, want success", result["status"])
	}
}
