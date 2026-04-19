package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestExecute_UnknownSubcommand(t *testing.T) {
	if Execute([]string{"browserwing", "unknown-cmd"}) {
		t.Error("Execute should return false for unknown subcommands")
	}
}

func TestExecute_NoArgs(t *testing.T) {
	if Execute([]string{"browserwing"}) {
		t.Error("Execute should return false when no subcommand given")
	}
}

func TestExecute_Help(t *testing.T) {
	if !Execute([]string{"browserwing", "help"}) {
		t.Error("Execute should return true for help command")
	}
}

func TestGetBaseURL_Default(t *testing.T) {
	os.Unsetenv("BROWSERWING_URL")
	cliPort = ""
	got := getBaseURL()
	if !strings.HasPrefix(got, "http://localhost:") {
		t.Errorf("getBaseURL() = %q, want http://localhost:<port>", got)
	}
}

func TestGetBaseURL_Env(t *testing.T) {
	os.Setenv("BROWSERWING_URL", "http://custom:9090/")
	defer os.Unsetenv("BROWSERWING_URL")

	got := getBaseURL()
	if got != "http://custom:9090" {
		t.Errorf("getBaseURL() = %q, want %q", got, "http://custom:9090")
	}
}

func TestToRows_SliceOfMaps(t *testing.T) {
	input := []interface{}{
		map[string]interface{}{"name": "a", "score": 1},
		map[string]interface{}{"name": "b", "score": 2},
	}
	rows := toRows(input)
	if len(rows) != 2 {
		t.Fatalf("toRows returned %d rows, want 2", len(rows))
	}
	if rows[0]["name"] != "a" {
		t.Errorf("rows[0][name] = %v, want %v", rows[0]["name"], "a")
	}
}

func TestToRows_SingleMap(t *testing.T) {
	input := map[string]interface{}{"name": "single"}
	rows := toRows(input)
	if len(rows) != 1 {
		t.Fatalf("toRows returned %d rows, want 1", len(rows))
	}
}

func TestToRows_Nil(t *testing.T) {
	rows := toRows(nil)
	if rows != nil {
		t.Error("toRows(nil) should return nil")
	}
}

func TestGetKeys(t *testing.T) {
	m := map[string]interface{}{"b": 1, "a": 2, "c": 3}
	keys := getKeys(m)
	if len(keys) != 3 {
		t.Fatalf("getKeys returned %d keys, want 3", len(keys))
	}
}

func TestFormatTable(t *testing.T) {
	data := []interface{}{
		map[string]interface{}{"title": "Hello", "score": 100},
	}
	var buf bytes.Buffer
	formatTable(data, &buf)
	out := buf.String()
	if !strings.Contains(out, "Hello") || !strings.Contains(out, "100") {
		t.Errorf("formatTable output missing data: %s", out)
	}
}

func TestFormatTable_NoData(t *testing.T) {
	var buf bytes.Buffer
	formatTable(nil, &buf)
	if !strings.Contains(buf.String(), "(no data)") {
		t.Error("formatTable(nil) should output '(no data)'")
	}
}

func TestFormatCSV(t *testing.T) {
	data := []interface{}{
		map[string]interface{}{"name": "test", "value": "123"},
	}
	var buf bytes.Buffer
	formatCSV(data, &buf)
	out := buf.String()
	if !strings.Contains(out, "test") || !strings.Contains(out, "123") {
		t.Errorf("formatCSV output missing data: %s", out)
	}
}

func TestFindDisplayData_SingleKey(t *testing.T) {
	data := map[string]interface{}{
		"results": []interface{}{1, 2, 3},
	}
	result := findDisplayData(data)
	arr, ok := result.([]interface{})
	if !ok {
		t.Fatal("findDisplayData should unwrap single-key map")
	}
	if len(arr) != 3 {
		t.Errorf("got %d items, want 3", len(arr))
	}
}

func TestFindDisplayData_MultipleKeys(t *testing.T) {
	data := map[string]interface{}{
		"a": 1,
		"b": 2,
	}
	result := findDisplayData(data)
	m, ok := result.(map[string]interface{})
	if !ok {
		t.Fatal("findDisplayData should return the map as-is for multiple keys")
	}
	if len(m) != 2 {
		t.Errorf("got %d keys, want 2", len(m))
	}
}
