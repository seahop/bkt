package security

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestBuildDecryptOnlySourcesOrderAndDedup(t *testing.T) {
	srcs := buildDecryptOnlySources("primary", true, "jwt-secret", " prev1 , primary, prev2,,prev1 ", "legacy-jwt")
	var names, secrets []string
	for _, s := range srcs {
		names = append(names, s.name)
		secrets = append(secrets, string(s.secret))
	}
	want := []string{"prev1", "prev2", "jwt-secret", "legacy-jwt"}
	if len(secrets) != len(want) {
		t.Fatalf("got %v (%v)", secrets, names)
	}
	for i := range want {
		if secrets[i] != want[i] {
			t.Fatalf("order: got %v want %v", secrets, want)
		}
	}
	// Without a dedicated ENCRYPTION_KEY the JWT secret IS the primary.
	srcs = buildDecryptOnlySources("jwt-secret", false, "jwt-secret", "", "jwt-secret")
	if len(srcs) != 0 {
		t.Fatalf("primary must not be duplicated as decrypt-only: %d", len(srcs))
	}
}

func TestDecryptSecretKeyStatusReportsStale(t *testing.T) {
	useTestKeyMaterial(t)
	old := decryptOnlySources
	t.Cleanup(func() { decryptOnlySources = old })

	prev := newNamedKeySource("ENCRYPTION_KEY_PREVIOUS[0]", []byte("short"))
	legacyJWT := newNamedKeySource("ENCRYPTION_LEGACY_JWT_SECRET", []byte("dev_jwt_secret_change_in_production"))
	decryptOnlySources = []*keySource{prev, legacyJWT}

	current, _ := EncryptSecretKey("current")
	if p, stale, err := DecryptSecretKeyStatus(current); err != nil || stale || p != "current" {
		t.Fatalf("primary value: %q stale=%v err=%v", p, stale, err)
	}
	underPrev, _ := encryptV2With("under-previous", prev.v2Key)
	if p, stale, err := DecryptSecretKeyStatus(underPrev); err != nil || !stale || p != "under-previous" {
		t.Fatalf("previous-key value: %q stale=%v err=%v", p, stale, err)
	}
	underLegacy, _ := encryptV2With("under-legacy-jwt", legacyJWT.v2Key)
	if p, stale, err := DecryptSecretKeyStatus(underLegacy); err != nil || !stale || p != "under-legacy-jwt" {
		t.Fatalf("legacy-jwt value: %q stale=%v err=%v", p, stale, err)
	}
	v1 := base64.StdEncoding.EncodeToString(encryptLegacyForTest(t, "v1-format"))
	if p, stale, err := DecryptSecretKeyStatus(v1); err != nil || !stale || p != "v1-format" {
		t.Fatalf("v1 value: %q stale=%v err=%v", p, stale, err)
	}
	other := newKeySource([]byte("unknown-key"))
	unknown, _ := encryptV2With("x", other.v2Key)
	if _, _, err := DecryptSecretKeyStatus(unknown); err == nil {
		t.Fatal("value under an unconfigured key must fail")
	}
	if maybeLegacyFormat(current) {
		t.Fatal("a v2 value must not be classified as a v1 candidate")
	}
}

// Integration test against a real PostgreSQL (set BKT_TEST_POSTGRES_DSN).
func TestReencryptStoredSecretsPostgres(t *testing.T) {
	dsn := os.Getenv("BKT_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("BKT_TEST_POSTGRES_DSN not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS access_keys`, `DROP TABLE IF EXISTS s3_configurations`, `DROP TABLE IF EXISTS buckets`,
		`CREATE TABLE buckets (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), webhook_secret text DEFAULT '')`,
		`CREATE TABLE access_keys (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), secret_key_encrypted text NOT NULL)`,
		`CREATE TABLE s3_configurations (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), access_key_id text NOT NULL, secret_access_key text NOT NULL)`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	useTestKeyMaterial(t)
	old := decryptOnlySources
	t.Cleanup(func() { decryptOnlySources = old })
	prev := newNamedKeySource("ENCRYPTION_KEY_PREVIOUS[0]", []byte("old-key"))
	decryptOnlySources = []*keySource{prev}

	cur, _ := EncryptSecretKey("cur-secret")
	stale1, _ := encryptV2With("stale-secret", prev.v2Key)
	staleAK, _ := encryptV2With("stale-ak", prev.v2Key)
	staleSK, _ := encryptV2With("stale-sk", prev.v2Key)
	junk, _ := encryptV2With("junk", newKeySource([]byte("lost-key")).v2Key)
	db.Exec(`INSERT INTO access_keys (secret_key_encrypted) VALUES (?), (?), (?)`, cur, stale1, junk)
	db.Exec(`INSERT INTO s3_configurations (access_key_id, secret_access_key) VALUES (?, ?)`, staleAK, staleSK)
	staleWH, _ := encryptV2With("stale-webhook", prev.v2Key)
	db.Exec(`INSERT INTO buckets (webhook_secret) VALUES (?), (?), ('')`, "enc:v2:"+staleWH, "legacy-plaintext-webhook")

	st, err := ReencryptStoredSecrets(db)
	if err != nil {
		t.Fatal(err)
	}
	if st.Reencrypted != 4 || st.Failed != 1 {
		t.Fatalf("stats %+v", st)
	}
	// Everything readable is now under the primary key alone.
	decryptOnlySources = nil
	var vals []string
	db.Raw(`SELECT secret_key_encrypted FROM access_keys UNION ALL SELECT access_key_id FROM s3_configurations UNION ALL SELECT secret_access_key FROM s3_configurations`).Scan(&vals)
	readable := 0
	for _, v := range vals {
		if _, stale, err := DecryptSecretKeyStatus(v); err == nil && !stale {
			readable++
		}
	}
	if readable != 4 {
		t.Fatalf("readable under primary: %d of %d", readable, len(vals))
	}
	// Webhook secret keeps its prefix; legacy plaintext is untouched.
	var wh []string
	db.Raw(`SELECT webhook_secret FROM buckets ORDER BY webhook_secret`).Scan(&wh)
	if len(wh) != 3 || !strings.HasPrefix(wh[1], "enc:v2:") || wh[2] != "legacy-plaintext-webhook" {
		t.Fatalf("webhook secrets: %v", wh)
	}
	if p, stale, err := DecryptSecretKeyStatus(wh[1][len("enc:v2:"):]); err != nil || stale || p != "stale-webhook" {
		t.Fatalf("webhook secret not moved to primary: %q %v %v", p, stale, err)
	}
	// The undecryptable value was left untouched.
	var n int64
	db.Raw(`SELECT count(*) FROM access_keys WHERE secret_key_encrypted = ?`, junk).Scan(&n)
	if n != 1 {
		t.Fatal("undecryptable value modified")
	}
	// Idempotent.
	decryptOnlySources = []*keySource{prev}
	st, err = ReencryptStoredSecrets(db)
	if err != nil || st.Reencrypted != 0 {
		t.Fatalf("second pass: %+v %v", st, err)
	}
}
