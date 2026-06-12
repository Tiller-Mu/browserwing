package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// P4.6 contract source:
// docs/P4_6_POSTGRES_STORAGE_MIGRATION_DESIGN.md requires a single PostgreSQL
// Store, an operation inventory, removal of BoltDB/SQLite production entrances,
// and PostgreSQL PlayBot configuration with a separate LLM API key encryption key.

func TestP46StoreOperationInventoryMatchesStoreInterface(t *testing.T) {
	root := backendRoot(t)
	storeMethods := parseInterfaceMethods(t, filepath.Join(root, "storage"), "Store")
	if len(storeMethods) == 0 {
		t.Fatalf("storage.Store interface not found or has no methods; P4.6 requires production modules to depend on a unified Store interface")
	}

	inventory := parseStoreOperationInventory(t, root)
	if len(inventory.entries) == 0 {
		t.Fatalf("Store Operation Inventory entries not found; P4.6 requires every Store method to declare method, legacy source, postgres target, and contract test")
	}

	var missing []string
	for method := range storeMethods {
		if _, ok := inventory.entries[method]; !ok {
			missing = append(missing, method)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("Store methods missing from operation inventory: %s", strings.Join(sortedStrings(missing), ", "))
	}

	var extra []string
	for method := range inventory.entries {
		if _, ok := storeMethods[method]; !ok {
			extra = append(extra, method)
		}
	}
	if len(extra) > 0 {
		t.Fatalf("Store Operation Inventory records methods not present on storage.Store: %s", strings.Join(sortedStrings(extra), ", "))
	}
}

func TestP46StoreOperationInventoryHasContractTestNames(t *testing.T) {
	root := backendRoot(t)
	inventory := parseStoreOperationInventory(t, root)
	if len(inventory.entries) == 0 {
		t.Fatalf("Store Operation Inventory entries not found")
	}
	contractTests := parseP46ContractTestFunctions(t, root)

	var bad []string
	for method, entry := range inventory.entries {
		contractTest := strings.TrimSpace(entry.ContractTest)
		if contractTest == "" {
			bad = append(bad, method+" missing ContractTest")
			continue
		}
		if !strings.HasPrefix(contractTest, "TestP46") {
			bad = append(bad, method+" has non-P4.6 ContractTest "+contractTest)
		}
		if _, ok := contractTests[contractTest]; !ok {
			bad = append(bad, method+" references missing ContractTest "+contractTest)
		}
		if strings.TrimSpace(entry.Domain) == "" {
			bad = append(bad, method+" missing Domain")
		}
		if strings.TrimSpace(entry.LegacySource) == "" {
			bad = append(bad, method+" missing LegacySource")
		}
		if strings.TrimSpace(entry.PostgresTarget) == "" {
			bad = append(bad, method+" missing PostgresTarget")
		}
	}
	if len(bad) > 0 {
		t.Fatalf("invalid Store Operation Inventory entries:\n%s", strings.Join(sortedStrings(bad), "\n"))
	}
}

func TestP46LegacyBoltMethodsAreCoveredByStoreInventory(t *testing.T) {
	root := backendRoot(t)
	legacyMethods := p46LegacyBoltMethodBaseline()
	storeMethods := parseInterfaceMethods(t, filepath.Join(root, "storage"), "Store")
	inventory := parseStoreOperationInventory(t, root)

	var missing []string
	for method := range legacyMethods {
		if _, ok := storeMethods[method]; !ok {
			missing = append(missing, method+" missing from storage.Store")
		}
		if _, ok := inventory.entries[method]; !ok {
			missing = append(missing, method+" missing from Store Operation Inventory")
		}
	}
	if len(missing) > 0 {
		t.Fatalf("legacy BoltDB operations not covered by P4.6 Store/Inventory:\n%s", strings.Join(sortedStrings(missing), "\n"))
	}
}

func TestP46ProductionCodeDoesNotUseBoltDBOrSQLite(t *testing.T) {
	root := backendRoot(t)
	var violations []string

	for _, file := range productionGoFiles(t, root) {
		parsed := parseGoFile(t, file)
		rel := relPath(t, root, file)

		for _, imported := range parsed.Imports {
			pathValue, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", rel, err)
			}
			switch pathValue {
			case "go.etcd.io/bbolt":
				violations = append(violations, rel+": imports go.etcd.io/bbolt")
			case "github.com/glebarez/sqlite":
				violations = append(violations, rel+": imports github.com/glebarez/sqlite")
			}
		}

		ast.Inspect(parsed, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.SelectorExpr:
				if ident, ok := n.X.(*ast.Ident); ok && ident.Name == "storage" {
					switch n.Sel.Name {
					case "NewBoltDB", "InitSQLite", "DB", "BoltDB":
						violations = append(violations, rel+": references storage."+n.Sel.Name)
					}
				}
			case *ast.ValueSpec:
				if rel == filepath.ToSlash(filepath.Join("storage", "sqlite.go")) {
					for _, name := range n.Names {
						if name.Name == "DB" {
							violations = append(violations, rel+": declares global storage.DB")
						}
					}
				}
			}
			return true
		})
	}

	if len(violations) > 0 {
		t.Fatalf("P4.6 forbids production BoltDB/SQLite/global storage.DB entrances; found %d violations:\n%s", len(violations), firstLines(sortedStrings(violations), 80))
	}
}

func TestP46MainInitializesOnlyPostgresStore(t *testing.T) {
	root := backendRoot(t)
	mainFile := filepath.Join(root, "main.go")
	parsed := parseGoFile(t, mainFile)

	var oldEntrances []string
	var sawPostgresInitCall bool
	ast.Inspect(parsed, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if ident, ok := n.X.(*ast.Ident); ok && ident.Name == "storage" {
				switch n.Sel.Name {
				case "NewBoltDB", "InitSQLite", "DB", "BoltDB":
					oldEntrances = append(oldEntrances, "storage."+n.Sel.Name)
				case "NewPostgresStore", "InitPostgres", "OpenPostgres", "NewPostgresGorm":
					sawPostgresInitCall = true
				case "NewStore":
					sawPostgresInitCall = true
				}
			}
		}
		return true
	})

	if len(oldEntrances) > 0 {
		t.Fatalf("main.go must not initialize or type against old stores; found: %s", strings.Join(sortedStrings(oldEntrances), ", "))
	}
	if !sawPostgresInitCall {
		t.Fatalf("main.go must explicitly initialize the PostgreSQL Store startup path through storage.NewPostgresStore, InitPostgres, OpenPostgres, NewPostgresGorm, or NewStore")
	}
}

func TestP46SDKDoesNotOpenBoltDB(t *testing.T) {
	root := backendRoot(t)
	sdkRoot := filepath.Join(root, "sdk")
	var violations []string

	for _, file := range productionGoFiles(t, sdkRoot) {
		parsed := parseGoFile(t, file)
		rel := relPath(t, root, file)
		for _, imported := range parsed.Imports {
			pathValue, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", rel, err)
			}
			if pathValue == "go.etcd.io/bbolt" {
				violations = append(violations, rel+": imports go.etcd.io/bbolt")
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if sel, ok := node.(*ast.SelectorExpr); ok {
				if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "storage" {
					switch sel.Sel.Name {
					case "NewBoltDB", "BoltDB":
						violations = append(violations, rel+": references storage."+sel.Sel.Name)
					}
				}
			}
			return true
		})
	}

	if len(violations) > 0 {
		t.Fatalf("P4.6 forbids production SDK code from opening or depending on BoltDB:\n%s", strings.Join(sortedStrings(violations), "\n"))
	}
}

func TestP46ExampleConfigUsesPostgresPlayBotDSN(t *testing.T) {
	root := backendRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "config.example.toml"))
	if err != nil {
		t.Fatalf("read config.example.toml: %v", err)
	}

	var cfg struct {
		Database struct {
			Type string `toml:"type"`
			DSN  string `toml:"dsn"`
			Path string `toml:"path"`
		} `toml:"database"`
		Security struct {
			LLMAPIKeyEncryptionKey string `toml:"llm_api_key_encryption_key"`
		} `toml:"security"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config.example.toml: %v", err)
	}

	if cfg.Database.Type != "postgres" {
		t.Fatalf("config.example.toml database.type = %q, want postgres", cfg.Database.Type)
	}
	if !dsnTargetsPlayBot(cfg.Database.DSN) {
		t.Fatalf("config.example.toml database.dsn must target database PlayBot, got %q", cfg.Database.DSN)
	}
	if strings.TrimSpace(cfg.Database.Path) != "" {
		t.Fatalf("config.example.toml must not present database.path as the P4.6 production entrance, got %q", cfg.Database.Path)
	}
	exampleText := string(data)
	if !strings.Contains(exampleText, "[security]") || !strings.Contains(exampleText, "llm_api_key_encryption_key") {
		t.Fatalf("config.example.toml must document [security] llm_api_key_encryption_key; the value may be a placeholder or a commented example")
	}
}

func TestP46ConfigModelDefinesPostgresAndLLMEncryptionFields(t *testing.T) {
	root := backendRoot(t)
	parsed := parseGoFile(t, filepath.Join(root, "config", "config.go"))

	databaseFields := structFieldsByTomlTag(t, parsed, "DatabaseConfig")
	for _, field := range []string{"type", "dsn"} {
		if _, ok := databaseFields[field]; !ok {
			t.Fatalf("config.DatabaseConfig missing toml:%q field required by P4.6", field)
		}
	}

	if _, ok := structFieldsByTomlTag(t, parsed, "SecurityConfig")["llm_api_key_encryption_key"]; !ok {
		t.Fatalf("config.SecurityConfig missing toml:\"llm_api_key_encryption_key\" field required by P4.6")
	}

	configFields := structFieldsByTomlTag(t, parsed, "Config")
	if _, ok := configFields["security"]; !ok {
		t.Fatalf("config.Config missing toml:\"security\" field required by P4.6")
	}
}

func TestP46StartupRejectsNonPostgresDatabaseType(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config("postgres://user:password@localhost:5432/PlayBot?sslmode=disable", p46Base64Key(0x41), "sk-contract-secret")
	setDatabaseType(cfg, "sqlite")
	err := expectOpenStoreError(t, cfg)
	if err == nil {
		t.Fatalf("startup Store entry accepted database.type != postgres")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "postgres") {
		t.Fatalf("non-postgres database type error = %q, want explicit postgres error", err.Error())
	}
}
`)
}

func TestP46StartupRejectsDSNWithoutPlayBotDatabase(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46NonPlayBotDSN(t), p46Base64Key(0x42), "sk-contract-secret")
	err := expectOpenStoreError(t, cfg)
	if err == nil {
		t.Fatalf("startup Store entry accepted PostgreSQL DSN whose database name is not exactly PlayBot")
	}
	lowerErr := strings.ToLower(err.Error())
	explicitDSNValidation := strings.Contains(lowerErr, "dsn") ||
		strings.Contains(lowerErr, "database name") ||
		strings.Contains(lowerErr, "must") ||
		strings.Contains(lowerErr, "target") ||
		strings.Contains(lowerErr, "exactly")
	if !strings.Contains(lowerErr, "playbot") || !explicitDSNValidation {
		t.Fatalf("wrong database error = %q, want explicit DSN/database name validation for PlayBot before connecting", err.Error())
	}
}
`)
}

func TestP46StartupRejectsInvalidLLMAPIKeyEncryptionKey(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	secret := "sk-contract-secret"
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "invalid_key"), "not-valid-base64", secret)
	t.Setenv("BROWSERWING_LLM_API_KEY_ENCRYPTION_KEY", "")
	err := expectOpenStoreError(t, cfg)
	if err == nil {
		t.Fatalf("startup Store entry accepted invalid llm_api_key_encryption_key while an LLM API key must be persisted")
	}
	lowerErr := strings.ToLower(err.Error())
	if !strings.Contains(lowerErr, "llm_api_key_encryption_key") && !strings.Contains(lowerErr, "encryption key") {
		t.Fatalf("invalid encryption key error = %q, want explicit llm_api_key_encryption_key/encryption key error", err.Error())
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid encryption key error leaked LLM API key: %q", err.Error())
	}
}
`)
}

