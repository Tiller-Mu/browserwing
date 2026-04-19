package browser

import (
	"testing"

	"github.com/browserwing/browserwing/models"
)

func TestEnsureReturn(t *testing.T) {
	tests := []struct {
		name string
		code string
		want string
	}{
		{
			name: "single expression without return",
			code: "document.title",
			want: "return document.title;",
		},
		{
			name: "single expression with trailing semicolon",
			code: "document.title;",
			want: "return document.title;",
		},
		{
			name: "already has return",
			code: "return document.title;",
			want: "return document.title;",
		},
		{
			name: "multiline - last line is expression",
			code: "const x = 1;\nx + 2",
			want: "const x = 1;\nreturn x + 2;",
		},
		{
			name: "multiline - last line already has return",
			code: "const x = 1;\nreturn x + 2;",
			want: "const x = 1;\nreturn x + 2;",
		},
		{
			name: "function call as last line",
			code: "function getData() { return 1; }\ngetData()",
			want: "function getData() { return 1; }\nreturn getData();",
		},
		{
			name: "await expression",
			code: "const r = await fetch('/api');\nawait r.json()",
			want: "const r = await fetch('/api');\nreturn await r.json();",
		},
		{
			name: "multiline return - last line is closing bracket",
			code: "const list = data.items;\nreturn JSON.stringify(list.map(v => ({\n  name: v.name,\n  url: v.url,\n})));",
			want: "const list = data.items;\nreturn JSON.stringify(list.map(v => ({\n  name: v.name,\n  url: v.url,\n})));",
		},
		{
			name: "return in nested function should still add outer return",
			code: "const fn = () => { return 1; };\nfn()",
			want: "const fn = () => { return 1; };\nreturn fn();",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureReturn(tt.code)
			if got != tt.want {
				t.Errorf("ensureReturn() =\n%s\nwant:\n%s", got, tt.want)
			}
		})
	}
}

func TestSubstituteActionVariables(t *testing.T) {
	p := &Player{}

	tests := []struct {
		name      string
		action    models.ScriptAction
		variables map[string]string
		checkFn   func(t *testing.T, result models.ScriptAction)
	}{
		{
			name: "substitute in Value field",
			action: models.ScriptAction{
				Type:  "input",
				Value: "Hello ${name}!",
			},
			variables: map[string]string{"name": "World"},
			checkFn: func(t *testing.T, result models.ScriptAction) {
				if result.Value != "Hello World!" {
					t.Errorf("Value = %q, want %q", result.Value, "Hello World!")
				}
			},
		},
		{
			name: "substitute in URL field",
			action: models.ScriptAction{
				Type: "navigate",
				URL:  "https://example.com/search?q=${keyword}",
			},
			variables: map[string]string{"keyword": "golang"},
			checkFn: func(t *testing.T, result models.ScriptAction) {
				if result.URL != "https://example.com/search?q=golang" {
					t.Errorf("URL = %q, want %q", result.URL, "https://example.com/search?q=golang")
				}
			},
		},
		{
			name: "substitute in Selector field",
			action: models.ScriptAction{
				Type:     "click",
				Selector: "#item-${item_id}",
			},
			variables: map[string]string{"item_id": "42"},
			checkFn: func(t *testing.T, result models.ScriptAction) {
				if result.Selector != "#item-42" {
					t.Errorf("Selector = %q, want %q", result.Selector, "#item-42")
				}
			},
		},
		{
			name: "substitute in XPath field",
			action: models.ScriptAction{
				Type:  "click",
				XPath: "//div[@data-id='${row_id}']",
			},
			variables: map[string]string{"row_id": "abc"},
			checkFn: func(t *testing.T, result models.ScriptAction) {
				if result.XPath != "//div[@data-id='abc']" {
					t.Errorf("XPath = %q, want %q", result.XPath, "//div[@data-id='abc']")
				}
			},
		},
		{
			name: "substitute in JSCode field",
			action: models.ScriptAction{
				Type:   "execute_js",
				JSCode: "return document.querySelector('${sel}').textContent;",
			},
			variables: map[string]string{"sel": ".title"},
			checkFn: func(t *testing.T, result models.ScriptAction) {
				want := "return document.querySelector('.title').textContent;"
				if result.JSCode != want {
					t.Errorf("JSCode = %q, want %q", result.JSCode, want)
				}
			},
		},
		{
			name: "substitute in AIControlPrompt",
			action: models.ScriptAction{
				Type:            "ai_control",
				AIControlPrompt: "Find the article titled ${title}",
			},
			variables: map[string]string{"title": "Hello"},
			checkFn: func(t *testing.T, result models.ScriptAction) {
				if result.AIControlPrompt != "Find the article titled Hello" {
					t.Errorf("AIControlPrompt = %q, want %q", result.AIControlPrompt, "Find the article titled Hello")
				}
			},
		},
		{
			name: "substitute in FilePaths",
			action: models.ScriptAction{
				Type:      "upload_file",
				FilePaths: []string{"/tmp/${filename}.png", "/data/${filename}.jpg"},
			},
			variables: map[string]string{"filename": "photo"},
			checkFn: func(t *testing.T, result models.ScriptAction) {
				if result.FilePaths[0] != "/tmp/photo.png" {
					t.Errorf("FilePaths[0] = %q, want %q", result.FilePaths[0], "/tmp/photo.png")
				}
				if result.FilePaths[1] != "/data/photo.jpg" {
					t.Errorf("FilePaths[1] = %q, want %q", result.FilePaths[1], "/data/photo.jpg")
				}
			},
		},
		{
			name: "multiple variables in one field",
			action: models.ScriptAction{
				Type:  "input",
				Value: "${greeting}, ${name}! Welcome to ${place}.",
			},
			variables: map[string]string{
				"greeting": "Hi",
				"name":     "Alice",
				"place":    "BrowserWing",
			},
			checkFn: func(t *testing.T, result models.ScriptAction) {
				want := "Hi, Alice! Welcome to BrowserWing."
				if result.Value != want {
					t.Errorf("Value = %q, want %q", result.Value, want)
				}
			},
		},
		{
			name: "no matching variable leaves placeholder intact",
			action: models.ScriptAction{
				Type:  "input",
				Value: "Hello ${unknown}!",
			},
			variables: map[string]string{"name": "World"},
			checkFn: func(t *testing.T, result models.ScriptAction) {
				if result.Value != "Hello ${unknown}!" {
					t.Errorf("Value = %q, want %q", result.Value, "Hello ${unknown}!")
				}
			},
		},
		{
			name: "empty variables map changes nothing",
			action: models.ScriptAction{
				Type:  "input",
				Value: "Hello ${name}!",
			},
			variables: map[string]string{},
			checkFn: func(t *testing.T, result models.ScriptAction) {
				if result.Value != "Hello ${name}!" {
					t.Errorf("Value = %q, want %q", result.Value, "Hello ${name}!")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := p.substituteActionVariables(tt.action, tt.variables)
			tt.checkFn(t, result)
		})
	}
}
