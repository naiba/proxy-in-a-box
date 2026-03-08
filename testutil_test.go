package proxyinabox

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func SetupTestDB() *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		panic("failed to open test database: " + err.Error())
	}
	db.AutoMigrate(&Proxy{}, &BlockedIP{})
	return db
}