func TestP46LLMAPIKeyEncryptionKeySourcePrecedence(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	dsn := p46IsolatedPlayBotDSN(t, "env_key_precedence")
	fileKey := "not-valid-file-key"
	envKey := p46Base64Key(0x44)
	cfg := p46Config(dsn, fileKey, "sk-contract-secret")
	t.Setenv("BROWSERWING_LLM_API_KEY_ENCRYPTION_KEY", envKey)
	store, cleanup := openStore(t, cfg)
	defer cleanup()
	if _, err := store.GetDefaultLLMConfig(); err != nil {
		t.Fatalf("startup did not import/load LLM config using env encryption key override: %v", err)
	}
}
`)
}

func TestP46StartupDoesNotReuseAuthAppKeyForLLMEncryption(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "auth_key_not_llm_key"), "", "sk-contract-secret")
	setAuthBool(cfg, "Enabled", true)
	setAuthString(cfg, "AppKey", p46Base64Key(0x45))
	t.Setenv("BROWSERWING_LLM_API_KEY_ENCRYPTION_KEY", "")
	err := expectOpenStoreError(t, cfg)
	if err == nil {
		t.Fatalf("startup Store entry reused or ignored auth.app_key for LLM API key encryption")
	}
	lowerErr := strings.ToLower(err.Error())
	if !strings.Contains(lowerErr, "llm") || (!strings.Contains(lowerErr, "encryption") && !strings.Contains(lowerErr, "key")) {
		t.Fatalf("missing LLM encryption key error = %q, want explicit LLM encryption key error rather than connection fallback", err.Error())
	}
}
`)
}

func TestP46StartupSeedsEmptyPlayBotDatabase(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "startup_seed"), p46Base64Key(0x46), "sk-p46-startup-import")
	cfg.LLMs[0].Name = "p46-startup-import"
	setAuthBool(cfg, "Enabled", true)
	setAuthString(cfg, "DefaultUsername", "p46-contract-admin")
	setAuthString(cfg, "DefaultPassword", "p46-contract-password")

	db := openRawDB(t, cfg)
	requireEmptyIfTableExists(t, db, "llm_configs")
	requireEmptyIfTableExists(t, db, "prompts")
	requireEmptyIfTableExists(t, db, "scripts")
	requireEmptyIfTableExists(t, db, "browser_instances")
	requireEmptyIfTableExists(t, db, "users")
	db.Close()

	store, cleanup := openStore(t, cfg)
	defer cleanup()

	llm, err := store.GetDefaultLLMConfig()
	if err != nil {
		t.Fatalf("startup did not import config LLM into empty PlayBot Store: %v", err)
	}
	if llm.Name != "p46-startup-import" || llm.APIKey != "sk-p46-startup-import" || !llm.IsDefault || !llm.IsActive {
		t.Fatalf("startup imported LLM config = %+v, want enabled default config from file with decrypted API key", llm)
	}

	prompts, err := store.ListPrompts()
	if err != nil {
		t.Fatalf("ListPrompts after startup seed: %v", err)
	}
	if len(prompts) == 0 {
		t.Fatalf("startup did not seed system prompts")
	}

	scripts, err := store.ListScripts()
	if err != nil {
		t.Fatalf("ListScripts after startup seed: %v", err)
	}
	if len(scripts) == 0 {
		t.Fatalf("startup did not seed builtin scripts")
	}

	if _, err := store.GetDefaultBrowserInstance(); err != nil {
		t.Fatalf("startup did not seed default browser instance: %v", err)
	}
	if _, err := store.GetUserByUsername("p46-contract-admin"); err != nil {
		t.Fatalf("startup did not seed default auth user when auth is enabled: %v", err)
	}
}
`)
}

func TestP46PlaybotJobJSONDoesNotContainPostgresLLMAPIKey(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	secret := "sk-p46-playbot-postgres-secret"
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "playbot_redaction"), p46Base64Key(0x55), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	llm := &models.LLMConfigModel{
		ID:        p46ID("llm_playbot"),
		Name:      "contract playbot redaction",
		Provider:  "openai",
		APIKey:    secret,
		Model:     "gpt-4o-mini",
		BaseURL:   "https://api.example.invalid",
		IsDefault: true,
		IsActive:  true,
	}
	defer store.DeleteLLMConfig(llm.ID)
	if err := store.SaveLLMConfig(llm); err != nil {
		t.Fatalf("SaveLLMConfig for Playbot redaction contract: %v", err)
	}
	runtimeCfg, err := store.GetLLMConfig(llm.ID)
	if err != nil {
		t.Fatalf("GetLLMConfig for Playbot redaction contract: %v", err)
	}
	if runtimeCfg.APIKey != secret {
		t.Fatalf("Postgres Store did not return decrypted key to runtime Playbot chain: got %q", runtimeCfg.APIKey)
	}

	fakePython, engineDir, jobFile, argsFile := writeFailingFakePlaybot(t, secret)
	_, err = playbot.GenerateTestPlan(context.Background(), playbot.GenerateOptions{
		PageURL:         "https://example.invalid/contracts",
		Snapshot:        map[string]any{"title": "Contract page"},
		IntentPlan:      map[string]any{"goal": "generate cases"},
		PageDescription: "contract page",
		Instruction:     "generate a smoke case",
		LLMEndpoint:     runtimeCfg.BaseURL,
		LLMAPIKey:       runtimeCfg.APIKey,
		LLMModel:        runtimeCfg.Model,
		PythonPath:      fakePython,
		EngineDir:       engineDir,
	})
	if err == nil {
		t.Fatalf("fake Playbot should fail so the contract can inspect redacted error propagation")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Playbot error summary leaked decrypted LLM API key: %q", err.Error())
	}

	jobBytes, readErr := os.ReadFile(jobFile)
	if readErr != nil {
		t.Fatalf("read fake Playbot job JSON: %v", readErr)
	}
	if strings.Contains(string(jobBytes), secret) {
		t.Fatalf("Playbot job JSON leaked decrypted LLM API key: %s", string(jobBytes))
	}
	argsBytes, readErr := os.ReadFile(argsFile)
	if readErr != nil {
		t.Fatalf("read fake Playbot argv record: %v", readErr)
	}
	if !strings.Contains(string(argsBytes), secret) {
		t.Fatalf("decrypted LLM API key was not passed to the controlled Playbot CLI argv; args=%q", string(argsBytes))
	}
}
`)
}

func TestP46LLMConfigPersistenceModelDoesNotExposePlainAPIKeyColumn(t *testing.T) {
	root := backendRoot(t)

	var plaintextColumns []string
	hasCiphertext := false
	hasNonce := false
	hasKeyID := false
	for _, file := range productionGoFiles(t, root) {
		parsed := parseGoFile(t, file)
		for structName, fields := range allStructFields(parsed) {
			if !isLLMPersistenceStruct(structName, fields) {
				continue
			}
			for fieldName, field := range fields {
				tag := strings.Trim(field.Tag, "`")
				loweredTag := strings.ToLower(tag)
				if fieldName == "APIKey" && !fieldMarkedNonPersistent(loweredTag) {
					plaintextColumns = append(plaintextColumns, relPath(t, root, file)+":"+structName+"."+fieldName)
					continue
				}
				if strings.Contains(loweredTag, "api_key") && !strings.Contains(loweredTag, "ciphertext") && !fieldMarkedNonPersistent(loweredTag) {
					plaintextColumns = append(plaintextColumns, relPath(t, root, file)+":"+structName+"."+fieldName)
				}
				switch fieldName {
				case "APIKeyCiphertext":
					hasCiphertext = true
				case "APIKeyNonce":
					hasNonce = true
				case "APIKeyKeyID":
					hasKeyID = true
				}
			}
		}
	}
	if len(plaintextColumns) > 0 {
		t.Fatalf("P4.6 forbids plaintext api_key persistence columns:\n%s", strings.Join(sortedStrings(plaintextColumns), "\n"))
	}
	if !hasCiphertext || !hasNonce || !hasKeyID {
		t.Fatalf("P4.6 requires LLM API key encrypted at-rest fields APIKeyCiphertext/APIKeyNonce/APIKeyKeyID")
	}
}

func TestP46ScriptStructuredFieldsHavePostgresColumnTags(t *testing.T) {
	root := backendRoot(t)
	inventory := parseStoreOperationInventory(t, root)
	if len(inventory.entries) == 0 {
		t.Fatalf("Store Operation Inventory entries not found; cannot verify scripts.description and scripts.mcp_command_description are declared as structured PostgreSQL columns")
	}

	var targets []string
	for _, entry := range inventory.entries {
		if entry.Domain == "Script" || strings.HasPrefix(entry.Method, "SaveScript") || strings.HasPrefix(entry.Method, "GetScript") || strings.HasPrefix(entry.Method, "ListScript") {
			targets = append(targets, entry.PostgresTarget)
		}
	}
	joined := strings.ToLower(strings.Join(targets, "\n"))
	for _, column := range []string{"scripts.description", "scripts.mcp_command_description"} {
		if !strings.Contains(joined, column) {
			t.Fatalf("Store Operation Inventory must declare %s as a structured PostgreSQL column target; targets:\n%s", column, strings.Join(targets, "\n"))
		}
	}
}

func TestP46PostgresStoreScriptRoundTrip(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "script_roundtrip"), p46Base64Key(0x51), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	db := openRawDB(t, cfg)
	defer db.Close()
	requireColumn(t, db, "scripts", "description")
	requireColumn(t, db, "scripts", "mcp_command_description")
	for _, column := range []string{"actions", "tags", "downloaded_files", "mcp_input_schema", "variables"} {
		requireColumnType(t, db, "scripts", column, "jsonb")
	}

	script := &models.Script{
		ID:          p46ID("script"),
		Name:        "contract script",
		Description: "structured description",
		URL:         "https://example.invalid",
		Group:       "contract",
		Duration:    42,
		CanPublish: true,
		CanFetch:   true,
		RequiresLogin: true,
		Actions: []models.ScriptAction{{
			Type:     "click",
			Selector: "#submit",
			Intent:   &models.ActionIntent{Verb: "click", Object: "submit"},
		}},
		Tags: []string{"p46", "roundtrip"},
		DownloadedFiles: []models.DownloadedFile{{
			FileName: "report.csv",
			FilePath: "/tmp/report.csv",
			URL:      "https://example.invalid/report.csv",
			MimeType: "text/csv",
			Size:     12,
		}},
		IsMCPCommand:          true,
		MCPCommandName:        "contract_command",
		MCPCommandDescription: "structured MCP description",
		MCPInputSchema:        map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}}},
		Variables:             map[string]string{"tenant": "contract", "role": "admin"},
	}
	defer store.DeleteScript(script.ID)

	if err := store.SaveScript(script); err != nil {
		t.Fatalf("SaveScript through Postgres Store: %v", err)
	}
	got, err := store.GetScript(script.ID)
	if err != nil {
		t.Fatalf("GetScript through Postgres Store: %v", err)
	}
	if got.Description != script.Description || got.MCPCommandDescription != script.MCPCommandDescription {
		t.Fatalf("structured script fields did not roundtrip through Store: description=%q mcp=%q", got.Description, got.MCPCommandDescription)
	}
	if len(got.Actions) != 1 || got.Actions[0].Intent == nil || got.Actions[0].Intent.Object != "submit" {
		t.Fatalf("actions JSONB roundtrip lost nested intent: %+v", got.Actions)
	}
	if len(got.Tags) != 2 || got.Tags[1] != "roundtrip" {
		t.Fatalf("tags JSONB roundtrip lost payload: %+v", got.Tags)
	}
	if len(got.DownloadedFiles) != 1 || got.DownloadedFiles[0].FileName != "report.csv" {
		t.Fatalf("downloaded_files JSONB roundtrip lost payload: %+v", got.DownloadedFiles)
	}
	if got.MCPInputSchema["type"] != "object" || got.Variables["tenant"] != "contract" {
		t.Fatalf("mcp_input_schema/variables JSONB roundtrip lost payload: schema=%+v variables=%+v", got.MCPInputSchema, got.Variables)
	}
}
`)
}

func TestP46PostgresStoreLLMDefaultUniquenessAndEncryptedAtRest(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	secret := "sk-p46-contract-secret"
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "llm_at_rest"), p46Base64Key(0x52), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	db := openRawDB(t, cfg)
	defer db.Close()
	requireColumnAbsent(t, db, "llm_configs", "api_key")
	for _, column := range []string{"api_key_ciphertext", "api_key_nonce", "api_key_key_id"} {
		requireColumn(t, db, "llm_configs", column)
	}

	first := &models.LLMConfigModel{
		ID:        p46ID("llm_a"),
		Name:      "contract default one",
		Provider:  "openai",
		APIKey:    secret,
		Model:     "gpt-4o-mini",
		BaseURL:   "https://api.example.invalid",
		IsDefault: true,
		IsActive:  true,
	}
	second := &models.LLMConfigModel{
		ID:        p46ID("llm_b"),
		Name:      "contract default two",
		Provider:  "openai",
		APIKey:    "sk-p46-second-secret",
		Model:     "gpt-4o-mini",
		BaseURL:   "https://api.example.invalid",
		IsDefault: true,
		IsActive:  true,
	}
	defer store.DeleteLLMConfig(first.ID)
	defer store.DeleteLLMConfig(second.ID)

	if err := store.SaveLLMConfig(first); err != nil {
		t.Fatalf("SaveLLMConfig first default through Postgres Store: %v", err)
	}
	_ = store.SaveLLMConfig(second)

	configs, err := store.ListLLMConfigs()
	if err != nil {
		t.Fatalf("ListLLMConfigs through Postgres Store: %v", err)
	}
	defaults := 0
	for _, cfg := range configs {
		if cfg.IsDefault && cfg.IsActive {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("active default LLM config count = %d, want exactly 1", defaults)
	}

	runtimeCfg, err := store.GetLLMConfig(first.ID)
	if err != nil {
		t.Fatalf("GetLLMConfig through Postgres Store: %v", err)
	}
	if runtimeCfg.APIKey != secret {
		t.Fatalf("Store did not decrypt API key for runtime use: got %q", runtimeCfg.APIKey)
	}

	raw := queryLLMAtRestPayload(t, db, first.ID)
	if strings.Contains(raw, secret) {
		t.Fatalf("llm_configs at-rest payload leaked plaintext API key: %s", raw)
	}
}
`)
}

