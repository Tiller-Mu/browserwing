package storage

import (
	"log"
	"path/filepath"

	"github.com/browserwing/browserwing/models"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// InitSQLite 初始化 SQLite 数据库
func InitSQLite(dataDir string) error {
	dbPath := filepath.Join(dataDir, "testing_platform.db")
	
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})
	if err != nil {
		return err
	}

	// 注册自动迁移
	if err := models.AutoMigrate(db); err != nil {
		return err
	}

	DB = db
	log.Printf("SQLite initialized and migrated at: %s", dbPath)
	return nil
}
