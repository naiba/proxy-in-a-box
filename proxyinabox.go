package proxyinabox

import (
	"path/filepath"

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
	Pinchtab struct {
		// pinchtab 二进制路径，留空则禁用浏览器抓取
		Bin string
		// pinchtab 监听端口，默认 9867
		Port string
	}
	// EnableMITM 是否启用 HTTPS 中间人解密，默认 false（关闭时走 TCP 隧道透传，客户端无需关闭 TLS 验证）
	EnableMITM bool `mapstructure:"enable_mitm"`
}

// Config system config
var Config Conf

var DataDir string

// Init init system
func Init(configFilePath string) {
	DataDir = filepath.Dir(configFilePath)
	validateConf()
	initDB()
}

func initDB() {
	var err error
	DB, err = gorm.Open(sqlite.Open(filepath.Join(DataDir, "proxyinabox.db")))
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