func TestP46PostgresStoreLLMUpdateWithoutAPIKeyPreservesSecretAndManagerRuntime(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	secret := "sk-p46-contract-retained-secret"
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "llm_update_without_key"), p46Base64Key(0x5c), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	db := openRawDB(t, cfg)
	defer db.Close()

	original := &models.LLMConfigModel{
		ID:        p46ID("llm_update"),
		Name:      "contract update retains key",
		Provider:  "openai",
		APIKey:    secret,
		Model:     "gpt-4o-mini",
		BaseURL:   "https://api.example.invalid",
		IsDefault: true,
		IsActive:  true,
	}
	defer store.DeleteLLMConfig(original.ID)

	if err := store.SaveLLMConfig(original); err != nil {
		t.Fatalf("SaveLLMConfig original through Postgres Store: %v", err)
	}
	rawBefore := queryLLMAtRestPayload(t, db, original.ID)
	if rawBefore == "" || strings.Contains(rawBefore, secret) {
		t.Fatalf("original at-rest LLM payload should be encrypted and non-empty, got %q", rawBefore)
	}

	manager := llm.NewManager(store)
	if err := manager.LoadAll(); err != nil {
		t.Fatalf("LoadAll before update: %v", err)
	}

	updated := &models.LLMConfigModel{
		ID:        original.ID,
		Name:      "contract update renamed",
		Provider:  "openai",
		Model:     "gpt-4o",
		BaseURL:   "https://api-updated.example.invalid",
		IsDefault: true,
		IsActive:  true,
	}
	if err := manager.Update(updated); err != nil {
		t.Fatalf("Manager.Update without api_key should preserve existing encrypted key and reload runtime config: %v", err)
	}

	rawAfter := queryLLMAtRestPayload(t, db, original.ID)
	if rawAfter != rawBefore {
		t.Fatalf("UpdateLLMConfig without api_key must preserve encrypted API key fields exactly, before=%q after=%q", rawBefore, rawAfter)
	}

	stored, err := store.GetLLMConfig(original.ID)
	if err != nil {
		t.Fatalf("GetLLMConfig after api_key-less update: %v", err)
	}
	if stored.APIKey != secret {
		t.Fatalf("Store did not retain decrypted API key after api_key-less update: got %q", stored.APIKey)
	}
	if stored.Name != updated.Name || stored.Model != updated.Model || stored.BaseURL != updated.BaseURL {
		t.Fatalf("UpdateLLMConfig did not persist non-secret fields: %+v", stored)
	}

	if _, ok := manager.Get(original.Name); ok {
		t.Fatalf("Manager.Update should remove extractor under old LLM config name")
	}
	extractor, ok := manager.Get(updated.Name)
	if !ok {
		t.Fatalf("Manager.Update should load active updated LLM config into memory")
	}
	if got := extractorAPIKey(t, extractor); got != secret {
		t.Fatalf("Manager runtime extractor did not keep original API key after api_key-less update: got %q", got)
	}
}
`)
}

func TestP46PostgresStoreToolConfigByScriptDeletion(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "tool_cascade"), p46Base64Key(0x53), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	script := &models.Script{
		ID:          p46ID("script_delete"),
		Name:        "delete cascade script",
		Description: "desc",
		URL:         "https://example.invalid",
	}
	otherScript := &models.Script{
		ID:          p46ID("script_keep"),
		Name:        "script whose tool must survive",
		Description: "desc",
		URL:         "https://example.invalid/keep",
	}
	tool := &models.ToolConfig{
		ID:          p46ID("tool"),
		Name:        "contract tool",
		Type:        models.ToolTypeScript,
		Description: "tool desc",
		Enabled:     true,
		ScriptID:    script.ID,
		Parameters:  map[string]any{"mode": "contract"},
	}
	otherTool := &models.ToolConfig{
		ID:          p46ID("tool_keep"),
		Name:        "contract tool keep",
		Type:        models.ToolTypeScript,
		Description: "other script tool desc",
		Enabled:     true,
		ScriptID:    otherScript.ID,
		Parameters:  map[string]any{"mode": "keep"},
	}
	defer store.DeleteToolConfig(tool.ID)
	defer store.DeleteToolConfig(otherTool.ID)
	defer store.DeleteScript(script.ID)
	defer store.DeleteScript(otherScript.ID)

	if err := store.SaveScript(script); err != nil {
		t.Fatalf("SaveScript for tool cascade contract: %v", err)
	}
	if err := store.SaveScript(otherScript); err != nil {
		t.Fatalf("SaveScript other for DeleteToolConfigByScriptID contract: %v", err)
	}
	if err := store.SaveToolConfig(tool); err != nil {
		t.Fatalf("SaveToolConfig for script: %v", err)
	}
	if err := store.SaveToolConfig(otherTool); err != nil {
		t.Fatalf("SaveToolConfig for other script: %v", err)
	}
	if err := store.DeleteToolConfigByScriptID(script.ID); err != nil {
		t.Fatalf("DeleteToolConfigByScriptID through Postgres Store: %v", err)
	}
	if got, err := store.GetToolConfig(tool.ID); err == nil && got != nil {
		t.Fatalf("DeleteToolConfigByScriptID left target script tool config: %+v", got)
	}
	if got, err := store.GetToolConfig(otherTool.ID); err != nil || got == nil {
		t.Fatalf("DeleteToolConfigByScriptID removed other script tool config, got=%+v err=%v", got, err)
	}
	if err := store.SaveToolConfig(tool); err != nil {
		t.Fatalf("SaveToolConfig again for DeleteScript cascade contract: %v", err)
	}
	if err := store.DeleteScript(script.ID); err != nil {
		t.Fatalf("DeleteScript through Postgres Store: %v", err)
	}
	if got, err := store.GetToolConfig(tool.ID); err == nil && got != nil {
		t.Fatalf("script tool config survived DeleteScript: %+v", got)
	}
}
`)
}

func TestP46PostgresStoreSchedulerPaginationAndFilters(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "scheduler"), p46Base64Key(0x54), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	oldTime := time.Now().Add(-time.Hour)
	newTime := time.Now()
	oldTask := &models.ScheduledTask{
		ID:             p46ID("task_old"),
		Name:           "alpha old",
		Description:    "contract filter target",
		Enabled:        true,
		ScheduleType:   models.ScheduleTypeCron,
		ScheduleConfig: "0 0 * * *",
		ExecutionType:  models.ExecutionTypeScript,
		ScriptID:       "script-a",
		ScriptName:     "script A",
		ResultDir:      "/tmp",
		CreatedAt:      oldTime,
		UpdatedAt:      oldTime,
	}
	newTask := &models.ScheduledTask{
		ID:             p46ID("task_new"),
		Name:           "alpha new",
		Description:    "contract filter target",
		Enabled:        true,
		ScheduleType:   models.ScheduleTypeCron,
		ScheduleConfig: "0 1 * * *",
		ExecutionType:  models.ExecutionTypeScript,
		ScriptID:       "script-b",
		ScriptName:     "script B",
		ResultDir:      "/tmp",
		CreatedAt:      newTime,
		UpdatedAt:      newTime,
	}
	defer store.DeleteScheduledTask(oldTask.ID)
	defer store.DeleteScheduledTask(newTask.ID)

	if err := store.CreateScheduledTask(oldTask); err != nil {
		t.Fatalf("CreateScheduledTask old through Postgres Store: %v", err)
	}
	if err := store.CreateScheduledTask(newTask); err != nil {
		t.Fatalf("CreateScheduledTask new through Postgres Store: %v", err)
	}
	got, total, err := store.ListScheduledTasksWithPagination(1, 1, "contract filter")
	if err != nil {
		t.Fatalf("ListScheduledTasksWithPagination through Postgres Store: %v", err)
	}
	if total < 2 {
		t.Fatalf("scheduled task filtered total = %d, want at least 2", total)
	}
	if len(got) != 1 || got[0].ID != newTask.ID {
		t.Fatalf("scheduler pagination/filter/sort returned %+v, want first page [%s]", got, newTask.ID)
	}
}
`)
}

func TestP46PostgresStoreBrowserDefaultsAndCookieRoundTrip(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "browser_cookie"), p46Base64Key(0x56), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	headless := true
	noSandbox := true
	firstConfig := &models.BrowserConfig{
		ID:          p46ID("browser_config_a"),
		Name:        "contract browser config A",
		Description: "first default",
		IsDefault:   true,
		URLPattern:  "https://a.example.invalid/*",
		UserAgent:   "BrowserWingContract/1",
		Headless:    &headless,
		NoSandbox:   &noSandbox,
		LaunchArgs:  []string{"--disable-gpu", "--window-size=1200,800"},
		Proxy:       "http://proxy.example.invalid:8080",
	}
	secondConfig := &models.BrowserConfig{
		ID:          p46ID("browser_config_b"),
		Name:        "contract browser config B",
		Description: "second default",
		IsDefault:   true,
		URLPattern:  "https://b.example.invalid/*",
		UserAgent:   "BrowserWingContract/2",
		Headless:    &headless,
		LaunchArgs:  []string{"--lang=en-US"},
	}
	defer store.DeleteBrowserConfig(firstConfig.ID)
	defer store.DeleteBrowserConfig(secondConfig.ID)
	if err := store.SaveBrowserConfig(firstConfig); err != nil {
		t.Fatalf("SaveBrowserConfig first default through Postgres Store: %v", err)
	}
	if err := store.SaveBrowserConfig(secondConfig); err != nil {
		t.Fatalf("SaveBrowserConfig second default through Postgres Store: %v", err)
	}
	defaultConfig, err := store.GetDefaultBrowserConfig()
	if err != nil {
		t.Fatalf("GetDefaultBrowserConfig through Postgres Store: %v", err)
	}
	if defaultConfig.ID != secondConfig.ID {
		t.Fatalf("default browser config ID = %s, want latest default %s", defaultConfig.ID, secondConfig.ID)
	}
	reloadedFirstConfig, err := store.GetBrowserConfig(firstConfig.ID)
	if err != nil {
		t.Fatalf("GetBrowserConfig first through Postgres Store: %v", err)
	}
	if reloadedFirstConfig.IsDefault {
		t.Fatalf("SaveBrowserConfig did not clear previous default")
	}
	if reloadedFirstConfig.Headless == nil || !*reloadedFirstConfig.Headless || len(reloadedFirstConfig.LaunchArgs) != 2 || reloadedFirstConfig.Proxy != firstConfig.Proxy {
		t.Fatalf("browser config field roundtrip lost data: %+v", reloadedFirstConfig)
	}
	if err := store.DeleteBrowserConfig(secondConfig.ID); err == nil {
		t.Fatalf("DeleteBrowserConfig accepted deletion of current default config")
	}
	defaultConfig, err = store.GetDefaultBrowserConfig()
	if err != nil {
		t.Fatalf("GetDefaultBrowserConfig after rejected default delete: %v", err)
	}
	if defaultConfig.ID != secondConfig.ID {
		t.Fatalf("default browser config changed after rejected delete: %+v", defaultConfig)
	}

	firstInstance := &models.BrowserInstance{
		ID:          p46ID("browser_instance_a"),
		Name:        "contract instance A",
		Description: "local first",
		IsDefault:   true,
		IsActive:    true,
		Type:        "local",
		BinPath:     "chrome-a",
		UserDataDir: "profile-a",
		LaunchArgs:  []string{"--contract-a"},
	}
	secondInstance := &models.BrowserInstance{
		ID:          p46ID("browser_instance_b"),
		Name:        "contract instance B",
		Description: "remote second",
		IsDefault:   true,
		IsActive:    true,
		Type:        "remote",
		ControlURL:  "http://127.0.0.1:9222",
	}
	defer store.DeleteBrowserInstance(firstInstance.ID)
	defer store.DeleteBrowserInstance(secondInstance.ID)
	if err := store.SaveBrowserInstance(firstInstance); err != nil {
		t.Fatalf("SaveBrowserInstance first default through Postgres Store: %v", err)
	}
	if err := store.SaveBrowserInstance(secondInstance); err != nil {
		t.Fatalf("SaveBrowserInstance second default through Postgres Store: %v", err)
	}
	defaultInstance, err := store.GetDefaultBrowserInstance()
	if err != nil {
		t.Fatalf("GetDefaultBrowserInstance through Postgres Store: %v", err)
	}
	if defaultInstance.ID != secondInstance.ID {
		t.Fatalf("default browser instance ID = %s, want latest default %s", defaultInstance.ID, secondInstance.ID)
	}
	reloadedFirstInstance, err := store.GetBrowserInstance(firstInstance.ID)
	if err != nil {
		t.Fatalf("GetBrowserInstance first through Postgres Store: %v", err)
	}
	if reloadedFirstInstance.IsDefault {
		t.Fatalf("SaveBrowserInstance did not clear previous default")
	}
	if len(reloadedFirstInstance.LaunchArgs) != 1 || reloadedFirstInstance.LaunchArgs[0] != "--contract-a" {
		t.Fatalf("browser instance JSON fields did not roundtrip: %+v", reloadedFirstInstance)
	}
	if err := store.DeleteBrowserInstance(secondInstance.ID); err == nil {
		t.Fatalf("DeleteBrowserInstance accepted deletion of current default instance")
	}
	defaultInstance, err = store.GetDefaultBrowserInstance()
	if err != nil {
		t.Fatalf("GetDefaultBrowserInstance after rejected default delete: %v", err)
	}
	if defaultInstance.ID != secondInstance.ID {
		t.Fatalf("default browser instance changed after rejected delete: %+v", defaultInstance)
	}

	cookieStore := &models.CookieStore{
		ID:       p46ID("cookie"),
		Platform: "contract-platform",
		Cookies: []*proto.NetworkCookie{{
			Name:     "session",
			Value:    "sensitive-cookie-value",
			Domain:   "example.invalid",
			Path:     "/",
			HTTPOnly: true,
			Secure:   true,
		}},
	}
	defer store.DeleteCookies(cookieStore.ID)
	if err := store.SaveCookies(cookieStore); err != nil {
		t.Fatalf("SaveCookies through Postgres Store: %v", err)
	}
	gotCookies, err := store.GetCookies(cookieStore.ID)
	if err != nil {
		t.Fatalf("GetCookies through Postgres Store: %v", err)
	}
	if gotCookies.Platform != cookieStore.Platform || len(gotCookies.Cookies) != 1 || gotCookies.Cookies[0].Value != "sensitive-cookie-value" || !gotCookies.Cookies[0].HTTPOnly {
		t.Fatalf("cookie roundtrip lost data: %+v", gotCookies)
	}
}
`)
}

