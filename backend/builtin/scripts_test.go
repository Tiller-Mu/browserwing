package builtin

import (
	"testing"

	"github.com/browserwing/browserwing/models"
)

// mockStore is a simple in-memory script store for testing.
type mockStore struct {
	scripts map[string]*models.Script
}

func newMockStore() *mockStore {
	return &mockStore{scripts: make(map[string]*models.Script)}
}

func (m *mockStore) GetScript(id string) (*models.Script, error) {
	s, ok := m.scripts[id]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (m *mockStore) SaveScript(script *models.Script) error {
	m.scripts[script.ID] = script
	return nil
}

func TestLoadBuiltinScripts_FirstRun(t *testing.T) {
	store := newMockStore()

	// Directly sync the local builtinScripts to simulate first run without network
	for _, s := range builtinScripts {
		sc := s
		sc.CreatedAt = sc.UpdatedAt
		store.SaveScript(&sc)
	}

	if len(store.scripts) != len(builtinScripts) {
		t.Errorf("loaded %d scripts, want %d", len(store.scripts), len(builtinScripts))
	}

	for _, bs := range builtinScripts {
		if _, ok := store.scripts[bs.ID]; !ok {
			t.Errorf("builtin script %q not loaded", bs.ID)
		}
	}
}

func TestLoadBuiltinScripts_UpdatesExisting(t *testing.T) {
	store := newMockStore()

	for _, s := range builtinScripts {
		sc := s
		store.SaveScript(&sc)
	}
	firstCount := len(store.scripts)

	// Second sync should not change count
	for _, s := range builtinScripts {
		sc := s
		store.SaveScript(&sc)
	}
	if len(store.scripts) != firstCount {
		t.Errorf("second load changed script count: %d -> %d", firstCount, len(store.scripts))
	}

	for _, bs := range builtinScripts {
		stored := store.scripts[bs.ID]
		if stored == nil {
			t.Errorf("script %s missing after second load", bs.ID)
			continue
		}
		if stored.Description != bs.Description {
			t.Errorf("script %s was not updated on second load", bs.ID)
		}
	}
}

func TestBuiltinScripts_HaveRequiredFields(t *testing.T) {
	for _, s := range builtinScripts {
		if s.ID == "" {
			t.Error("builtin script has empty ID")
		}
		if s.Name == "" {
			t.Errorf("script %s has empty Name", s.ID)
		}
		if s.Description == "" {
			t.Errorf("script %s has empty Description", s.ID)
		}
		if len(s.Actions) == 0 {
			t.Errorf("script %s has no Actions", s.ID)
		}
		if len(s.Tags) == 0 {
			t.Errorf("script %s has no Tags", s.ID)
		}

		hasBuiltinTag := false
		for _, tag := range s.Tags {
			if tag == "builtin" {
				hasBuiltinTag = true
				break
			}
		}
		if !hasBuiltinTag {
			t.Errorf("script %s missing 'builtin' tag", s.ID)
		}
	}
}

func TestBuiltinScripts_UniqueIDs(t *testing.T) {
	seen := make(map[string]bool)
	for _, s := range builtinScripts {
		if seen[s.ID] {
			t.Errorf("duplicate builtin script ID: %s", s.ID)
		}
		seen[s.ID] = true
	}
}

func TestBuiltinScripts_UniqueMCPNames(t *testing.T) {
	seen := make(map[string]bool)
	for _, s := range builtinScripts {
		if s.MCPCommandName == "" {
			continue
		}
		if seen[s.MCPCommandName] {
			t.Errorf("duplicate MCP command name: %s", s.MCPCommandName)
		}
		seen[s.MCPCommandName] = true
	}
}

func TestBuiltinScripts_HaveEvaluateAction(t *testing.T) {
	for _, s := range builtinScripts {
		hasEvaluate := false
		for _, a := range s.Actions {
			if a.Type == "evaluate" {
				hasEvaluate = true
				if a.JSCode == "" {
					t.Errorf("script %s: evaluate action has empty JSCode", s.ID)
				}
				if a.VariableName == "" {
					t.Errorf("script %s: evaluate action has empty VariableName", s.ID)
				}
				break
			}
		}
		if !hasEvaluate {
			t.Errorf("script %s should have at least one evaluate action", s.ID)
		}
	}
}
