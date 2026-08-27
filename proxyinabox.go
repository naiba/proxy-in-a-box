package proxyinabox

import (
	"fmt"
	"path/filepath"
	"time"

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
		ProxyVerifyWorker int `mapstructure:"proxy_verify_worker"`
	}
	Obscura struct {
		// obscura 二进制路径，留空则使用 PATH 中的默认命令
		Bin string `mapstructure:"bin"`
	} `mapstructure:"obscura"`
	// Upstream controls how requests are attempted through proxies selected from
	// the pool. Zero values use the safe defaults defined by the MITM package.
	Upstream struct {
		MaxAttempts           int           `mapstructure:"max_attempts"`
		ConnectTimeout        time.Duration `mapstructure:"connect_timeout"`
		HandshakeTimeout      time.Duration `mapstructure:"handshake_timeout"`
		ResponseHeaderTimeout time.Duration `mapstructure:"response_header_timeout"`
		RequestTimeout        time.Duration `mapstructure:"request_timeout"`
		TargetFailureTTL      time.Duration `mapstructure:"target_failure_ttl"`
	} `mapstructure:"upstream"`
	// EnableMITM 是否启用 HTTPS 中间人解密，默认 false（关闭时走 TCP 隧道透传，客户端无需关闭 TLS 验证）
	EnableMITM bool `mapstructure:"enable_mitm"`
}

// Config system config
var Config Conf

var DataDir string

// Init init system
func Init(configFilePath string) {
	DataDir = filepath.Dir(configFilePath)
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
	if err := migrateDB(DB); err != nil {
		panic(fmt.Errorf("migrate database: %w", err))
	}
}

func migrateDB(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		migrator := tx.Migrator()
		if migrator.HasTable(&Proxy{}) {
			// Add this independently of the rest of AutoMigrate. Startup queries
			// depend on it, so a failed migration must never be allowed to look
			// successful and fail later in MemCache.load.
			if !migrator.HasColumn(&Proxy{}, "Available") {
				if err := migrator.AddColumn(&Proxy{}, "Available"); err != nil {
					return fmt.Errorf("add proxies.available: %w", err)
				}
			}

			// Older releases allowed duplicate endpoints. Remove them before
			// AutoMigrate creates idx_proxy_endpoint, normalizing an empty protocol
			// as HTTP when deciding which row is newest.
			if migrator.HasColumn(&Proxy{}, "IP") &&
				migrator.HasColumn(&Proxy{}, "Port") &&
				migrator.HasColumn(&Proxy{}, "Protocol") &&
				migrator.HasColumn(&Proxy{}, "DeletedAt") {
				if err := tx.Exec(`DELETE FROM proxies WHERE id IN (
					SELECT id FROM (
						SELECT id, ROW_NUMBER() OVER (
							PARTITION BY ip, port, COALESCE(NULLIF(protocol, ''), 'http')
							ORDER BY CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END, id DESC
						) AS duplicate_number
						FROM proxies
					) AS ranked_proxies
					WHERE duplicate_number > 1
				)`).Error; err != nil {
					return fmt.Errorf("deduplicate legacy proxies: %w", err)
				}
				if err := tx.Unscoped().Model(&Proxy{}).
					Where("protocol = '' OR protocol IS NULL").
					Update("protocol", "http").Error; err != nil {
					return fmt.Errorf("normalize legacy proxy protocols: %w", err)
				}
			}
		}

		if err := tx.AutoMigrate(&Proxy{}, &BlockedIP{}); err != nil {
			return err
		}
		if !tx.Migrator().HasColumn(&Proxy{}, "Available") {
			return fmt.Errorf("proxies.available is missing after migration")
		}
		return nil
	})
}