func TestP46PostgresStorePromptSystemUpgrade(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "prompt_upgrade"), p46Base64Key(0x57), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	latestExtractor := models.GetSystemPromptByID(models.SystemPromptExtractorID)
	if latestExtractor == nil {
		t.Fatalf("missing production system prompt %s", models.SystemPromptExtractorID)
	}
	createdAt := time.Now().Add(-2 * time.Hour).Round(time.Microsecond)
	stale := &models.Prompt{
		ID:          models.SystemPromptExtractorID,
		Name:        "stale extractor",
		Description: "old system prompt",
		Content:     "old content",
		Type:        models.PromptTypeSystem,
		Version:     latestExtractor.Version - 1,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt,
	}
	if stale.Version < 0 {
		stale.Version = 0
	}
	if err := store.SavePrompt(stale); err != nil {
		t.Fatalf("SavePrompt stale system prompt: %v", err)
	}

	latestForm := models.GetSystemPromptByID(models.SystemPromptFormFillerID)
	if latestForm == nil {
		t.Fatalf("missing production system prompt %s", models.SystemPromptFormFillerID)
	}
	modified := &models.Prompt{
		ID:          models.SystemPromptFormFillerID,
		Name:        "user modified form prompt",
		Description: "must not be overwritten",
		Content:     "user edited content",
		Type:        models.PromptTypeSystem,
		Version:     latestForm.Version - 1,
		CreatedAt:   createdAt,
		UpdatedAt:   createdAt.Add(2 * time.Minute),
	}
	if modified.Version < 0 {
		modified.Version = 0
	}
	if err := store.SavePrompt(modified); err != nil {
		t.Fatalf("SavePrompt modified system prompt: %v", err)
	}

	if err := store.CheckAndUpdateSystemPrompts(); err != nil {
		t.Fatalf("CheckAndUpdateSystemPrompts through Postgres Store: %v", err)
	}
	upgraded, err := store.GetPrompt(models.SystemPromptExtractorID)
	if err != nil {
		t.Fatalf("GetPrompt upgraded system prompt: %v", err)
	}
	if upgraded.Version != latestExtractor.Version || upgraded.Content != latestExtractor.Content {
		t.Fatalf("stale unmodified system prompt was not upgraded: %+v want version %d", upgraded, latestExtractor.Version)
	}
	if !upgraded.CreatedAt.Equal(createdAt) {
		t.Fatalf("system prompt upgrade should preserve CreatedAt; got %s want %s", upgraded.CreatedAt, createdAt)
	}
	preserved, err := store.GetPrompt(models.SystemPromptFormFillerID)
	if err != nil {
		t.Fatalf("GetPrompt modified system prompt: %v", err)
	}
	if preserved.Content != modified.Content || preserved.Version != modified.Version {
		t.Fatalf("user-modified system prompt was overwritten: %+v", preserved)
	}
}
`)
}

func TestP46PostgresStoreAgentCascadeAndMCPServiceToolsRoundTrip(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "agent_mcp"), p46Base64Key(0x58), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	session := &models.AgentSession{
		ID:          p46ID("agent_session"),
		LLMConfigID: "llm-contract",
		CreatedAt:   time.Now().Add(-time.Hour),
		UpdatedAt:   time.Now().Add(-time.Hour),
	}
	if err := store.SaveAgentSession(session); err != nil {
		t.Fatalf("SaveAgentSession through Postgres Store: %v", err)
	}
	oldMessage := &models.AgentMessage{
		ID:        p46ID("agent_message_old"),
		SessionID: session.ID,
		Role:      "user",
		Content:   "old",
		Timestamp: time.Now().Add(-time.Minute),
		ToolCalls: []map[string]interface{}{{"name": "browser_snapshot", "arguments": map[string]interface{}{"url": "https://example.invalid"}}},
	}
	newMessage := &models.AgentMessage{
		ID:        p46ID("agent_message_new"),
		SessionID: session.ID,
		Role:      "assistant",
		Content:   "new",
		Timestamp: time.Now(),
	}
	if err := store.SaveAgentMessage(newMessage); err != nil {
		t.Fatalf("SaveAgentMessage new through Postgres Store: %v", err)
	}
	if err := store.SaveAgentMessage(oldMessage); err != nil {
		t.Fatalf("SaveAgentMessage old through Postgres Store: %v", err)
	}
	messages, err := store.ListAgentMessages(session.ID)
	if err != nil {
		t.Fatalf("ListAgentMessages through Postgres Store: %v", err)
	}
	if len(messages) != 2 || messages[0].ID != oldMessage.ID || messages[1].ID != newMessage.ID {
		t.Fatalf("agent messages order = %+v, want timestamp asc old,new", messages)
	}
	if len(messages[0].ToolCalls) != 1 || messages[0].ToolCalls[0]["name"] != "browser_snapshot" {
		t.Fatalf("agent message tool_calls JSON did not roundtrip: %+v", messages[0].ToolCalls)
	}
	if err := store.DeleteAgentSession(session.ID); err != nil {
		t.Fatalf("DeleteAgentSession through Postgres Store: %v", err)
	}
	messages, err = store.ListAgentMessages(session.ID)
	if err != nil {
		t.Fatalf("ListAgentMessages after DeleteAgentSession: %v", err)
	}
	if len(messages) != 0 {
		t.Fatalf("DeleteAgentSession did not cascade messages: %+v", messages)
	}

	service := &models.MCPService{
		ID:          p46ID("mcp_service"),
		Name:        "contract mcp",
		Description: "roundtrip service",
		Type:        models.MCPServiceTypeStdio,
		Command:     "node",
		Args:        []string{"server.js", "--stdio"},
		Env:         map[string]string{"TENANT": "contract"},
		Enabled:     true,
		Status:      models.MCPServiceStatusDisconnected,
	}
	defer store.DeleteMCPService(service.ID)
	if err := store.SaveMCPService(service); err != nil {
		t.Fatalf("SaveMCPService through Postgres Store: %v", err)
	}
	tools := []models.MCPDiscoveredTool{{
		Name:        "search",
		Description: "search tool",
		Enabled:     true,
		Schema:      map[string]interface{}{"type": "object", "properties": map[string]interface{}{"query": map[string]interface{}{"type": "string"}}},
	}, {
		Name:        "open",
		Description: "open tool",
		Enabled:     false,
		Schema:      map[string]interface{}{"type": "object"},
	}}
	if err := store.SaveMCPServiceTools(service.ID, tools); err != nil {
		t.Fatalf("SaveMCPServiceTools through Postgres Store: %v", err)
	}
	gotTools, err := store.GetMCPServiceTools(service.ID)
	if err != nil {
		t.Fatalf("GetMCPServiceTools through Postgres Store: %v", err)
	}
	if len(gotTools) != 2 || gotTools[0].Name != "search" || gotTools[0].Schema["type"] != "object" || gotTools[1].Enabled {
		t.Fatalf("MCP service tools did not roundtrip: %+v", gotTools)
	}
	gotService, err := store.GetMCPService(service.ID)
	if err != nil {
		t.Fatalf("GetMCPService through Postgres Store: %v", err)
	}
	if gotService.ToolCount != 2 || len(gotService.Args) != 2 || gotService.Env["TENANT"] != "contract" {
		t.Fatalf("MCP service metadata/tools count did not roundtrip: %+v", gotService)
	}
}
`)
}

