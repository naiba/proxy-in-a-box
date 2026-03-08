package proxyinabox

import (
	"testing"
	"time"
)

func TestBlockedIPModel(t *testing.T) {
	db := SetupTestDB()

	locked := BlockedIP{
		IP:                  "1.2.3.4",
		ConsecutiveFailures: 3,
		LockedUntil:         time.Now().Add(15 * 24 * time.Hour),
	}
	if err := db.Create(&locked).Error; err != nil {
		t.Fatalf("failed to create blocked IP: %v", err)
	}

	var got BlockedIP
	if err := db.First(&got, "ip = ?", "1.2.3.4").Error; err != nil {
		t.Fatalf("failed to find blocked IP: %v", err)
	}
	if got.ConsecutiveFailures != 3 {
		t.Errorf("ConsecutiveFailures = %d, want 3", got.ConsecutiveFailures)
	}
	if got.LockedUntil.IsZero() {
		t.Error("LockedUntil should not be zero")
	}

	db.Model(&got).Update("consecutive_failures", 5)
	var updated BlockedIP
	db.First(&updated, "ip = ?", "1.2.3.4")
	if updated.ConsecutiveFailures != 5 {
		t.Errorf("after update, ConsecutiveFailures = %d, want 5", updated.ConsecutiveFailures)
	}

	db.Where("ip = ?", "1.2.3.4").Delete(&BlockedIP{})
	var count int64
	db.Model(&BlockedIP{}).Count(&count)
	if count != 0 {
		t.Errorf("after delete, count = %d, want 0", count)
	}
}

func TestBlockedIPPrimaryKey(t *testing.T) {
	db := SetupTestDB()

	b1 := BlockedIP{IP: "10.0.0.1", ConsecutiveFailures: 1}
	db.Create(&b1)

	b2 := BlockedIP{IP: "10.0.0.1", ConsecutiveFailures: 2}
	if err := db.Save(&b2).Error; err != nil {
		t.Fatalf("Save with same IP should upsert, got error: %v", err)
	}

	var got BlockedIP
	db.First(&got, "ip = ?", "10.0.0.1")
	if got.ConsecutiveFailures != 2 {
		t.Errorf("after upsert, ConsecutiveFailures = %d, want 2", got.ConsecutiveFailures)
	}
}
