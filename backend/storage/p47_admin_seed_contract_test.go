package storage

import (
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/models"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var p47StorageContractSequence uint64

func TestP47PostgresSeedDefaultUserCreatesAndPreservesAdminAccess(t *testing.T) {
	db := newP47StorageContractDB(t)
	store := &PostgresStore{db: db}
	if err := store.autoMigrate(); err != nil {
		t.Fatalf("migrate P4.7 storage contract schema: %v", err)
	}
	cfg := p47StorageAuthConfig("p47-default-admin")

	if err := store.seedDefaultUser(cfg); err != nil {
		t.Fatalf("seed default user: %v", err)
	}
	seeded, err := store.GetUserByUsername("p47-default-admin")
	if err != nil {
		t.Fatalf("load seeded default user: %v", err)
	}
	if !p47StorageUserIsAdmin(t, seeded) {
		t.Fatalf("seeded default user IsAdmin = false, want true")
	}

	ordinary := &models.User{
		ID:        "p47-ordinary-user",
		Username:  "p47-ordinary-user",
		Password:  "ordinary-password",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateUser(ordinary); err != nil {
		t.Fatalf("create ordinary user: %v", err)
	}
	loadedOrdinary, err := store.GetUserByUsername(ordinary.Username)
	if err != nil {
		t.Fatalf("load ordinary user: %v", err)
	}
	if p47StorageUserIsAdmin(t, loadedOrdinary) {
		t.Fatalf("ordinary newly created user IsAdmin = true, want default false")
	}

	upgradeDB := newP47StorageContractDB(t)
	upgradeStore := &PostgresStore{db: upgradeDB}
	if err := upgradeStore.autoMigrate(); err != nil {
		t.Fatalf("migrate P4.7 upgrade contract schema: %v", err)
	}
	existingDefault := &models.User{
		ID:        "p47-existing-default",
		Username:  "p47-existing-default",
		Password:  "existing-password",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	p47StorageSetUserAdmin(t, existingDefault, false)
	if err := upgradeStore.CreateUser(existingDefault); err != nil {
		t.Fatalf("seed existing non-admin default user: %v", err)
	}
	upgradeCfg := p47StorageAuthConfig(existingDefault.Username)
	if err := upgradeStore.seedDefaultUser(upgradeCfg); err != nil {
		t.Fatalf("seed default user for upgrade path: %v", err)
	}
	promoted, err := upgradeStore.GetUserByUsername(existingDefault.Username)
	if err != nil {
		t.Fatalf("load promoted default user: %v", err)
	}
	if !p47StorageUserIsAdmin(t, promoted) {
		t.Fatalf("existing default user was not promoted to admin when no admins exist")
	}
}

func p47StorageAuthConfig(username string) *config.Config {
	return &config.Config{
		Auth: &config.AuthConfig{
			Enabled:         true,
			DefaultUsername: username,
			DefaultPassword: "contract-password",
		},
	}
}

func p47StorageUserIsAdmin(t *testing.T, user *models.User) bool {
	t.Helper()
	value := reflect.ValueOf(user).Elem().FieldByName("IsAdmin")
	if !value.IsValid() {
		t.Fatalf("models.User missing IsAdmin; P4.7 default seed contract requires production admin flag")
	}
	if value.Kind() != reflect.Bool {
		t.Fatalf("models.User.IsAdmin = %s, want bool", value.Kind())
	}
	return value.Bool()
}

func p47StorageSetUserAdmin(t *testing.T, user *models.User, admin bool) {
	t.Helper()
	value := reflect.ValueOf(user).Elem().FieldByName("IsAdmin")
	if !value.IsValid() {
		t.Fatalf("models.User missing IsAdmin; P4.7 default seed contract requires production admin flag")
	}
	if !value.CanSet() || value.Kind() != reflect.Bool {
		t.Fatalf("models.User.IsAdmin cannot be set as bool in storage contract test")
	}
	value.SetBool(admin)
}

func newP47StorageContractDB(t *testing.T) *gorm.DB {
	t.Helper()
	baseDSN := strings.TrimSpace(os.Getenv("BROWSERWING_P46_POSTGRES_DSN"))
	if baseDSN == "" {
		t.Skip("P4.7 storage seed contract requires BROWSERWING_P46_POSTGRES_DSN targeting PostgreSQL database PlayBot")
	}

	schema := fmt.Sprintf("p47_storage_contract_%d", atomic.AddUint64(&p47StorageContractSequence, 1))
	adminDB, err := gorm.Open(postgres.Open(baseDSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open PostgreSQL admin connection: %v", err)
	}
	adminSQL, err := adminDB.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL admin sql handle: %v", err)
	}
	if err := adminDB.Exec("CREATE SCHEMA " + p47QuotePostgresIdentifier(schema)).Error; err != nil {
		t.Fatalf("create PostgreSQL test schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		if err := adminDB.Exec("DROP SCHEMA IF EXISTS " + p47QuotePostgresIdentifier(schema) + " CASCADE").Error; err != nil {
			t.Fatalf("drop PostgreSQL test schema %s: %v", schema, err)
		}
		_ = adminSQL.Close()
	})

	db, err := gorm.Open(postgres.Open(p47PostgresDSNWithSearchPath(t, baseDSN, schema)), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open PostgreSQL test schema connection: %v", err)
	}
	testSQL, err := db.DB()
	if err != nil {
		t.Fatalf("get PostgreSQL test sql handle: %v", err)
	}
	t.Cleanup(func() { _ = testSQL.Close() })
	return db
}

func p47PostgresDSNWithSearchPath(t *testing.T, rawDSN, schema string) string {
	t.Helper()
	option := "-c search_path=" + schema
	if strings.Contains(rawDSN, "://") {
		parsed, err := url.Parse(rawDSN)
		if err != nil {
			t.Fatalf("parse PostgreSQL DSN: %v", err)
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

func p47QuotePostgresIdentifier(value string) string {
	return "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
}
