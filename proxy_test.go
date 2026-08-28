package proxyinabox

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestProxyURI(t *testing.T) {
	tests := []struct {
		name     string
		proxy    Proxy
		expected string
	}{
		{
			name:     "http protocol",
			proxy:    Proxy{IP: "1.2.3.4", Port: "8080", Protocol: "http"},
			expected: "http://1.2.3.4:8080",
		},
		{
			name:     "socks5 protocol",
			proxy:    Proxy{IP: "5.6.7.8", Port: "1080", Protocol: "socks5"},
			expected: "socks5://5.6.7.8:1080",
		},
		{
			name:     "empty protocol defaults to http",
			proxy:    Proxy{IP: "10.0.0.1", Port: "3128", Protocol: ""},
			expected: "http://10.0.0.1:3128",
		},
		{
			name:     "https protocol",
			proxy:    Proxy{IP: "192.168.1.1", Port: "443", Protocol: "https"},
			expected: "https://192.168.1.1:443",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.proxy.URI()
			if got != tt.expected {
				t.Errorf("URI() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestMigrateDBAddsAvailabilityAndDeduplicatesLegacyProxies(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Exec(`CREATE TABLE proxies (
		id integer PRIMARY KEY AUTOINCREMENT,
		created_at datetime,
		updated_at datetime,
		deleted_at datetime,
		ip text,
		port text,
		country text,
		provence text,
		source text,
		protocol text,
		delay integer,
		last_verify datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO proxies (ip, port, protocol, last_verify) VALUES (?, ?, ?, ?)",
		"1.2.3.4", "8080", "http", time.Now(),
	).Error; err != nil {
		t.Fatalf("insert legacy proxy: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO proxies (ip, port, protocol, last_verify) VALUES (?, ?, ?, ?)",
		"1.2.3.4", "8080", "http", time.Now(),
	).Error; err != nil {
		t.Fatalf("insert duplicate legacy proxy: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO proxies (ip, port, protocol, last_verify) VALUES (?, ?, ?, ?)",
		"1.2.3.4", "8080", "", time.Now(),
	).Error; err != nil {
		t.Fatalf("insert blank-protocol legacy proxy: %v", err)
	}
	if err := db.Exec(
		"INSERT INTO proxies (deleted_at, ip, port, protocol, last_verify) VALUES (?, ?, ?, ?, ?)",
		time.Now(), "1.2.3.4", "8080", "http", time.Now(),
	).Error; err != nil {
		t.Fatalf("insert soft-deleted legacy proxy: %v", err)
	}

	if err := migrateDB(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	var migrated []Proxy
	if err := db.Find(&migrated).Error; err != nil {
		t.Fatalf("load migrated proxies: %v", err)
	}
	if len(migrated) != 1 {
		t.Fatalf("migrated proxy count = %d, want 1", len(migrated))
	}
	if !migrated[0].Available {
		t.Fatal("legacy proxy should default to available after migration")
	}
	if migrated[0].LastDeepVerify.IsZero() || !migrated[0].LastDeepVerify.Equal(migrated[0].LastVerify) {
		t.Fatalf("legacy deep verification = %v, want last verification %v", migrated[0].LastDeepVerify, migrated[0].LastVerify)
	}
	if migrated[0].Protocol != "http" {
		t.Fatalf("migrated protocol = %q, want http", migrated[0].Protocol)
	}
	var unscopedCount int64
	if err := db.Unscoped().Model(&Proxy{}).Count(&unscopedCount).Error; err != nil {
		t.Fatalf("count all migrated proxies: %v", err)
	}
	if unscopedCount != 1 {
		t.Fatalf("unscoped migrated proxy count = %d, want 1", unscopedCount)
	}
	if !db.Migrator().HasIndex(&Proxy{}, "idx_proxy_endpoint") {
		t.Fatal("endpoint unique index was not created")
	}
}

func TestMigrateDBDefersLegacyQuarantinedProxies(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.Exec(`CREATE TABLE proxies (
		id integer PRIMARY KEY AUTOINCREMENT,
		created_at datetime,
		updated_at datetime,
		deleted_at datetime,
		ip text,
		port text,
		country text,
		provence text,
		source text,
		protocol text,
		delay integer,
		available numeric NOT NULL DEFAULT true,
		last_verify datetime
	)`).Error; err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	lastVerify := time.Now().Add(-time.Hour).Round(0)
	if err := db.Exec(
		"INSERT INTO proxies (ip, port, protocol, available, last_verify) VALUES (?, ?, ?, ?, ?)",
		"2.3.4.5", "8080", "http", false, lastVerify,
	).Error; err != nil {
		t.Fatalf("insert quarantined proxy: %v", err)
	}

	before := time.Now()
	if err := migrateDB(db); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	var migrated Proxy
	if err := db.First(&migrated).Error; err != nil {
		t.Fatalf("load migrated proxy: %v", err)
	}
	if migrated.Available {
		t.Fatal("quarantined proxy became available during migration")
	}
	delay := migrated.NextVerifyAt.Sub(before)
	if delay < 29*time.Minute || delay > 31*time.Minute {
		t.Fatalf("migrated retry delay = %s, want about 30m", delay)
	}
	if !migrated.LastDeepVerify.Equal(lastVerify) {
		t.Fatalf("LastDeepVerify = %v, want %v", migrated.LastDeepVerify, lastVerify)
	}
}

func TestProxyString(t *testing.T) {
	p := Proxy{IP: "1.2.3.4", Port: "8080", Protocol: "http", Country: "US", Source: "test"}
	s := p.String()
	if s == "" {
		t.Error("String() returned empty")
	}
	for _, substr := range []string{"1.2.3.4", "8080", "http", "US", "test"} {
		if !contains(s, substr) {
			t.Errorf("String() missing %q in %q", substr, s)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
