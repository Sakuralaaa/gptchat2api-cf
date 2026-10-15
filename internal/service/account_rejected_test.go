package service

import (
	"testing"
	"time"
)

// TestImageAccountRejectedCooldown covers the short local cooldown applied
// after chatgpt.com answers an image conversation with the disguised
// "Model not found" 404. The rejection is a request quality signal, so it must
// only affect scheduling: no quota, status, or proxy health changes.
func TestImageAccountRejectedCooldown(t *testing.T) {
	accounts := newTestAccountServiceWithConfig(t, testAccountConfig{})

	if accounts.imageAccountRejectedCooledDown("at-1") {
		t.Fatal("token should not start cooled down")
	}
	accounts.MarkImageAccountRejected("at-1")
	if !accounts.imageAccountRejectedCooledDown("at-1") {
		t.Fatal("token should be cooled down after MarkImageAccountRejected")
	}
	if accounts.imageAccountRejectedCooledDown("at-2") {
		t.Fatal("an unrelated token must not be cooled down")
	}

	// Blank tokens are ignored rather than cooling down the empty key.
	accounts.MarkImageAccountRejected("   ")
	if accounts.imageAccountRejectedCooledDown("") {
		t.Fatal("a blank token must not be cooled down")
	}

	// Expired entries are dropped.
	accounts.mu.Lock()
	accounts.imageRejectedCooldown["at-3"] = time.Now().Add(-time.Second)
	accounts.mu.Unlock()
	if accounts.imageAccountRejectedCooledDown("at-3") {
		t.Fatal("an expired cooldown must not count")
	}

	// The cooldown window is the documented 10 minutes.
	if imageRejectedCooldownWindow != 10*time.Minute {
		t.Fatalf("imageRejectedCooldownWindow = %s, want 10m", imageRejectedCooldownWindow)
	}
}
