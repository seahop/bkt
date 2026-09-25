package api

import (
	"testing"
	"time"

	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/google/uuid"
)

func evalFor(t *testing.T, bucket string, docs ...string) *services.AccessEvaluator {
	t.Helper()
	u := models.User{Username: "alice"}
	for _, d := range docs {
		u.Policies = append(u.Policies, models.Policy{Document: d})
	}
	return services.NewAccessEvaluatorFromData(&u, bucket, true, nil)
}

// The configure-time check uses the literal key "*", which a narrower Deny
// never matches. The per-key sweep check must honor it.
func TestReplicationPerKeyDenyIsHonored(t *testing.T) {
	allowAll := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["*"]}]}`
	denySrcSecret := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::src/secret/*"]}]}`
	denySrcKey := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:*"],"Resource":["arn:aws:s3:::src/x.txt"]}]}`
	denyDstPut := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:PutObject","s3:DeleteObject"],"Resource":["arn:aws:s3:::dst/keep/*"]}]}`

	srcEval := evalFor(t, "src", allowAll, denySrcSecret, denySrcKey)
	dstEval := evalFor(t, "dst", allowAll, denySrcSecret, denySrcKey, denyDstPut)

	// Sanity: the configure-time "*" probe does not see the narrow denies.
	if !srcEval.Allowed(services.ActionGetObject, replicationAllObjectsKey) {
		t.Fatal("the \"*\" probe is expected to pass (that is the bug the sweep check closes)")
	}
	cases := []struct {
		key  string
		want bool
	}{
		{"a.txt", true},
		{"secret/k", false},
		{"x.txt", false},
		{"keep/k", false},
		{"deep/secret/k", true},
	}
	for _, c := range cases {
		if got := replicationCopyAllowed(srcEval, dstEval, c.key); got != c.want {
			t.Errorf("copy %q: got %v want %v", c.key, got, c.want)
		}
	}
	if dstEval.Allowed(services.ActionDeleteObject, "keep/k") {
		t.Error("mirror-delete of a Deny'd target key must be refused")
	}
	if !dstEval.Allowed(services.ActionDeleteObject, "other") {
		t.Error("mirror-delete of an allowed target key must be allowed")
	}

	// A principal who lost all access replicates nothing.
	none := evalFor(t, "src")
	if replicationCopyAllowed(none, dstEval, "a.txt") {
		t.Error("no source read access must block the copy")
	}
}

func TestReplicationTargetMismatch(t *testing.T) {
	now := time.Now()
	id := uuid.New()
	other := uuid.New()
	configured := now.Add(-time.Hour)

	src := &models.Bucket{ReplicateToID: &id, ReplicationConfiguredAt: &configured}
	if r := replicationTargetMismatch(src, &models.Bucket{ID: id, CreatedAt: now.Add(-2 * time.Hour)}); r != "" {
		t.Errorf("matching ID: unexpected mismatch %q", r)
	}
	if r := replicationTargetMismatch(src, &models.Bucket{ID: other, CreatedAt: now.Add(-2 * time.Hour)}); r == "" {
		t.Error("re-created target (different ID) must be refused")
	}

	// Legacy config: no recorded ID; decide by creation time.
	legacy := &models.Bucket{UpdatedAt: configured}
	if r := replicationTargetMismatch(legacy, &models.Bucket{ID: other, CreatedAt: now}); r == "" {
		t.Error("target created after the config was saved must be refused")
	}
	if r := replicationTargetMismatch(legacy, &models.Bucket{ID: other, CreatedAt: now.Add(-2 * time.Hour)}); r != "" {
		t.Errorf("older target: unexpected mismatch %q", r)
	}
	legacyAt := &models.Bucket{UpdatedAt: now, ReplicationConfiguredAt: &configured}
	if r := replicationTargetMismatch(legacyAt, &models.Bucket{ID: other, CreatedAt: now.Add(-30 * time.Minute)}); r == "" {
		t.Error("ReplicationConfiguredAt must take precedence over UpdatedAt")
	}
}

func TestTryLockKeysAcrossBuckets(t *testing.T) {
	src, dst := "trylock-src-"+uuid.NewString(), "trylock-dst-"+uuid.NewString()

	// Busy target key: the cross-bucket try fails and holds nothing.
	holdDst := lockObjectKeys(dst, "k")
	start := time.Now()
	if _, ok := tryLockKeysAcrossBuckets(src, "k", dst, "k", 50*time.Millisecond); ok {
		t.Fatal("must fail while the target key is held")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("try-lock must honor its timeout")
	}
	if unlock, ok := tryLockObjectKeys(src, 50*time.Millisecond, "k"); !ok {
		t.Fatal("a failed cross-bucket try must release the source key it took")
	} else {
		unlock()
	}
	holdDst()

	// Free: acquires both; while held, each key is busy; unlock frees both.
	unlock, ok := tryLockKeysAcrossBuckets(src, "k", dst, "k", 50*time.Millisecond)
	if !ok {
		t.Fatal("must succeed when both keys are free")
	}
	if _, ok := tryLockObjectKeys(src, 10*time.Millisecond, "k"); ok {
		t.Fatal("source key must be held")
	}
	if _, ok := tryLockObjectKeys(dst, 10*time.Millisecond, "k"); ok {
		t.Fatal("target key must be held")
	}
	unlock()
	u2, ok := tryLockKeysAcrossBuckets(dst, "k", src, "k", 50*time.Millisecond)
	if !ok {
		t.Fatal("unlock must release both keys")
	}
	u2()
}

// Sweeps must skip a busy key quickly instead of blocking on it.
func TestSweepsSkipBusyKeys(t *testing.T) {
	old := sweepLockTimeout
	sweepLockTimeout = 30 * time.Millisecond
	defer func() { sweepLockTimeout = old }()

	b := &models.Bucket{ID: uuid.New(), Name: "busy-" + uuid.NewString()}
	d := &models.Bucket{ID: uuid.New(), Name: "busy-dst-" + uuid.NewString()}
	hold := lockObjectKeys(b.Name, "k")
	defer hold()
	holdDst := lockObjectKeys(d.Name, "k")
	defer holdDst()

	if done, busy := expireCurrentObject(nil, b, "k", time.Now()); done || !busy {
		t.Errorf("expireCurrentObject: done=%v busy=%v, want busy", done, busy)
	}
	if done, busy := expireNoncurrentVersion(nil, b, &models.ObjectVersion{Key: "k"}); done || !busy {
		t.Errorf("expireNoncurrentVersion: done=%v busy=%v, want busy", done, busy)
	}
	if done, busy := replicateObject(nil, nil, b, d, "k"); done || !busy {
		t.Errorf("replicateObject: done=%v busy=%v, want busy", done, busy)
	}
	if done, busy := mirrorDelete(nil, b, d, "k"); done || !busy {
		t.Errorf("mirrorDelete: done=%v busy=%v, want busy", done, busy)
	}
}