func TestP46PostgresStoreAuthLookupAndUniqueness(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "auth_lookup"), p46Base64Key(0x59), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	user := &models.User{
		ID:        p46ID("user"),
		Username:  "p46-contract-user",
		Password:  "hashed-password",
		CreatedAt: time.Now().Add(-time.Hour),
		UpdatedAt: time.Now().Add(-time.Hour),
	}
	defer store.DeleteUser(user.ID)
	if err := store.CreateUser(user); err != nil {
		t.Fatalf("CreateUser through Postgres Store: %v", err)
	}
	byUsername, err := store.GetUserByUsername(user.Username)
	if err != nil {
		t.Fatalf("GetUserByUsername through Postgres Store: %v", err)
	}
	if byUsername.ID != user.ID {
		t.Fatalf("GetUserByUsername returned %+v, want ID %s", byUsername, user.ID)
	}
	duplicateUser := &models.User{ID: p46ID("user_dup"), Username: user.Username, Password: "other"}
	if err := store.CreateUser(duplicateUser); err == nil {
		defer store.DeleteUser(duplicateUser.ID)
		t.Fatalf("CreateUser accepted duplicate username %q", user.Username)
	}

	apiKey := &models.ApiKey{
		ID:          p46ID("api_key"),
		Name:        "contract key",
		Key:         "p46-contract-api-key",
		Description: "lookup by key",
		UserID:      user.ID,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	defer store.DeleteApiKey(apiKey.ID)
	if err := store.CreateApiKey(apiKey); err != nil {
		t.Fatalf("CreateApiKey through Postgres Store: %v", err)
	}
	byKey, err := store.GetApiKeyByKey(apiKey.Key)
	if err != nil {
		t.Fatalf("GetApiKeyByKey through Postgres Store: %v", err)
	}
	if byKey.ID != apiKey.ID || byKey.UserID != user.ID {
		t.Fatalf("GetApiKeyByKey returned %+v, want ID %s user %s", byKey, apiKey.ID, user.ID)
	}
	duplicateKey := &models.ApiKey{ID: p46ID("api_key_dup"), Name: "duplicate", Key: apiKey.Key, UserID: user.ID}
	if err := store.CreateApiKey(duplicateKey); err == nil {
		defer store.DeleteApiKey(duplicateKey.ID)
		t.Fatalf("CreateApiKey accepted duplicate key value")
	}
}
`)
}

func TestP46PostgresStoreExecutionAndRecordingDomains(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "executions_recording"), p46Base64Key(0x5a), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()

	oldStart := time.Now().Add(-2 * time.Hour)
	newStart := time.Now().Add(-time.Hour)
	oldExec := &models.ScriptExecution{
		ID:            p46ID("script_exec_old"),
		ScriptID:      "script-contract",
		ScriptName:    "contract script",
		InstanceID:    "instance-a",
		InstanceName:  "instance A",
		StartTime:     oldStart,
		EndTime:       oldStart.Add(time.Second),
		Duration:      1000,
		Success:       true,
		Message:       "old success",
		TotalSteps:    2,
		SuccessSteps:  2,
		ExtractedData: map[string]interface{}{"items": []interface{}{"old"}},
		VideoPath:     "old.mp4",
	}
	newExec := &models.ScriptExecution{
		ID:            p46ID("script_exec_new"),
		ScriptID:      "script-contract",
		ScriptName:    "contract script",
		InstanceID:    "instance-b",
		StartTime:     newStart,
		EndTime:       newStart.Add(time.Second),
		Duration:      1000,
		Success:       false,
		Message:       "new failed",
		ErrorMsg:      "target failure",
		TotalSteps:    3,
		SuccessSteps:  2,
		FailedSteps:   1,
		ExtractedData: map[string]interface{}{"items": []interface{}{"new"}},
		VideoPath:     "new.mp4",
	}
	otherExec := &models.ScriptExecution{ID: p46ID("script_exec_other"), ScriptID: "script-other", ScriptName: "other", StartTime: time.Now(), Success: true}
	defer store.DeleteScriptExecution(oldExec.ID)
	defer store.DeleteScriptExecution(newExec.ID)
	defer store.DeleteScriptExecution(otherExec.ID)
	for _, exec := range []*models.ScriptExecution{oldExec, newExec, otherExec} {
		if err := store.SaveScriptExecution(exec); err != nil {
			t.Fatalf("SaveScriptExecution through Postgres Store: %v", err)
		}
	}
	scriptExecs, err := store.ListScriptExecutions("script-contract")
	if err != nil {
		t.Fatalf("ListScriptExecutions through Postgres Store: %v", err)
	}
	if len(scriptExecs) != 2 || scriptExecs[0].ID != newExec.ID || scriptExecs[1].ID != oldExec.ID {
		t.Fatalf("script execution filter/sort returned %+v, want new,old for script-contract", scriptExecs)
	}
	latestExec, err := store.GetLatestScriptExecutionByScriptID("script-contract")
	if err != nil {
		t.Fatalf("GetLatestScriptExecutionByScriptID through Postgres Store: %v", err)
	}
	if latestExec.ID != newExec.ID {
		t.Fatalf("latest script execution ID = %s, want %s", latestExec.ID, newExec.ID)
	}
	otherLatest, err := store.GetLatestScriptExecutionByScriptID("script-other")
	if err != nil {
		t.Fatalf("GetLatestScriptExecutionByScriptID for other script through Postgres Store: %v", err)
	}
	if otherLatest.ID != otherExec.ID {
		t.Fatalf("latest other script execution ID = %s, want %s", otherLatest.ID, otherExec.ID)
	}
	gotNewExec, err := store.GetScriptExecution(newExec.ID)
	if err != nil {
		t.Fatalf("GetScriptExecution through Postgres Store: %v", err)
	}
	if gotNewExec.ExtractedData["items"] == nil || gotNewExec.ErrorMsg != "target failure" || gotNewExec.VideoPath != "new.mp4" {
		t.Fatalf("script execution roundtrip lost payload: %+v", gotNewExec)
	}
	if err := store.DeleteScriptExecutionsByScriptID("script-contract"); err != nil {
		t.Fatalf("DeleteScriptExecutionsByScriptID through Postgres Store: %v", err)
	}
	remaining, err := store.ListScriptExecutions("script-contract")
	if err != nil {
		t.Fatalf("ListScriptExecutions after DeleteScriptExecutionsByScriptID: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("DeleteScriptExecutionsByScriptID left rows: %+v", remaining)
	}
	if _, err := store.GetScriptExecution(otherExec.ID); err != nil {
		t.Fatalf("DeleteScriptExecutionsByScriptID removed other script execution: %v", err)
	}

	recording := &models.RecordingConfig{
		ID:        "default",
		Enabled:   true,
		FrameRate: 24,
		Quality:   82,
		Format:    "mp4",
		OutputDir: "contract-recordings",
	}
	if err := store.SaveRecordingConfig(recording); err != nil {
		t.Fatalf("SaveRecordingConfig through Postgres Store: %v", err)
	}
	gotRecording := store.GetDefaultRecordingConfig()
	if gotRecording == nil || !gotRecording.Enabled || gotRecording.FrameRate != 24 || gotRecording.Format != "mp4" || gotRecording.OutputDir != "contract-recordings" {
		t.Fatalf("GetDefaultRecordingConfig did not return saved default config: %+v", gotRecording)
	}
	byIDRecording, err := store.GetRecordingConfig("default")
	if err != nil {
		t.Fatalf("GetRecordingConfig through Postgres Store: %v", err)
	}
	if byIDRecording.Quality != 82 {
		t.Fatalf("recording config roundtrip lost quality: %+v", byIDRecording)
	}

	task := &models.ScheduledTask{
		ID:             p46ID("task_for_exec"),
		Name:           "contract execution task",
		Enabled:        true,
		ScheduleType:   models.ScheduleTypeCron,
		ScheduleConfig: "0 0 * * *",
		ExecutionType:  models.ExecutionTypeScript,
		ScriptID:       "script-contract",
		ScriptName:     "contract script",
		ResultDir:      "/tmp",
	}
	defer store.DeleteScheduledTask(task.ID)
	if err := store.CreateScheduledTask(task); err != nil {
		t.Fatalf("CreateScheduledTask for task execution contract: %v", err)
	}
	successExec := &models.TaskExecution{
		ID:            p46ID("task_exec_success"),
		TaskID:        task.ID,
		TaskName:      task.Name,
		StartTime:     time.Now().Add(-time.Minute),
		EndTime:       time.Now().Add(-time.Minute).Add(time.Second),
		Success:       true,
		Message:       "target success",
		ResultData:    map[string]interface{}{"count": float64(1)},
		ExecutionType: models.ExecutionTypeScript,
		ScriptID:      task.ScriptID,
	}
	failedExec := &models.TaskExecution{
		ID:            p46ID("task_exec_failed"),
		TaskID:        task.ID,
		TaskName:      task.Name,
		StartTime:     time.Now(),
		EndTime:       time.Now().Add(time.Second),
		Success:       false,
		Message:       "target failure",
		ErrorMsg:      "boom",
		ResultData:    map[string]interface{}{"count": float64(2)},
		ExecutionType: models.ExecutionTypeScript,
		ScriptID:      task.ScriptID,
	}
	defer store.DeleteTaskExecution(successExec.ID)
	defer store.DeleteTaskExecution(failedExec.ID)
	if err := store.CreateTaskExecution(successExec); err != nil {
		t.Fatalf("CreateTaskExecution success through Postgres Store: %v", err)
	}
	if err := store.CreateTaskExecution(failedExec); err != nil {
		t.Fatalf("CreateTaskExecution failed through Postgres Store: %v", err)
	}
	gotTaskExec, err := store.GetTaskExecution(failedExec.ID)
	if err != nil {
		t.Fatalf("GetTaskExecution through Postgres Store: %v", err)
	}
	if gotTaskExec.ResultData["count"] == nil || gotTaskExec.ErrorMsg != "boom" {
		t.Fatalf("task execution result_data/error roundtrip lost payload: %+v", gotTaskExec)
	}
	page, total, err := store.ListTaskExecutionsWithPagination(1, 1, task.ID, "target", "failed")
	if err != nil {
		t.Fatalf("ListTaskExecutionsWithPagination through Postgres Store: %v", err)
	}
	if total != 1 || len(page) != 1 || page[0].ID != failedExec.ID {
		t.Fatalf("task execution pagination/filter returned total=%d page=%+v, want failed execution only", total, page)
	}
	if err := store.BatchDeleteTaskExecutions([]string{failedExec.ID, successExec.ID}); err != nil {
		t.Fatalf("BatchDeleteTaskExecutions through Postgres Store: %v", err)
	}
	if _, err := store.GetTaskExecution(failedExec.ID); err == nil {
		t.Fatalf("BatchDeleteTaskExecutions left failed execution %s", failedExec.ID)
	}
}
`)
}

