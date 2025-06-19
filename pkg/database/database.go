package database

import (
	"fmt"
	"gobi/config"
	"gobi/internal/models"

	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var DB *gorm.DB

func InitDB(cfg *config.Config) error {
	var err error
	switch cfg.Database.Type {
	case "sqlite":
		DB, err = gorm.Open(sqlite.Open(cfg.Database.DSN), &gorm.Config{})
	case "mysql":
		DB, err = gorm.Open(mysql.Open(cfg.Database.DSN), &gorm.Config{})
	case "postgres":
		DB, err = gorm.Open(postgres.Open(cfg.Database.DSN), &gorm.Config{})
	default:
		return fmt.Errorf("unsupported database type: %s", cfg.Database.Type)
	}
	if err != nil {
		return err
	}

	// Auto migrate the schema
	err = DB.AutoMigrate(
		&models.User{},
		&models.DataSource{},
		&models.Query{},
		&models.Chart{},
		&models.ExcelTemplate{},
		&models.Report{},
		&models.ReportSchedule{},
	)
	if err != nil {
		return err
	}

	return nil
}
