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
	// Folder-marker keys are valid on both backends (the local backend stores
	// them as "<folder>/.bkt-folder").
	for _, b := range []*models.Bucket{local, s3b} {
		if err := validateKeyForBucket(b, "folder/"); err != nil {
			t.Errorf("%s backend should accept folder-marker keys: %v", b.StorageBackend, err)
		}
	}
	// Non-canonical spellings alias on the filesystem only: rejected on local
	// buckets, distinct valid keys on S3 buckets.
	for _, k := range []string{"a//b", "./a", "a/./b", "a//", "folder/.bkt-folder"} {
		if err := validateKeyForBucket(local, k); err == nil {
			t.Errorf("validateKeyForBucket(local, %q) accepted", k)
		}
	}
	for _, k := range []string{"a//b", "./a", "a/./b"} {
		if err := validateKeyForBucket(s3b, k); err != nil {
			t.Errorf("validateKeyForBucket(s3, %q) = %v, want nil", k, err)
		}
	}
	for _, b := range []*models.Bucket{local, s3b} {
		for _, k := range []string{".bkt-versions/a/x", "../x", "/abs", ""} {
			if err := validateKeyForBucket(b, k); err == nil {
				t.Errorf("validateKeyForBucket(%s, %q) accepted", b.StorageBackend, k)
			}
		}
	}
}

func objectLockEntries() int {
	objectKeyLocks.Lock()
	defer objectKeyLocks.Unlock()
	return len(objectKeyLocks.m)
}

func TestTryLockObjectKeys(t *testing.T) {
	// Free keys: acquired immediately, even with a zero timeout.
	unlock, ok := tryLockObjectKeys("tb", 0, "a", "b")
	if !ok {
		t.Fatal("tryLock of free keys failed")
	}

	// Held keys: a second tryLock gives up after the timeout and holds
	// nothing — in particular not the free key "c" it may have taken first.
	start := time.Now()
	u2, ok := tryLockObjectKeys("tb", 50*time.Millisecond, "c", "b")
	if ok {
		u2()
		t.Fatal("tryLock acquired a held key")
	}
	if d := time.Since(start); d < 40*time.Millisecond || d > 5*time.Second {
		t.Errorf("tryLock gave up after %v, want ~50ms", d)
	}
	u2() // no-op on failure
	if u3, ok := tryLockObjectKeys("tb", 0, "c"); !ok {
		t.Error("failed tryLock leaked its partial acquisition of key c")
	} else {
		u3()
	}

	// A blocking locker waiting on a held key gets it once released, and a
	// tryLock waiting within its timeout succeeds too.
	got := make(chan struct{})
	go func() {
		u, ok := tryLockObjectKeys("tb", 5*time.Second, "a")
		if ok {
			u()
		}
		close(got)
	}()
	time.Sleep(20 * time.Millisecond)
	unlock()
	unlock() // idempotent
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("waiting tryLock never acquired the released key")
	}

	if n := objectLockEntries(); n != 0 {
		t.Errorf("%d lock entries leaked", n)
	}
}

func TestTryLockObjectKeysMixedWithBlockingLockers(t *testing.T) {
	// Blocking and try-lockers on overlapping key sets in opposite orders
	// never deadlock and never hold the same key twice.
	var inside int32 // both lockers take both keys: at most one holder
	var wg sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < 40; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			u := lockObjectKeys("mx", "x", "y")
			if atomic.AddInt32(&inside, 1) != 1 {
				t.Error("key held twice")
			}
			atomic.AddInt32(&inside, -1)
			u()
		}()
		go func() {
			defer wg.Done()
			u, ok := tryLockObjectKeys("mx", time.Millisecond, "y", "x")
			if !ok {
				return
			}
			if atomic.AddInt32(&inside, 1) != 1 {
				t.Error("key held twice")
			}
			atomic.AddInt32(&inside, -1)
			u()
		}()
	}
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadlock")
	}
	if n := objectLockEntries(); n != 0 {
		t.Errorf("%d lock entries leaked", n)
	}
}
