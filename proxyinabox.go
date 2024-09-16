package proxyinabox

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// DB instance
var DB *gorm.DB

// CI cache instance
var CI Cache

// Conf config struct
type Conf struct {
	Debug bool
	Redis struct {
		Host string
		Port string
		Pass string
		Db   int
	}
	Sys struct {
		Name              string
		ProxyVerifyWorker int   `mapstructure:"proxy_verify_worker"`
		DomainsPerIP      int   `mapstructure:"domains_per_ip"`
		RequestLimitPerIP int64 `mapstructure:"request_limit_per_ip"`
		VerifyDuration    int   `mapstructure:"verify_duration"`
	}
}

// Config system config
var Config Conf

// Init init system
func Init() {
	validateConf()
	initDB()
}

func initDB() {
	var err error
	DB, err = gorm.Open(sqlite.Open("proxyinabox.db"))
	if err != nil {
		panic(err)
	}
	if Config.Debug {
		DB = DB.Debug()
	}
	DB.AutoMigrate(&Proxy{})
}

func validateConf() {
	if Config.Sys.VerifyDuration <= 5 {
		panic("proxy verify duration (must >5 minute)")
	}
}
