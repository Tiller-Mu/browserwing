package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Request struct {
	Endpoint string
	Model    string
	APIKey   string
	Timeout  time.Duration
	System   string
	User     string
}

type Response struct {
	VisibleMessage string          `json:"visible_message"`
	CandidateSteps []CandidateStep `json:"candidate_steps,omitempty"`
	Assumptions    []string        `json:"assumptions,omitempty"`
	RiskNotes      []string        `json:"risk_notes,omitempty"`
	SemanticPlan   json.RawMessage `json:"semantic_plan,omitempty"`
	Raw            map[string]any  `json:"-"`
}

type CandidateStep struct {
	Action        string `json:"action,omitempty"`
	TargetSummary string `json:"target_summary,omitempty"`
	ValueSummary  string `json:"value_summary,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

type Client interface {
	GenerateVisibleOutput(context.Context, Request) (Response, error)
}

type OpenAICompatibleClient struct {
	HTTPClient *http.Client
}

func NewOpenAICompatibleClient() *OpenAICompatibleClient {
	return &OpenAICompatibleClient{}
}

func (c *OpenAICompatibleClient) GenerateVisibleOutput(ctx context.Context, req Request) (Response, error) {
	endpoint := chatCompletionsURL(req.Endpoint)
	if strings.TrimSpace(endpoint) == "" {
		return Response{}, fmt.Errorf("playbot_llm_endpoint_required")
	}
	if strings.TrimSpace(req.Model) == "" {
		return Response{}, fmt.Errorf("playbot_llm_model_required")
	}
	if strings.TrimSpace(req.APIKey) == "" {
		return Response{}, fmt.Errorf("playbot_llm_secret_required")
	}

	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	body := map[string]any{
		"model": req.Model,
		"messages": []map[string]string{
			{"role": "system", "content": req.System},
			{"role": "user", "content": req.User},
		},
		"temperature": 0.2,
		"response_format": map[string]string{
			"type": "json_object",
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.APIKey)
	resp, err := client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("playbot_llm_request_failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Response{}, fmt.Errorf("playbot_llm_response_read_failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("playbot_llm_request_failed")
	}
	content, err := extractChatCompletionContent(data)
	if err != nil {
		return Response{}, err
	}
	var out Response
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return Response{}, fmt.Errorf("playbot_llm_output_invalid")
	}
	var rawOut map[string]any
	_ = json.Unmarshal([]byte(content), &rawOut)
	out.Raw = rawOut
	if strings.TrimSpace(out.VisibleMessage) == "" {
		return Response{}, fmt.Errorf("playbot_llm_visible_message_required")
	}
	return out, nil
}

func ResolveAPIKey(envName string) string {
	envName = strings.TrimSpace(envName)
	if envName == "" {
		envName = "BROWSERWING_PLAYBOT_LLM_API_KEY"
	}
	return os.Getenv(envName)
}

func chatCompletionsURL(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return ""
	}
	if strings.HasSuffix(endpoint, "/chat/completions") {
		return endpoint
	}
	return endpoint + "/chat/completions"
}

func extractChatCompletionContent(data []byte) (string, error) {
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("playbot_llm_response_invalid")
	}
	if len(payload.Choices) == 0 || strings.TrimSpace(payload.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("playbot_llm_response_invalid")
	}
	return payload.Choices[0].Message.Content, nil
}
