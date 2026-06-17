package api

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

const playbotRunSchemaVersion = "p4.7.5"

type playbotRunEvent struct {
	SchemaVersion  string         `json:"schema_version"`
	RunID          string         `json:"run_id"`
	RequestID      string         `json:"request_id,omitempty"`
	Seq            int64          `json:"seq"`
	Phase          string         `json:"phase"`
	Level          string         `json:"level,omitempty"`
	Message        string         `json:"message,omitempty"`
	VisibleMessage string         `json:"visible_message,omitempty"`
	Data           map[string]any `json:"data,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

type playbotRun struct {
	id          string
	ownerID     string
	events      []playbotRunEvent
	subscribers map[chan playbotRunEvent]struct{}
	terminal    bool
	finalResult any
	createdAt   time.Time
	expiresAt   time.Time
	nextSeq     int64
}

type playbotRunSubscription struct {
	backlog <-chan playbotRunEvent
	live    <-chan playbotRunEvent
	cancel  func()
	done    bool
}

type playbotRunHub struct {
	mu        sync.Mutex
	runs      map[string]*playbotRun
	ttl       time.Duration
	maxEvents int
}

func newPlaybotRunHub(ttl time.Duration, maxEvents int) *playbotRunHub {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if maxEvents <= 0 {
		maxEvents = 200
	}
	return &playbotRunHub{
		runs:      map[string]*playbotRun{},
		ttl:       ttl,
		maxEvents: maxEvents,
	}
}

func (h *playbotRunHub) start(ownerID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupLocked(time.Now().UTC())
	id := uuid.NewString()
	now := time.Now().UTC()
	h.runs[id] = &playbotRun{
		id:          id,
		ownerID:     ownerID,
		subscribers: map[chan playbotRunEvent]struct{}{},
		createdAt:   now,
		expiresAt:   now.Add(h.ttl),
	}
	return id
}

func (h *playbotRunHub) append(runID string, event playbotRunEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now().UTC()
	h.cleanupLocked(now)
	run, ok := h.runs[runID]
	if !ok {
		return
	}
	run.nextSeq++
	event.SchemaVersion = playbotRunSchemaVersion
	event.RunID = runID
	event.Seq = run.nextSeq
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	if event.Level == "" {
		event.Level = "info"
	}
	run.events = append(run.events, event)
	if len(run.events) > h.maxEvents {
		run.events = append([]playbotRunEvent(nil), run.events[len(run.events)-h.maxEvents:]...)
	}
	if event.Phase == "done" || event.Phase == "failed" {
		run.terminal = true
		if event.Data != nil {
			run.finalResult = event.Data["response"]
		}
		run.expiresAt = now.Add(h.ttl)
	}
	for ch := range run.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
	if run.terminal {
		for ch := range run.subscribers {
			close(ch)
			delete(run.subscribers, ch)
		}
	}
}

func (h *playbotRunHub) subscribe(runID string, ownerID string, afterSeq int64) (playbotRunSubscription, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupLocked(time.Now().UTC())
	run, ok := h.runs[runID]
	if !ok || !playbotRunOwnerMatches(run, ownerID) {
		return playbotRunSubscription{}, false
	}
	backlog := make(chan playbotRunEvent, len(run.events))
	for _, event := range run.events {
		if event.Seq > afterSeq {
			backlog <- event
		}
	}
	close(backlog)
	if run.terminal {
		return playbotRunSubscription{backlog: backlog, done: true}, true
	}
	live := make(chan playbotRunEvent, 32)
	run.subscribers[live] = struct{}{}
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if current, ok := h.runs[runID]; ok {
			if _, exists := current.subscribers[live]; exists {
				close(live)
				delete(current.subscribers, live)
			}
		}
	}
	return playbotRunSubscription{backlog: backlog, live: live, cancel: cancel}, true
}

func (h *playbotRunHub) result(runID string, ownerID string) (any, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cleanupLocked(time.Now().UTC())
	run, ok := h.runs[runID]
	if !ok || !playbotRunOwnerMatches(run, ownerID) {
		return nil, false
	}
	return run.finalResult, true
}

func playbotRunOwnerMatches(run *playbotRun, ownerID string) bool {
	if run == nil {
		return false
	}
	return run.ownerID == ownerID
}

func (h *playbotRunHub) cleanupLocked(now time.Time) {
	for id, run := range h.runs {
		if now.After(run.expiresAt) {
			for ch := range run.subscribers {
				close(ch)
			}
			delete(h.runs, id)
		}
	}
}