func TestP46PostgresStoreTestingPlatformBusinessDataAccess(t *testing.T) {
	p46RunStoreProbe(t, `
func TestP46Probe(t *testing.T) {
	cfg := p46Config(p46IsolatedPlayBotDSN(t, "testing_platform"), p46Base64Key(0x5b), "")
	store, cleanup := openStore(t, cfg)
	defer cleanup()
	db := testingPlatformDB(t, store)

	project := models.Project{
		Name:        "p46 testing platform project",
		Description: "P4.6 TestingPlatform root",
	}
	if err := db.Create(&project).Error; err != nil {
		t.Fatalf("create Project through Store TestingPlatform DB: %v", err)
	}
	version := models.ProjectVersion{
		ProjectID:   project.ID,
		VersionName: "v1",
		Description: "original version",
		BaseURL:     "https://example.invalid",
	}
	if err := db.Create(&version).Error; err != nil {
		t.Fatalf("create ProjectVersion through Store TestingPlatform DB: %v", err)
	}
	page := models.TestPage{
		VersionID:   version.ID,
		Name:        "login page",
		Path:        "/login",
		Description: "Login flow",
	}
	if err := db.Create(&page).Error; err != nil {
		t.Fatalf("create TestPage through Store TestingPlatform DB: %v", err)
	}
	pageScript := models.PageScript{
		PageID:            page.ID,
		Name:              "main",
		ActionTrace:       `+"`"+`[{"action":"click","target":"#submit"}]`+"`"+`,
		DOMSnapshot:       `+"`"+`{"url":"https://example.invalid/login"}`+"`"+`,
		RecordingMetaJSON: `+"`"+`{"recording_kind":"business_flow","auth_context":"project_saved"}`+"`"+`,
	}
	if err := db.Create(&pageScript).Error; err != nil {
		t.Fatalf("save PageScript recording through Store TestingPlatform DB: %v", err)
	}
	testCase := models.TestCase{
		PageID:        page.ID,
		Title:         "valid login",
		Description:   "happy path",
		Blueprint:     `+"`"+`{"title":"valid login","description":"happy path","auth_context":"project_saved","steps":[{"action":"navigate","url":"/login"}]}`+"`"+`,
		ScriptContent: "print('do not execute as fallback')",
		Status:        "active",
	}
	if err := db.Create(&testCase).Error; err != nil {
		t.Fatalf("create TestCase through Store TestingPlatform DB: %v", err)
	}
	execution := models.TestExecution{
		TestCaseID:   testCase.ID,
		Status:       "failed",
		ErrorMessage: "assertion failed",
		DurationMs:   1234,
		ReportData:   `+"`"+`{"summary":{"failed_step_index":0},"steps":[{"index":0,"status":"failed"}]}`+"`"+`,
	}
	if err := db.Create(&execution).Error; err != nil {
		t.Fatalf("create TestExecution through Store TestingPlatform DB: %v", err)
	}
	refinement := models.LLMRefinement{
		TestCaseID:        testCase.ID,
		UserPrompt:        "make assertion stricter",
		OriginalBlueprint: testCase.Blueprint,
		RefinedBlueprint:  `+"`"+`{"title":"valid login strict","description":"happy path strict","auth_context":"project_saved","steps":[{"action":"navigate","url":"/login"},{"action":"assert_text","text":"Welcome"}]}`+"`"+`,
		Summary:           "add assertion",
		RiskNotes:         "low",
		Status:            "proposed",
	}
	if err := db.Create(&refinement).Error; err != nil {
		t.Fatalf("create LLMRefinement through Store TestingPlatform DB: %v", err)
	}
	validatedAt := time.Now().Round(time.Microsecond)
	authState := models.ProjectAuthState{
		ProjectID:           project.ID,
		VersionID:           version.ID,
		Name:                "default",
		Status:              "active",
		SchemaVersion:       1,
		StateJSON:           `+"`"+`{"cookies":[{"name":"sid","value":"secret"}],"origins":[{"origin":"https://example.invalid","localStorage":[{"name":"token","value":"secret-token"}]}]}`+"`"+`,
		StateDigest:         "sha256:p46",
		OriginAllowlistJSON: `+"`"+`["https://example.invalid"]`+"`"+`,
		CookieCount:         1,
		OriginCount:         1,
		CapturedURL:         "https://example.invalid/login",
		CapturedPageID:      page.ID,
		CapturedAt:          validatedAt,
		LastValidatedAt:     &validatedAt,
	}
	if err := db.Create(&authState).Error; err != nil {
		t.Fatalf("create ProjectAuthState through Store TestingPlatform DB: %v", err)
	}

	var projects []models.Project
	if err := db.Where("name = ?", project.Name).Order("created_at desc, id desc").Find(&projects).Error; err != nil {
		t.Fatalf("list Project through Store TestingPlatform DB: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != project.ID {
		t.Fatalf("project list returned %+v, want project %d", projects, project.ID)
	}
	if err := db.Model(&models.ProjectVersion{}).Where("id = ? AND project_id = ?", version.ID, project.ID).Updates(map[string]any{
		"description": "updated version",
		"base_url":    "https://updated.example.invalid",
	}).Error; err != nil {
		t.Fatalf("update ProjectVersion through Store TestingPlatform DB: %v", err)
	}
	var updatedVersion models.ProjectVersion
	if err := db.Where("project_id = ? AND id = ?", project.ID, version.ID).First(&updatedVersion).Error; err != nil {
		t.Fatalf("read updated ProjectVersion through Store TestingPlatform DB: %v", err)
	}
	if updatedVersion.BaseURL != "https://updated.example.invalid" {
		t.Fatalf("ProjectVersion update did not persist: %+v", updatedVersion)
	}

	var loadedScript models.PageScript
	if err := db.Where("page_id = ? AND name = ?", page.ID, "main").First(&loadedScript).Error; err != nil {
		t.Fatalf("read PageScript main flow through Store TestingPlatform DB: %v", err)
	}
	if !strings.Contains(loadedScript.RecordingMetaJSON, "\"auth_context\":\"project_saved\"") {
		t.Fatalf("PageScript recording_meta_json was not preserved: %q", loadedScript.RecordingMetaJSON)
	}
	var cases []models.TestCase
	if err := db.Where("page_id = ?", page.ID).Order("updated_at desc, id desc").Find(&cases).Error; err != nil {
		t.Fatalf("list TestCase through Store TestingPlatform DB: %v", err)
	}
	if len(cases) != 1 || cases[0].ID != testCase.ID || cases[0].ScriptContent == "" {
		t.Fatalf("TestCase list/detail data did not roundtrip: %+v", cases)
	}
	if err := db.Model(&models.TestCase{}).Where("id = ? AND page_id = ?", testCase.ID, page.ID).Updates(map[string]any{
		"title":       "valid login strict",
		"description": "happy path strict",
		"blueprint":   refinement.RefinedBlueprint,
	}).Error; err != nil {
		t.Fatalf("update TestCase through Store TestingPlatform DB: %v", err)
	}

	var executions []models.TestExecution
	if err := db.Where("test_case_id = ?", testCase.ID).Order("created_at desc, id desc").Find(&executions).Error; err != nil {
		t.Fatalf("list TestExecution through Store TestingPlatform DB: %v", err)
	}
	if len(executions) != 1 || executions[0].Status != "failed" || !strings.Contains(executions[0].ReportData, "failed_step_index") {
		t.Fatalf("TestExecution list/detail data did not roundtrip: %+v", executions)
	}

	appliedAt := time.Now().Round(time.Microsecond)
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.LLMRefinement{}).Where("id = ? AND test_case_id = ?", refinement.ID, testCase.ID).Updates(map[string]any{
			"status":     "applied",
			"applied_at": &appliedAt,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&models.TestCase{}).Where("id = ?", testCase.ID).Update("blueprint", refinement.RefinedBlueprint).Error
	}); err != nil {
		t.Fatalf("apply LLMRefinement transaction through Store TestingPlatform DB: %v", err)
	}
	discarded := models.LLMRefinement{
		TestCaseID:        testCase.ID,
		UserPrompt:        "discard me",
		OriginalBlueprint: refinement.RefinedBlueprint,
		RefinedBlueprint:  refinement.RefinedBlueprint,
		Summary:           "discarded",
		Status:            "proposed",
	}
	if err := db.Create(&discarded).Error; err != nil {
		t.Fatalf("create discardable LLMRefinement through Store TestingPlatform DB: %v", err)
	}
	if err := db.Model(&models.LLMRefinement{}).Where("id = ? AND test_case_id = ?", discarded.ID, testCase.ID).Update("status", "discarded").Error; err != nil {
		t.Fatalf("discard LLMRefinement through Store TestingPlatform DB: %v", err)
	}
	var refinements []models.LLMRefinement
	if err := db.Where("test_case_id = ?", testCase.ID).Order("created_at desc, id desc").Find(&refinements).Error; err != nil {
		t.Fatalf("list LLMRefinement through Store TestingPlatform DB: %v", err)
	}
	if len(refinements) != 2 || !containsRefinementStatus(refinements, "applied") || !containsRefinementStatus(refinements, "discarded") {
		t.Fatalf("LLMRefinement list/apply/discard data did not roundtrip: %+v", refinements)
	}

	var activeAuth models.ProjectAuthState
	if err := db.Where("project_id = ? AND version_id = ? AND status = ?", project.ID, version.ID, "active").First(&activeAuth).Error; err != nil {
		t.Fatalf("read active ProjectAuthState through Store TestingPlatform DB: %v", err)
	}
	if activeAuth.CookieCount != 1 || activeAuth.OriginCount != 1 || activeAuth.StateJSON == "" || activeAuth.CapturedPageID != page.ID {
		t.Fatalf("ProjectAuthState active row did not roundtrip: %+v", activeAuth)
	}

	var clonedVersion models.ProjectVersion
	if err := db.Transaction(func(tx *gorm.DB) error {
		clonedVersion = models.ProjectVersion{
			ProjectID:   project.ID,
			VersionName: "v1 clone",
			Description: "clone version",
			BaseURL:     updatedVersion.BaseURL,
		}
		if err := tx.Create(&clonedVersion).Error; err != nil {
			return err
		}
		clonedPage := models.TestPage{
			VersionID:   clonedVersion.ID,
			Name:        page.Name,
			Path:        page.Path,
			Description: page.Description,
		}
		return tx.Create(&clonedPage).Error
	}); err != nil {
		t.Fatalf("clone ProjectVersion through Store TestingPlatform DB: %v", err)
	}
	var clonedPageCount int64
	if err := db.Model(&models.TestPage{}).Where("version_id = ?", clonedVersion.ID).Count(&clonedPageCount).Error; err != nil {
		t.Fatalf("count cloned TestPage through Store TestingPlatform DB: %v", err)
	}
	if clonedPageCount != 1 {
		t.Fatalf("cloned ProjectVersion page count = %d, want 1", clonedPageCount)
	}

	if err := db.Delete(&models.Project{}, project.ID).Error; err != nil {
		t.Fatalf("delete Project through Store TestingPlatform DB: %v", err)
	}
	requireGormCount(t, db, &models.ProjectVersion{}, "project_id = ?", []any{project.ID}, 0)
	requireGormCount(t, db, &models.TestPage{}, "version_id IN ?", []any{[]uint{version.ID, clonedVersion.ID}}, 0)
	requireGormCount(t, db, &models.PageScript{}, "page_id = ?", []any{page.ID}, 0)
	requireGormCount(t, db, &models.TestCase{}, "page_id = ?", []any{page.ID}, 0)
	requireGormCount(t, db, &models.TestExecution{}, "test_case_id = ?", []any{testCase.ID}, 0)
	requireGormCount(t, db, &models.LLMRefinement{}, "test_case_id = ?", []any{testCase.ID}, 0)
	requireGormCount(t, db, &models.ProjectAuthState{}, "project_id = ?", []any{project.ID}, 0)
}
`)
}

type inventoryParseResult struct {
	entries map[string]inventoryEntry
}

type inventoryEntry struct {
	Domain         string
	Method         string
	LegacySource   string
	PostgresTarget string
	ContractTest   string
}

type fieldInfo struct {
	Name string
	Tag  string
}

func p46LegacyBoltMethodBaseline() map[string]struct{} {
	methods := map[string]struct{}{}
	for _, method := range []string{
		"SaveCookies", "GetCookies", "DeleteCookies",
		"SaveScript", "GetScript", "ListScripts", "UpdateScript", "DeleteScript",
		"SaveLLMConfig", "GetLLMConfig", "ListLLMConfigs", "UpdateLLMConfig", "DeleteLLMConfig", "GetDefaultLLMConfig", "ClearDefaultLLMConfig",
		"SaveBrowserConfig", "GetBrowserConfig", "GetDefaultBrowserConfig", "ListBrowserConfigs", "DeleteBrowserConfig",
		"SavePrompt", "GetPrompt", "ListPrompts", "UpdatePrompt", "DeletePrompt", "CheckAndUpdateSystemPrompts",
		"SaveScriptExecution", "GetScriptExecution", "GetLatestScriptExecutionByScriptID", "ListScriptExecutions", "DeleteScriptExecution", "DeleteScriptExecutionsByScriptID",
		"SaveRecordingConfig", "GetRecordingConfig", "GetDefaultRecordingConfig",
		"SaveAgentSession", "GetAgentSession", "ListAgentSessions", "DeleteAgentSession",
		"SaveAgentMessage", "GetAgentMessage", "ListAgentMessages",
		"SaveToolConfig", "GetToolConfig", "ListToolConfigs", "DeleteToolConfig", "DeleteToolConfigByScriptID",
		"SaveMCPService", "GetMCPService", "ListMCPServices", "DeleteMCPService", "SaveMCPServiceTools", "GetMCPServiceTools",
		"CreateUser", "GetUser", "GetUserByUsername", "ListUsers", "UpdateUser", "DeleteUser",
		"CreateApiKey", "GetApiKey", "GetApiKeyByKey", "ListApiKeys", "ListApiKeysByUser", "UpdateApiKey", "DeleteApiKey",
		"SaveBrowserInstance", "GetBrowserInstance", "ListBrowserInstances", "GetDefaultBrowserInstance", "UpdateBrowserInstance", "DeleteBrowserInstance",
		"CreateScheduledTask", "GetScheduledTask", "UpdateScheduledTask", "DeleteScheduledTask", "ListScheduledTasks", "ListScheduledTasksWithPagination",
		"CreateTaskExecution", "GetTaskExecution", "DeleteTaskExecution", "ListTaskExecutions", "ListTaskExecutionsWithPagination", "BatchDeleteTaskExecutions",
	} {
		methods[method] = struct{}{}
	}
	return methods
}

func backendRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	return root
}

func writeP46Config(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		t.Fatalf("write temp P4.6 config: %v", err)
	}
	return path
}

func p46Base64Key(fill byte) string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill
	}
	return base64.StdEncoding.EncodeToString(key)
}

func p46RunStoreProbe(t *testing.T, probeTest string) {
	t.Helper()
	var failures []string
	for _, variant := range p46StoreProbeOpenVariants() {
		dir := t.TempDir()
		module := fmt.Sprintf(`module browserwing_p46_probe

go 1.25.0

require github.com/browserwing/browserwing v0.0.0

replace github.com/browserwing/browserwing => %s
`, filepath.ToSlash(backendRoot(t)))
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o644); err != nil {
			t.Fatalf("write probe go.mod: %v", err)
		}
		if sum, err := os.ReadFile(filepath.Join(backendRoot(t), "go.sum")); err == nil {
			if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o644); err != nil {
				t.Fatalf("write probe go.sum: %v", err)
			}
		}
		source := p46StoreProbeSource(variant.code, probeTest)
		if err := os.WriteFile(filepath.Join(dir, "p46_contract_test.go"), []byte(source), 0o644); err != nil {
			t.Fatalf("write probe test: %v", err)
		}

		cmd := exec.Command("go", "test", "-mod=mod", ".", "-run", "TestP46Probe", "-count=1")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		output := out.String()
		if err == nil {
			return
		}
		if !p46ProbeLooksLikeUnsupportedSignature(output) {
			t.Fatalf("P4.6 production Store probe failed using %s:\n%s", variant.name, output)
		}
		failures = append(failures, variant.name+":\n"+output)
	}

	t.Fatalf("P4.6 requires a production PostgreSQL Store startup entry that tests can exercise through storage.Store. Tried supported signatures:\n%s", firstLines(failures, 6))
}

type p46StoreProbeOpenVariant struct {
	name string
	code string
}

