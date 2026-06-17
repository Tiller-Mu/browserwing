package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/browserwing/browserwing/services/playbotagent"
)

func TestP475PlaybotRunHubReplaysEventsAfterSeq(t *testing.T) {
	hub := newPlaybotRunHub(time.Minute, 10)
	ownerID := "owner-a"
	runID := hub.start(ownerID)
	hub.append(runID, playbotRunEvent{Phase: "queued", Message: "queued"})
	hub.append(runID, playbotRunEvent{Phase: "llm_visible_output", VisibleMessage: "visible"})

	sub, ok := hub.subscribe(runID, ownerID, 1)
	if !ok {
		t.Fatalf("subscribe returned not found")
	}
	var backlog []playbotRunEvent
	for event := range sub.backlog {
		backlog = append(backlog, event)
	}
	if len(backlog) != 1 || backlog[0].Seq != 2 || backlog[0].Phase != "llm_visible_output" {
		t.Fatalf("backlog after seq 1 = %+v, want only seq 2 visible output", backlog)
	}
	hub.append(runID, playbotRunEvent{Phase: "done", Data: map[string]any{"response": map[string]any{"saved": true}}})
	live := <-sub.live
	if live.Seq != 3 || live.Phase != "done" {
		t.Fatalf("live event = %+v, want seq 3 done", live)
	}

	resub, ok := hub.subscribe(runID, ownerID, 2)
	if !ok {
		t.Fatalf("resubscribe returned not found")
	}
	var replay []playbotRunEvent
	for event := range resub.backlog {
		replay = append(replay, event)
	}
	if !resub.done || len(replay) != 1 || replay[0].Phase != "done" {
		t.Fatalf("terminal replay = done:%v events:%+v, want done event only", resub.done, replay)
	}
}

func TestP475SanitizePlaybotRunEventDataMasksCandidateStepValues(t *testing.T) {
	data := sanitizePlaybotRunEventData(map[string]any{
		"candidate_steps": []any{map[string]any{
			"action":         "fill",
			"target_summary": "API token input",
			"value_summary":  "sk-test-token-for-recording",
			"value":          "raw-recorded-token",
			"fill":           map[string]any{"value": "raw-recorded-token"},
			"reason":         "Use the recorded sandbox token",
		}},
		"debug": map[string]any{"prompt": "raw prompt"},
	}, nil)
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal sanitized event data: %v", err)
	}
	text := string(raw)
	for _, forbidden := range []string{"sk-test-token-for-recording", "raw-recorded-token", "debug", "raw prompt", "\"value\":", "\"fill\":"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized event data leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "已录制输入值") {
		t.Fatalf("sanitized event data should include masked input summary: %s", text)
	}
}

func TestP475AgentFailedProgressEventDoesNotTerminateRun(t *testing.T) {
	hub := newPlaybotRunHub(time.Minute, 10)
	ownerID := "owner-a"
	runID := hub.start(ownerID)
	hub.append(runID, playbotRunEventFromAgentEvent(playbotagent.Event{
		Phase:   "failed",
		Level:   "error",
		Message: "Blueprint compilation failed.",
		Data:    map[string]any{"code": "blueprint_unsupported_action"},
	}))

	sub, ok := hub.subscribe(runID, ownerID, 0)
	if !ok {
		t.Fatalf("subscribe returned not found")
	}
	if sub.done {
		t.Fatalf("agent failed progress event must not terminate the run")
	}
	var backlog []playbotRunEvent
	for event := range sub.backlog {
		backlog = append(backlog, event)
	}
	if len(backlog) != 1 || backlog[0].Phase != "agent_failed" {
		t.Fatalf("agent progress event = %+v, want single agent_failed event", backlog)
	}

	hub.append(runID, playbotRunEvent{
		Phase: "failed",
		Level: "error",
		Data: map[string]any{
			"response": map[string]any{"error": "blueprint_unsupported_action"},
		},
	})
	live := <-sub.live
	if live.Phase != "failed" {
		t.Fatalf("terminal event phase = %q, want failed", live.Phase)
	}
	result, ok := hub.result(runID, ownerID)
	if !ok {
		t.Fatalf("result returned not found")
	}
	if response, _ := result.(map[string]any); response["error"] != "blueprint_unsupported_action" {
		t.Fatalf("final result = %#v, want wrapped response", result)
	}
}

func TestP475PlaybotRunHubRejectsDifferentOwner(t *testing.T) {
	hub := newPlaybotRunHub(time.Minute, 10)
	runID := hub.start("owner-a")
	hub.append(runID, playbotRunEvent{Phase: "done", Data: map[string]any{
		"response": map[string]any{"saved": true},
	}})

	if _, ok := hub.subscribe(runID, "owner-b", 0); ok {
		t.Fatalf("different owner must not subscribe to a playbot run")
	}
	if result, ok := hub.result(runID, "owner-b"); ok || result != nil {
		t.Fatalf("different owner result = %#v, ok=%v; want not found", result, ok)
	}
	if _, ok := hub.subscribe(runID, "owner-a", 0); !ok {
		t.Fatalf("original owner should still be able to replay the run")
	}
}

func TestP475SanitizeAgentDisplayPayloadDropsRawModelOutputAndMasksValues(t *testing.T) {
	recordedValue := "sk-test-token-for-recording"
	secretValue := "backend-owned-llm-secret"
	localPath := `D:\dpProject\browserwing\profiles\state.json`
	result := map[string]any{
		"visible_summary": "Use " + recordedValue + " with " + secretValue + " from " + localPath,
		"model_output": map[string]any{
			"visible_message": "Model described " + recordedValue,
			"candidate_steps": []any{map[string]any{
				"action":         "fill",
				"target_summary": "token input " + recordedValue,
				"value_summary":  recordedValue,
				"value":          recordedValue,
				"reason":         "enter " + recordedValue,
			}},
			"assumptions": []any{"uses " + recordedValue},
			"risk_notes":  []any{"do not reveal " + secretValue + ` from C:\Users\contract-user\secret.txt`},
			"raw_context": map[string]any{"prompt": "raw prompt", "value": recordedValue},
		},
	}

	visibleSummary, modelOutput := sanitizeP475AgentDisplayPayload(result, p475RecordingSource{
		ActionTrace: []map[string]any{{"type": "input", "value": recordedValue}},
	}, playbotagent.SecretChannel{Value: secretValue})
	raw, err := json.Marshal(map[string]any{
		"visible_summary": visibleSummary,
		"model_output":    modelOutput,
	})
	if err != nil {
		t.Fatalf("marshal display payload: %v", err)
	}
	text := string(raw)
	for _, forbidden := range []string{recordedValue, secretValue, `C:\Users\`, "dpProject", "raw_context", "raw prompt", "\"value\":"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("display payload leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "已录制输入值") || !strings.Contains(text, "redacted") || !strings.Contains(text, "redacted-local-path") {
		t.Fatalf("display payload did not include expected masks: %s", text)
	}
}
