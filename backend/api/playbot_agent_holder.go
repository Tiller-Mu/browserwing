package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/browserwing/browserwing/services/playbotagent"
)

type playbotAgentHolder struct {
	mu     sync.RWMutex
	client any
}

type mapPlaybotAgentClient interface {
	Run(context.Context, map[string]any) (map[string]any, error)
}

type binaryPlaybotAgentClient interface {
	Run(context.Context, playbotagent.Job) (playbotagent.Result, error)
}

type binaryPlaybotAgentClientWithEvents interface {
	RunWithEvents(context.Context, playbotagent.Job, playbotagent.RunOptions) (playbotagent.Result, error)
}

type unavailablePlaybotAgentClient struct {
	err error
}

func (c unavailablePlaybotAgentClient) Run(context.Context, map[string]any) (map[string]any, error) {
	if c.err != nil {
		return nil, c.err
	}
	return nil, fmt.Errorf("playbot_agent_client_unavailable")
}

func newPlaybotAgentHolder(client any) *playbotAgentHolder {
	return &playbotAgentHolder{client: client}
}

func (h *Handler) ensurePlaybotAgentHolder() *playbotAgentHolder {
	if h.playbotAgent == nil {
		client, err := playbotagent.NewBinaryClient(playbotagent.BinaryClientOptions{
			SecretEnvName:   "BROWSERWING_PLAYBOT_LLM_API_KEY",
			RedactLocalPath: true,
		})
		if err != nil {
			h.playbotAgent = newPlaybotAgentHolder(unavailablePlaybotAgentClient{err: err})
			return h.playbotAgent
		}
		h.playbotAgent = newPlaybotAgentHolder(client)
	}
	return h.playbotAgent
}

func (h *Handler) SetPlaybotAgentClientForTest(client any) {
	h.ensurePlaybotAgentHolder().set(client)
}

func (h *playbotAgentHolder) set(client any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.client = client
}

func (h *playbotAgentHolder) run(ctx context.Context, job map[string]any, secret playbotagent.SecretChannel) (map[string]any, error) {
	return h.runWithEvents(ctx, job, secret, nil)
}

func (h *playbotAgentHolder) runWithEvents(ctx context.Context, job map[string]any, secret playbotagent.SecretChannel, sink func(playbotagent.Event)) (map[string]any, error) {
	h.mu.RLock()
	client := h.client
	h.mu.RUnlock()
	if client == nil {
		return nil, fmt.Errorf("playbot_agent_client_unavailable")
	}
	if typed, ok := client.(mapPlaybotAgentClient); ok {
		return typed.Run(ctx, job)
	}
	if typed, ok := client.(binaryPlaybotAgentClientWithEvents); ok {
		agentJob, err := mapToBinaryAgentJob(job)
		if err != nil {
			return nil, err
		}
		agentJob.SecretChannel = secret
		result, err := typed.RunWithEvents(ctx, agentJob, playbotagent.RunOptions{EventSink: sink})
		if err != nil {
			return nil, err
		}
		return binaryAgentResultToMap(result)
	}
	if typed, ok := client.(binaryPlaybotAgentClient); ok {
		agentJob, err := mapToBinaryAgentJob(job)
		if err != nil {
			return nil, err
		}
		agentJob.SecretChannel = secret
		result, err := typed.Run(ctx, agentJob)
		if err != nil {
			return nil, err
		}
		return binaryAgentResultToMap(result)
	}
	return nil, fmt.Errorf("playbot_agent_client_invalid")
}

func mapToBinaryAgentJob(job map[string]any) (playbotagent.Job, error) {
	data, err := json.Marshal(job)
	if err != nil {
		return playbotagent.Job{}, err
	}
	var out playbotagent.Job
	if err := json.Unmarshal(data, &out); err != nil {
		return playbotagent.Job{}, err
	}
	return out, nil
}

func binaryAgentResultToMap(result playbotagent.Result) (map[string]any, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}
