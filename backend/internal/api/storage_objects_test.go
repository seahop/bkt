package api

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bkt/internal/models"

	"github.com/google/uuid"
)

func TestLockObjectKeysSerializesSameKey(t *testing.T) {
	var inside int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := lockObjectKeys("b", "k")
			defer unlock()
			if n := atomic.AddInt32(&inside, 1); n != 1 {
				t.Errorf("%d holders of the same key lock", n)
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&inside, -1)
		}()
	}
	wg.Wait()
}

func TestLockObjectKeysMultiKeyNoDeadlock(t *testing.T) {
	// Opposite acquisition orders (a move a->b racing a move b->a) must not
	// deadlock: keys are locked in sorted order.
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(2)
			go func() { defer wg.Done(); u := lockObjectKeys("b", "x", "y"); u() }()
			go func() { defer wg.Done(); u := lockObjectKeys("b", "y", "x", "y"); u() }()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("multi-key locking deadlocked")
	}
	// Different buckets never contend; unlock is idempotent and the registry
	// is cleaned up.
	u1 := lockObjectKeys("b1", "k")
	u2 := lockObjectKeys("b2", "k")
	u1()
	u1()
	u2()
	objectKeyLocks.Lock()
	n := len(objectKeyLocks.m)
	objectKeyLocks.Unlock()
	if n != 0 {
		t.Errorf("%d lock entries leaked", n)
	}
}

func TestBucketViewOmitsSensitiveFields(t *testing.T) {
	cfg := uuid.New()
	b := &models.Bucket{
		ID:            uuid.New(),
		Name:          "photos",
		OwnerID:       uuid.New(),
		S3ConfigID:    &cfg,
		WebhookURL:    "https://hooks.slack.com/services/T000/B000/SECRET",
		WebhookEvents: "created",
		ReplicateTo:   "backup",
		Owner: models.User{
			ID:       uuid.New(),
			Username: "alice",
			Email:    "alice@example.com",
			IsAdmin:  true,
			SSOID:    "sso-123",
			SSOEmail: "alice@corp.example",
		},
	}
	raw, _ := json.Marshal(newBucketView(b, false, false))
	out := string(raw)
	for _, leak := range []string{"SECRET", "hooks.slack.com", "backup", cfg.String(), "alice@example.com", "sso-123", "alice@corp.example", "is_admin", "s3_config"} {
		if strings.Contains(out, leak) {
			t.Errorf("non-admin bucket view leaks %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, `"username":"alice"`) || !strings.Contains(out, `"name":"photos"`) {
		t.Errorf("view is missing expected fields: %s", out)
	}

	raw, _ = json.Marshal(newBucketView(b, true, true))
	out = string(raw)
	if !strings.Contains(out, "hooks.slack.com") || !strings.Contains(out, `"replicate_to":"backup"`) {
		t.Errorf("callers allowed to change settings should see them: %s", out)
	}
	if strings.Contains(out, cfg.String()) || strings.Contains(out, "alice@example.com") {
		t.Errorf("config id / owner email must never be in the non-admin view: %s", out)
	}
}

func TestValidateKeyForBucket(t *testing.T) {
	local := &models.Bucket{StorageBackend: "local"}
	s3b := &models.Bucket{StorageBackend: "s3"}
	if err := validateKeyForBucket(local, "folder/"); err == nil {
		t.Error("local backend must reject folder-marker keys")
	}
	if err := validateKeyForBucket(s3b, "folder/"); err != nil {
		t.Errorf("s3 backend should accept folder-marker keys: %v", err)
	}
	for _, k := range []string{"a//b", "./a", ".bkt-versions/a/x"} {
		if err := validateKeyForBucket(s3b, k); err == nil {
			t.Errorf("validateKeyForBucket(%q) accepted", k)
		}
	}
}