func p46StoreProbeOpenVariants() []p46StoreProbeOpenVariant {
	return []p46StoreProbeOpenVariant{
		{
			name: "storage.OpenPostgresStore(context.Context, *config.Config) (storage.Store, func() error, error)",
			code: `
func openStore(t *testing.T, cfg *config.Config) (storage.Store, func()) {
	t.Helper()
	store, cleanup, err := storage.OpenPostgresStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open PostgreSQL Store startup entry: %v", err)
	}
	return store, func() {
		if cleanup != nil {
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup PostgreSQL Store: %v", err)
			}
		}
	}
}

func expectOpenStoreError(t *testing.T, cfg *config.Config) error {
	t.Helper()
	_, cleanup, err := storage.OpenPostgresStore(context.Background(), cfg)
	if cleanup != nil {
		_ = cleanup()
	}
	return err
}
`,
		},
		{
			name: "storage.OpenPostgresStore(context.Context, *config.Config) (storage.Store, error)",
			code: `
func openStore(t *testing.T, cfg *config.Config) (storage.Store, func()) {
	t.Helper()
	store, err := storage.OpenPostgresStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open PostgreSQL Store startup entry: %v", err)
	}
	return store, func() {}
}

func expectOpenStoreError(t *testing.T, cfg *config.Config) error {
	t.Helper()
	_, err := storage.OpenPostgresStore(context.Background(), cfg)
	return err
}
`,
		},
		{
			name: "storage.NewStore(context.Context, *config.Config) (storage.Store, func() error, error)",
			code: `
func openStore(t *testing.T, cfg *config.Config) (storage.Store, func()) {
	t.Helper()
	store, cleanup, err := storage.NewStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open PostgreSQL Store startup entry: %v", err)
	}
	return store, func() {
		if cleanup != nil {
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup PostgreSQL Store: %v", err)
			}
		}
	}
}

func expectOpenStoreError(t *testing.T, cfg *config.Config) error {
	t.Helper()
	_, cleanup, err := storage.NewStore(context.Background(), cfg)
	if cleanup != nil {
		_ = cleanup()
	}
	return err
}
`,
		},
		{
			name: "storage.NewStore(context.Context, *config.Config) (storage.Store, error)",
			code: `
func openStore(t *testing.T, cfg *config.Config) (storage.Store, func()) {
	t.Helper()
	store, err := storage.NewStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open PostgreSQL Store startup entry: %v", err)
	}
	return store, func() {}
}

func expectOpenStoreError(t *testing.T, cfg *config.Config) error {
	t.Helper()
	_, err := storage.NewStore(context.Background(), cfg)
	return err
}
`,
		},
		{
			name: "storage.NewPostgresStore(context.Context, *config.Config) (storage.Store, func() error, error)",
			code: `
func openStore(t *testing.T, cfg *config.Config) (storage.Store, func()) {
	t.Helper()
	store, cleanup, err := storage.NewPostgresStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open PostgreSQL Store startup entry: %v", err)
	}
	return store, func() {
		if cleanup != nil {
			if err := cleanup(); err != nil {
				t.Fatalf("cleanup PostgreSQL Store: %v", err)
			}
		}
	}
}

func expectOpenStoreError(t *testing.T, cfg *config.Config) error {
	t.Helper()
	_, cleanup, err := storage.NewPostgresStore(context.Background(), cfg)
	if cleanup != nil {
		_ = cleanup()
	}
	return err
}
`,
		},
		{
			name: "storage.NewPostgresStore(context.Context, *config.Config) (storage.Store, error)",
			code: `
func openStore(t *testing.T, cfg *config.Config) (storage.Store, func()) {
	t.Helper()
	store, err := storage.NewPostgresStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open PostgreSQL Store startup entry: %v", err)
	}
	return store, func() {}
}

func expectOpenStoreError(t *testing.T, cfg *config.Config) error {
	t.Helper()
	_, err := storage.NewPostgresStore(context.Background(), cfg)
	return err
}
`,
		},
	}
}

func p46ProbeLooksLikeUnsupportedSignature(output string) bool {
	for _, marker := range []string{
		"undefined:",
		"assignment mismatch:",
		"not enough arguments in call to",
		"too many arguments in call to",
		"unknown field",
		"cannot use",
		"not in selector",
	} {
		if strings.Contains(output, marker) {
			return true
		}
	}
	return false
}

func p46StoreProbeSource(openStoreCode, probeTest string) string {
	return `package p46probe

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/llm"
	"github.com/browserwing/browserwing/models"
	"github.com/browserwing/browserwing/services/playbot"
	"github.com/browserwing/browserwing/storage"
	"github.com/go-rod/rod/lib/proto"
	"gorm.io/gorm"
)

var _ = models.Script{}
var _ = playbot.GenerateOptions{}
var _ = proto.NetworkCookie{}
var _ = gorm.DB{}

` + openStoreCode + `

func p46Config(dsn, encryptionKey, llmAPIKey string) *config.Config {
	cfg := &config.Config{}
	root := reflect.ValueOf(cfg).Elem()

	server := newConfigSection(root.FieldByName("Server"))
	setStringField(server, "Host", "127.0.0.1")
	setStringField(server, "Port", "0")
	assignConfigSection(root.FieldByName("Server"), server)

	database := newConfigSection(root.FieldByName("Database"))
	setStringField(database, "Type", "postgres")
	setStringField(database, "DSN", dsn)
	assignConfigSection(root.FieldByName("Database"), database)

	security := newConfigSection(root.FieldByName("Security"))
	setStringField(security, "LLMAPIKeyEncryptionKey", encryptionKey)
	assignConfigSection(root.FieldByName("Security"), security)

	llm := config.LLMConfig{
		Name:     "p46-contract-default",
		Provider: "openai",
		APIKey:   llmAPIKey,
		Model:    "gpt-4o-mini",
		BaseURL:  "https://api.example.invalid",
	}
	cfg.LLMs = []config.LLMConfig{llm}

	browser := newConfigSection(root.FieldByName("Browser"))
	setStringField(browser, "BinPath", "chrome")
	setStringField(browser, "UserDataDir", "./p46-contract-browser")
	assignConfigSection(root.FieldByName("Browser"), browser)

	auth := newConfigSection(root.FieldByName("Auth"))
	setBoolField(auth, "Enabled", false)
	setStringField(auth, "AppKey", "p46-contract-auth-key")
	setStringField(auth, "DefaultUsername", "admin")
	setStringField(auth, "DefaultPassword", "admin123")
	assignConfigSection(root.FieldByName("Auth"), auth)

	return cfg
}

func newConfigSection(field reflect.Value) reflect.Value {
	if !field.IsValid() {
		panic("missing config section")
	}
	if field.Kind() == reflect.Ptr {
		return reflect.New(field.Type().Elem()).Elem()
	}
	return reflect.New(field.Type()).Elem()
}

func assignConfigSection(field, value reflect.Value) {
	if field.Kind() == reflect.Ptr {
		field.Set(value.Addr())
		return
	}
	field.Set(value)
}

func setStringField(section reflect.Value, name, value string) {
	field := section.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.String {
		field.SetString(value)
	}
}

func setBoolField(section reflect.Value, name string, value bool) {
	field := section.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.Bool {
		field.SetBool(value)
	}
}

func configSection(cfg *config.Config, name string) reflect.Value {
	field := reflect.ValueOf(cfg).Elem().FieldByName(name)
	if !field.IsValid() {
		panic("missing config section " + name)
	}
	if field.Kind() == reflect.Ptr {
		if field.IsNil() {
			field.Set(reflect.New(field.Type().Elem()))
		}
		return field.Elem()
	}
	return field
}

func setDatabaseType(cfg *config.Config, value string) {
	setStringField(configSection(cfg, "Database"), "Type", value)
}

func databaseDSN(cfg *config.Config) string {
	field := configSection(cfg, "Database").FieldByName("DSN")
	if field.IsValid() && field.Kind() == reflect.String {
		return field.String()
	}
	return ""
}

func setAuthString(cfg *config.Config, name, value string) {
	setStringField(configSection(cfg, "Auth"), name, value)
}

func setAuthBool(cfg *config.Config, name string, value bool) {
	setBoolField(configSection(cfg, "Auth"), name, value)
}

func p46ContractDSN(t *testing.T) string {
	t.Helper()
	if dsn := strings.TrimSpace(os.Getenv("BROWSERWING_P46_POSTGRES_DSN")); dsn != "" {
		return dsn
	}
	t.Fatalf("P4.6 PostgreSQL Store behavior tests require BROWSERWING_P46_POSTGRES_DSN targeting database PlayBot")
	return ""
}

func p46NonPlayBotDSN(t *testing.T) string {
	t.Helper()
	const wrongDatabase = "PlayBot_contract_reject"
	dsn := p46ContractDSN(t)
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("parse PostgreSQL DSN for non-PlayBot contract: %v", err)
		}
		parsed.Path = "/" + wrongDatabase
		return parsed.String()
	}
	fields := strings.Fields(dsn)
	replaced := false
	for i, field := range fields {
		key, _, ok := strings.Cut(field, "=")
		if ok && key == "dbname" {
			fields[i] = "dbname=" + wrongDatabase
			replaced = true
			break
		}
	}
	if !replaced {
		fields = append(fields, "dbname="+wrongDatabase)
	}
	return strings.Join(fields, " ")
}

func p46IsolatedPlayBotDSN(t *testing.T, label string) string {
	t.Helper()
	baseDSN := p46ContractDSN(t)
	schema := p46ID("schema_" + label)
	cfg := p46Config(baseDSN, p46Base64Key(0x4f), "")
	db := openRawDB(t, cfg)
	defer db.Close()
	if _, err := db.Exec("CREATE SCHEMA " + quoteIdentifier(schema)); err != nil {
		t.Fatalf("create isolated PostgreSQL schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		cleanupDB := openRawDB(t, cfg)
		defer cleanupDB.Close()
		if _, err := cleanupDB.Exec("DROP SCHEMA IF EXISTS " + quoteIdentifier(schema) + " CASCADE"); err != nil {
			t.Fatalf("drop isolated PostgreSQL schema %s: %v", schema, err)
		}
	})
	return withSearchPath(t, baseDSN, schema)
}

func withSearchPath(t *testing.T, rawDSN, schema string) string {
	t.Helper()
	option := "-c search_path=" + schema
	if strings.Contains(rawDSN, "://") {
		parsed, err := url.Parse(rawDSN)
		if err != nil {
			t.Fatalf("parse PostgreSQL DSN for isolated schema: %v", err)
		}
		query := parsed.Query()
		if existing := strings.TrimSpace(query.Get("options")); existing != "" {
			option = existing + " " + option
		}
		query.Set("options", option)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(rawDSN) + " options='" + option + "'"
}

func quoteIdentifier(value string) string {
	return "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
}

func p46Base64Key(fill byte) string {
	key := make([]byte, 32)
	for i := range key {
		key[i] = fill
	}
	return base64.StdEncoding.EncodeToString(key)
}

func p46ID(prefix string) string {
	safe := strings.NewReplacer("-", "_", " ", "_", "/", "_").Replace(prefix)
	return fmt.Sprintf("p46_%s_%d", safe, time.Now().UnixNano())
}

func openRawDB(t *testing.T, cfg *config.Config) *sql.DB {
	t.Helper()
	var failures []string
	for _, driver := range []string{"postgres", "pgx"} {
		db, err := sql.Open(driver, databaseDSN(cfg))
		if err != nil {
			failures = append(failures, driver+": "+err.Error())
			continue
		}
		if err := db.Ping(); err != nil {
			db.Close()
			failures = append(failures, driver+": "+err.Error())
			continue
		}
		return db
	}
	t.Fatalf("open PostgreSQL PlayBot raw DB for at-rest assertions; failures:\n%s", strings.Join(failures, "\n"))
	return nil
}

func testingPlatformDB(t *testing.T, store storage.Store) *gorm.DB {
	t.Helper()
	gormDBType := reflect.TypeOf(&gorm.DB{})
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	for _, name := range []string{"GormDB", "GORMDB", "TestingPlatformDB", "DB"} {
		method := reflect.ValueOf(store).MethodByName(name)
		if !method.IsValid() || method.Type().NumIn() != 0 {
			continue
		}
		if method.Type().NumOut() == 1 && method.Type().Out(0) == gormDBType {
			db, _ := method.Call(nil)[0].Interface().(*gorm.DB)
			if db == nil {
				t.Fatalf("%s returned nil *gorm.DB", name)
			}
			return db
		}
		if method.Type().NumOut() == 2 && method.Type().Out(0) == gormDBType && method.Type().Out(1).Implements(errorType) {
			out := method.Call(nil)
			if !out[1].IsNil() {
				err, _ := out[1].Interface().(error)
				t.Fatalf("%s returned error: %v", name, err)
			}
			db, _ := out[0].Interface().(*gorm.DB)
			if db == nil {
				t.Fatalf("%s returned nil *gorm.DB", name)
			}
			return db
		}
	}
	t.Fatalf("P4.6 TestingPlatformStore must expose a controlled *gorm.DB entry such as GormDB(), TestingPlatformDB(), or DB() for P1-P4.5 business data access")
	return nil
}

func requireGormCount(t *testing.T, db *gorm.DB, model any, query string, args []any, want int64) {
	t.Helper()
	var count int64
	if err := db.Model(model).Where(query, args...).Count(&count).Error; err != nil {
		t.Fatalf("count %T where %q: %v", model, query, err)
	}
	if count != want {
		t.Fatalf("count %T where %q = %d, want %d", model, query, count, want)
	}
}

func containsRefinementStatus(refinements []models.LLMRefinement, status string) bool {
	for _, refinement := range refinements {
		if refinement.Status == status {
			return true
		}
	}
	return false
}

func extractorAPIKey(t *testing.T, extractor *llm.Extractor) string {
	t.Helper()
	if extractor == nil {
		t.Fatalf("nil LLM extractor")
	}
	value := reflect.ValueOf(extractor)
	if value.Kind() != reflect.Ptr || value.IsNil() {
		t.Fatalf("unexpected LLM extractor value: %T", extractor)
	}
	configField := value.Elem().FieldByName("config")
	if !configField.IsValid() || configField.IsNil() {
		t.Fatalf("LLM extractor does not hold a runtime config")
	}
	configValue := reflect.NewAt(configField.Type(), unsafe.Pointer(configField.UnsafeAddr())).Elem()
	cfg, ok := configValue.Interface().(*config.LLMConfig)
	if !ok || cfg == nil {
		t.Fatalf("unexpected LLM extractor config field type: %s", configField.Type())
	}
	return cfg.APIKey
}

func writeFailingFakePlaybot(t *testing.T, secret string) (pythonPath string, engineDir string, jobFile string, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	engineDir = filepath.Join(dir, "engine")
	if err := os.MkdirAll(engineDir, 0o755); err != nil {
		t.Fatalf("create fake Playbot engine dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(engineDir, "cli.py"), []byte("# fake cli marker\n"), 0o644); err != nil {
		t.Fatalf("write fake Playbot cli.py: %v", err)
	}
	jobFile = filepath.Join(dir, "job.json")
	argsFile = filepath.Join(dir, "args.txt")
	t.Setenv("BROWSERWING_P46_PLAYBOT_JOB_FILE", jobFile)
	t.Setenv("BROWSERWING_P46_PLAYBOT_ARGS_FILE", argsFile)
	t.Setenv("BROWSERWING_P46_PLAYBOT_SECRET", secret)

	if runtime.GOOS == "windows" {
		pythonPath = filepath.Join(dir, "fake-playbot.cmd")
		script := strings.Join([]string{
			"@echo off",
			"setlocal EnableDelayedExpansion",
			"set \"INPUT=\"",
			"set \"ARGS=\"",
			":loop",
			"if \"%~1\"==\"\" goto done",
			"set \"ARGS=!ARGS! %~1\"",
			"if \"%~1\"==\"--input\" (",
			"  shift",
			"  set \"ARGS=!ARGS! %~1\"",
			"  set \"INPUT=%~1\"",
			")",
			"shift",
			"goto loop",
			":done",
			"> \"%BROWSERWING_P46_PLAYBOT_ARGS_FILE%\" echo(!ARGS!",
			"copy /Y \"%INPUT%\" \"%BROWSERWING_P46_PLAYBOT_JOB_FILE%\" >nul",
			">&2 echo fake stderr leaked %BROWSERWING_P46_PLAYBOT_SECRET%",
			"exit /b 7",
		}, "\r\n") + "\r\n"
		if err := os.WriteFile(pythonPath, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake Playbot command: %v", err)
		}
		return pythonPath, engineDir, jobFile, argsFile
	}

	pythonPath = filepath.Join(dir, "fake-playbot")
	script := strings.Join([]string{
		"#!/bin/sh",
		"input=\"\"",
		"args=\"\"",
		"while [ \"$#\" -gt 0 ]; do",
		"  args=\"$args $1\"",
		"  if [ \"$1\" = \"--input\" ]; then",
		"    shift",
		"    args=\"$args $1\"",
		"    input=\"$1\"",
		"  fi",
		"  shift",
		"done",
		"printf \"%s\\n\" \"$args\" > \"$BROWSERWING_P46_PLAYBOT_ARGS_FILE\"",
		"cp \"$input\" \"$BROWSERWING_P46_PLAYBOT_JOB_FILE\"",
		"echo \"fake stderr leaked $BROWSERWING_P46_PLAYBOT_SECRET\" >&2",
		"exit 7",
	}, "\n") + "\n"
	if err := os.WriteFile(pythonPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake Playbot command: %v", err)
	}
	return pythonPath, engineDir, jobFile, argsFile
}

func columnType(t *testing.T, db *sql.DB, table, column string) string {
	t.Helper()
	var dataType string
	err := db.QueryRow(` + "`" + `
		SELECT data_type
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND column_name = $2
	` + "`" + `, table, column).Scan(&dataType)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatalf("query information_schema for %s.%s: %v", table, column, err)
	}
	return dataType
}

func requireColumn(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	if got := columnType(t, db, table, column); got == "" {
		t.Fatalf("PostgreSQL table %s missing required column %s", table, column)
	}
}

func requireColumnAbsent(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	if got := columnType(t, db, table, column); got != "" {
		t.Fatalf("PostgreSQL table %s must not have plaintext column %s; found data_type=%s", table, column, got)
	}
}

func requireColumnType(t *testing.T, db *sql.DB, table, column, want string) {
	t.Helper()
	got := columnType(t, db, table, column)
	if got != want {
		t.Fatalf("PostgreSQL column %s.%s data_type = %q, want %q", table, column, got, want)
	}
}

func queryLLMAtRestPayload(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var payload string
	if err := db.QueryRow(` + "`" + `
		SELECT coalesce(api_key_ciphertext, '') || ' ' || coalesce(api_key_nonce, '') || ' ' || coalesce(api_key_key_id, '')
		FROM llm_configs WHERE id = $1
	` + "`" + `, id).Scan(&payload); err != nil {
		t.Fatalf("query LLM config at-rest encrypted fields: %v", err)
	}
	return payload
}

func requireEmptyIfTableExists(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var exists bool
	if err := db.QueryRow(` + "`" + `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = $1
		)
	` + "`" + `, table).Scan(&exists); err != nil {
		t.Fatalf("check if table %s exists: %v", table, err)
	}
	if !exists {
		return
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("count rows in %s: %v", table, err)
	}
	if count != 0 {
		t.Fatalf("P4.6 empty PlayBot startup seed contract requires %s to be empty before startup; found %d rows", table, count)
	}
}

` + probeTest
}

func parseStoreOperationInventory(t *testing.T, root string) inventoryParseResult {
	t.Helper()
	result := inventoryParseResult{entries: map[string]inventoryEntry{}}

	for _, file := range productionGoFiles(t, root) {
		parsed := parseGoFile(t, file)
		ast.Inspect(parsed, func(node ast.Node) bool {
			composite, ok := node.(*ast.CompositeLit)
			if !ok {
				return true
			}
			entry := inventoryEntry{}
			for _, elt := range composite.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				value := stringLiteral(kv.Value)
				switch key.Name {
				case "Domain":
					entry.Domain = value
				case "Method":
					entry.Method = value
				case "LegacySource":
					entry.LegacySource = value
				case "PostgresTarget":
					entry.PostgresTarget = value
				case "ContractTest":
					entry.ContractTest = value
				}
			}
			if entry.Method != "" {
				result.entries[entry.Method] = entry
			}
			return true
		})
	}
	return result
}

func parseP46ContractTestFunctions(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	parsed := parseGoFile(t, filepath.Join(root, "p46_postgres_contract_test.go"))
	tests := map[string]struct{}{}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if strings.HasPrefix(fn.Name.Name, "TestP46") {
			tests[fn.Name.Name] = struct{}{}
		}
	}
	if len(tests) == 0 {
		t.Fatalf("no TestP46 contract tests found in p46_postgres_contract_test.go")
	}
	return tests
}

func parseInterfaceMethods(t *testing.T, dir, interfaceName string) map[string]struct{} {
	t.Helper()
	index := map[string]*ast.InterfaceType{}
	for _, file := range productionGoFiles(t, dir) {
		parsed := parseGoFile(t, file)
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				iface, ok := typeSpec.Type.(*ast.InterfaceType)
				if !ok {
					continue
				}
				index[typeSpec.Name.Name] = iface
			}
		}
	}
	methods := map[string]struct{}{}
	visited := map[string]bool{}
	collectInterfaceMethods(index, interfaceName, methods, visited)
	return methods
}

func collectInterfaceMethods(index map[string]*ast.InterfaceType, interfaceName string, methods map[string]struct{}, visited map[string]bool) {
	if visited[interfaceName] {
		return
	}
	visited[interfaceName] = true
	iface := index[interfaceName]
	if iface == nil {
		return
	}
	for _, method := range iface.Methods.List {
		if len(method.Names) == 0 {
			embeddedName := embeddedInterfaceName(method.Type)
			if embeddedName != "" {
				collectInterfaceMethods(index, embeddedName, methods, visited)
			}
			continue
		}
		for _, name := range method.Names {
			if ast.IsExported(name.Name) {
				methods[name.Name] = struct{}{}
			}
		}
	}
}

func embeddedInterfaceName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	default:
		return ""
	}
}

func productionGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor", "dist", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && defaultBuildIncludesFile(t, filepath.Dir(path), filepath.Base(path)) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk production go files under %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}

func defaultBuildIncludesFile(t *testing.T, dir, name string) bool {
	t.Helper()
	ok, err := build.Default.MatchFile(dir, name)
	if err != nil {
		t.Fatalf("evaluate build constraints for %s: %v", filepath.Join(dir, name), err)
	}
	return ok
}

func parseGoFile(t *testing.T, file string) *ast.File {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return parsed
}

func structFields(t *testing.T, parsed *ast.File, structName string) map[string]fieldInfo {
	t.Helper()
	fields, ok := allStructFields(parsed)[structName]
	if !ok {
		return map[string]fieldInfo{}
	}
	return fields
}

func allStructFields(parsed *ast.File) map[string]map[string]fieldInfo {
	all := map[string]map[string]fieldInfo{}
	fields := map[string]fieldInfo{}
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			st, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields = map[string]fieldInfo{}
			for _, field := range st.Fields.List {
				tag := ""
				if field.Tag != nil {
					tag = field.Tag.Value
				}
				for _, name := range field.Names {
					fields[name.Name] = fieldInfo{Name: name.Name, Tag: tag}
				}
			}
			all[typeSpec.Name.Name] = fields
		}
	}
	return all
}

func structFieldsByTomlTag(t *testing.T, parsed *ast.File, structName string) map[string]fieldInfo {
	t.Helper()
	result := map[string]fieldInfo{}
	for _, field := range structFields(t, parsed, structName) {
		tag := strings.Trim(field.Tag, "`")
		tomlTag := reflectStructTagGet(tag, "toml")
		if tomlTag == "" || tomlTag == "-" {
			continue
		}
		result[strings.Split(tomlTag, ",")[0]] = field
	}
	return result
}

func reflectStructTagGet(tag, key string) string {
	for tag != "" {
		tag = strings.TrimLeft(tag, " ")
		if tag == "" {
			break
		}
		i := strings.Index(tag, ":")
		if i < 0 {
			break
		}
		name := tag[:i]
		tag = tag[i+1:]
		if !strings.HasPrefix(tag, "\"") {
			break
		}
		value, rest, ok := scanStructTagValue(tag)
		if !ok {
			break
		}
		if name == key {
			return value
		}
		tag = rest
	}
	return ""
}

func scanStructTagValue(tag string) (value string, rest string, ok bool) {
	for i := 1; i < len(tag); i++ {
		if tag[i] == '"' && tag[i-1] != '\\' {
			raw := tag[:i+1]
			unquoted, err := strconv.Unquote(raw)
			if err != nil {
				return "", "", false
			}
			return unquoted, tag[i+1:], true
		}
	}
	return "", "", false
}

func stringLiteral(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return value
}

func dsnTargetsPlayBot(dsn string) bool {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return false
	}
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return false
		}
		dbName := strings.TrimPrefix(parsed.EscapedPath(), "/")
		unescaped, err := url.PathUnescape(dbName)
		if err != nil {
			return false
		}
		return unescaped == "PlayBot"
	}
	for _, field := range strings.Fields(dsn) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key != "dbname" {
			continue
		}
		value = strings.Trim(value, `"'`)
		return value == "PlayBot"
	}
	return false
}

func fieldMarkedNonPersistent(loweredTag string) bool {
	return strings.Contains(loweredTag, `gorm:"-"`)
}

func isLLMPersistenceStruct(structName string, fields map[string]fieldInfo) bool {
	lowered := strings.ToLower(structName)
	if strings.Contains(lowered, "llmconfigmodel") ||
		strings.Contains(lowered, "llmconfigrow") ||
		strings.Contains(lowered, "llmconfigrecord") ||
		strings.Contains(lowered, "postgresllmconfig") {
		return true
	}
	_, hasProvider := fields["Provider"]
	_, hasModel := fields["Model"]
	_, hasDefault := fields["IsDefault"]
	_, hasActive := fields["IsActive"]
	if hasProvider && hasModel && hasDefault && hasActive {
		for _, field := range fields {
			if strings.Contains(strings.ToLower(field.Tag), "gorm:") {
				return true
			}
		}
	}
	return false
}

func relPath(t *testing.T, root, file string) string {
	t.Helper()
	rel, err := filepath.Rel(root, file)
	if err != nil {
		t.Fatalf("rel path for %s: %v", file, err)
	}
	return filepath.ToSlash(rel)
}

func sortedStrings(values []string) []string {
	cp := append([]string(nil), values...)
	sort.Strings(cp)
	return cp
}

func firstLines(values []string, limit int) string {
	if len(values) <= limit {
		return strings.Join(values, "\n")
	}
	return strings.Join(values[:limit], "\n") + "\n... truncated " + strconv.Itoa(len(values)-limit) + " more"
}
