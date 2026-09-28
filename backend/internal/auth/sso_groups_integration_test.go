package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Postgres-gated: BKT_TEST_POSTGRES_HOST=pg BKT_TEST_POSTGRES_PASSWORD=t go test ./internal/auth -run Integration

type ssoGroupFixture struct {
	eng, ops, multi, unmapped, manual models.Group
}

func mkSSOGroup(t *testing.T, name string, links ...string) models.Group {
	t.Helper()
	g := models.Group{ID: uuid.New(), Name: name + "-" + uuid.NewString()[:6]}
	if err := database.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if err := database.DB.Create(&models.GroupSSOLink{GroupID: g.ID, SSOGroup: l}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func newSSOGroupFixture(t *testing.T) ssoGroupFixture {
	t.Helper()
	return ssoGroupFixture{
		eng:      mkSSOGroup(t, "eng", "Eng"),
		ops:      mkSSOGroup(t, "ops", "OPS"),
		multi:    mkSSOGroup(t, "multi", "eng", "platform"), // several IdP groups → one bkt group
		unmapped: mkSSOGroup(t, "fin", "finance"),
		manual:   mkSSOGroup(t, "manual"), // no links: manual group
	}
}

func mkIntegrationUser(t *testing.T, provider string) *models.User {
	t.Helper()
	n := "sg-" + uuid.NewString()[:8]
	u := &models.User{ID: uuid.New(), Username: n, Email: n + "@example.test", SSOProvider: provider}
	if provider != "" {
		u.SSOID = "sub-" + n
	}
	if err := database.DB.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func addMember(t *testing.T, u *models.User, g models.Group) {
	t.Helper()
	if err := database.DB.Exec(`INSERT INTO user_groups (user_id, group_id) VALUES (?, ?)`, u.ID, g.ID).Error; err != nil {
		t.Fatal(err)
	}
}

func memberships(t *testing.T, userID uuid.UUID) map[uuid.UUID]bool {
	t.Helper()
	var ids []uuid.UUID
	if err := database.DB.Table("user_groups").Where("user_id = ?", userID).Pluck("group_id", &ids).Error; err != nil {
		t.Fatal(err)
	}
	m := map[uuid.UUID]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func wantMembers(t *testing.T, step string, userID uuid.UUID, in, out []models.Group) {
	t.Helper()
	m := memberships(t, userID)
	for _, g := range in {
		if !m[g.ID] {
			t.Errorf("%s: user should be in %s", step, g.Name)
		}
	}
	for _, g := range out {
		if m[g.ID] {
			t.Errorf("%s: user should NOT be in %s", step, g.Name)
		}
	}
}

func TestIntegrationSSOGroupSync(t *testing.T) {
	authIntegrationDB(t)
	f := newSSOGroupFixture(t)
	u := mkIntegrationUser(t, "oidc")
	addMember(t, u, f.manual)

	// First sign-in with IdP groups [Eng, ops]: case-insensitive matches.
	added, removed, err := SyncSSOGroupMemberships(database.DB, u, []string{"Eng", "ops"}, true, "test")
	if err != nil {
		t.Fatal(err)
	}
	wantMembers(t, "first sign-in", u.ID, []models.Group{f.eng, f.ops, f.multi, f.manual}, []models.Group{f.unmapped})
	wantNames := []string{f.eng.Name, f.multi.Name, f.ops.Name}
	sort.Strings(wantNames)
	if strings.Join(added, ",") != strings.Join(wantNames, ",") || len(removed) != 0 {
		t.Errorf("added=%v removed=%v, want added=%v", added, removed, wantNames)
	}

	// Idempotent.
	if added, removed, _ := SyncSSOGroupMemberships(database.DB, u, []string{"eng", "OPS"}, true, "test"); len(added)+len(removed) != 0 {
		t.Errorf("re-sync changed memberships: +%v -%v", added, removed)
	}

	// A manual (admin-made) membership of this SSO user in a linked group is
	// replaced at the next sign-in.
	addMember(t, u, f.unmapped)

	// Later sign-in with only [ops]: removed from the Eng-mapped groups.
	_, removed, err = SyncSSOGroupMemberships(database.DB, u, []string{"ops"}, true, "test")
	if err != nil {
		t.Fatal(err)
	}
	wantMembers(t, "second sign-in", u.ID, []models.Group{f.ops, f.manual}, []models.Group{f.eng, f.multi, f.unmapped})
	if len(removed) != 3 {
		t.Errorf("removed = %v, want eng, multi, fin", removed)
	}

	// No group information at all: fail closed, manual group kept.
	if _, _, err := SyncSSOGroupMemberships(database.DB, u, nil, false, "the \"groups\" claim"); err != nil {
		t.Fatal(err)
	}
	wantMembers(t, "no groups claim", u.ID, []models.Group{f.manual}, []models.Group{f.eng, f.ops, f.multi, f.unmapped})

	// Local users are never touched, whatever the input.
	local := mkIntegrationUser(t, "")
	addMember(t, local, f.eng)
	if added, removed, err := SyncSSOGroupMemberships(database.DB, local, []string{"ops"}, true, "test"); err != nil || len(added)+len(removed) != 0 {
		t.Fatalf("local user: +%v -%v err=%v", added, removed, err)
	}
	if _, _, err := SyncSSOGroupMemberships(database.DB, local, nil, false, "test"); err != nil {
		t.Fatal(err)
	}
	wantMembers(t, "local user", local.ID, []models.Group{f.eng}, []models.Group{f.ops})
}

// ── OIDC callback end to end (fake IdP + Postgres) ───────────────────────────

func oidcCallback(t *testing.T, h *OIDCHandler, nonce string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=good-code&state=st", nil)
	c.Request.AddCookie(&http.Cookie{Name: "oidc_oauth_state", Value: "st"})
	c.Request.AddCookie(&http.Cookie{Name: "oidc_pkce_verifier", Value: "verifier-verifier-verifier-verifier-verifier"})
	c.Request.AddCookie(&http.Cookie{Name: "oidc_oidc_nonce", Value: nonce})
	h.Callback(c)
	return w
}

func ssoTestAuthConfig(cfg *config.Config) {
	cfg.Auth.JWTSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.Auth.AccessTokenExpiry, cfg.Auth.RefreshTokenExpiry = "15m", "24h"
}

func lastLoginAudit(t *testing.T, userID uuid.UUID) map[string]interface{} {
	t.Helper()
	var a models.AuditLog
	if err := database.DB.Where("user_id = ? AND action = ? AND status = ?", userID, "auth.login", "success").
		Order("created_at DESC").First(&a).Error; err != nil {
		t.Fatal(err)
	}
	m := map[string]interface{}{}
	_ = json.Unmarshal([]byte(a.Metadata), &m)
	return m
}

func TestIntegrationOIDCCallbackSyncsSSOGroups(t *testing.T) {
	authIntegrationDB(t)
	f := newSSOGroupFixture(t)

	idp := newFakeIdP(t)
	idp.issueNonce = "n-1"
	idp.extraIDClaims = map[string]interface{}{"sub": "sub-grp-" + uuid.NewString()[:8], "groups": []string{"ENG", "platform"}}
	h := idp.handler(t, func(s *OIDCProviderSettings) { s.GroupsClaimEnv = "OIDC_GROUPS_CLAIM" })
	ssoTestAuthConfig(h.cfg)

	w := oidcCallback(t, h, "n-1")
	if loc := w.Header().Get("Location"); w.Code != http.StatusTemporaryRedirect || strings.Contains(loc, "error=") {
		t.Fatalf("callback: %d %s", w.Code, loc)
	}
	var u models.User
	if err := database.DB.Where("sso_provider = ? AND sso_id = ?", "oidc", idp.extraIDClaims["sub"]).First(&u).Error; err != nil {
		t.Fatal(err)
	}
	wantMembers(t, "oidc login", u.ID, []models.Group{f.eng, f.multi}, []models.Group{f.ops, f.unmapped, f.manual})
	meta := lastLoginAudit(t, u.ID)
	if added, _ := meta["sso_groups_added"].([]interface{}); len(added) != 2 {
		t.Errorf("audit sso_groups_added = %v", meta["sso_groups_added"])
	}

	// Groups claim now missing from both ID token and UserInfo: fail closed.
	delete(idp.extraIDClaims, "groups")
	idp.issueNonce = "n-2"
	w = oidcCallback(t, h, "n-2")
	if loc := w.Header().Get("Location"); strings.Contains(loc, "error=") {
		t.Fatalf("second callback: %s", loc)
	}
	wantMembers(t, "oidc login without groups claim", u.ID, nil, []models.Group{f.eng, f.multi})
	meta = lastLoginAudit(t, u.ID)
	if removed, _ := meta["sso_groups_removed"].([]interface{}); len(removed) != 2 {
		t.Errorf("audit sso_groups_removed = %v", meta["sso_groups_removed"])
	}
}

// ── Vault JWT login (fake JWKS + Postgres) ───────────────────────────────────

func TestIntegrationVaultJWTSyncsSSOGroups(t *testing.T) {
	authIntegrationDB(t)
	f := newSSOGroupFixture(t)
	// The group grants a policy, so a user with no direct policies may log
	// in (the Vault JWT flow refuses users without any policy).
	pol := models.Policy{ID: uuid.New(), Name: "vj-" + uuid.NewString()[:6], Document: `{"Version":"2012-10-17","Statement":[]}`}
	if err := database.DB.Create(&pol).Error; err != nil {
		t.Fatal(err)
	}
	database.DB.Exec(`INSERT INTO group_policies (group_id, policy_id) VALUES (?, ?)`, f.ops.ID, pol.ID)

	idp := newFakeIdP(t)
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/jwt/.well-known/jwks.json" {
			http.NotFound(w, r)
			return
		}
		resp, err := http.Get(idp.srv.URL + "/jwks")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close() //nolint:errcheck // test helper
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(vault.Close)

	cfg := &config.Config{}
	ssoTestAuthConfig(cfg)
	cfg.VaultSSO.Enabled = true
	cfg.VaultSSO.Address = vault.URL
	cfg.VaultSSO.JWTPath = "auth/jwt"
	cfg.VaultSSO.Audience = "object-storage"
	cfg.VaultSSO.JWTGroupsClaim = "teams"
	h := NewVaultJWTHandler(cfg)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", h.LoginWithVaultJWT)

	sub := "vault-ent-" + uuid.NewString()[:8]
	login := func(extra jwt.MapClaims) *httptest.ResponseRecorder {
		claims := jwt.MapClaims{"sub": sub, "aud": "object-storage", "exp": time.Now().Add(time.Minute).Unix(),
			"email": sub + "@example.test", "name": sub}
		for k, v := range extra {
			claims[k] = v
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "rsa1"
		s, err := tok.SignedString(idp.rsaKey)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]string{"token": s})
		req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	if w := login(jwt.MapClaims{"teams": []string{"Ops", "Eng"}}); w.Code != http.StatusOK {
		t.Fatalf("vault jwt login: %d %s", w.Code, w.Body.String())
	}
	var u models.User
	if err := database.DB.Where("sso_provider = ? AND sso_id = ?", "vault", sub).First(&u).Error; err != nil {
		t.Fatal(err)
	}
	wantMembers(t, "vault jwt login", u.ID, []models.Group{f.ops, f.eng, f.multi}, []models.Group{f.unmapped, f.manual})

	// Claim absent: removed from all linked groups, and — with no policy
	// left, direct or via groups — the login is refused.
	if w := login(nil); w.Code != http.StatusForbidden {
		t.Fatalf("login without groups claim: %d %s, want 403 (no permissions)", w.Code, w.Body.String())
	}
	wantMembers(t, "vault jwt without claim", u.ID, nil, []models.Group{f.ops, f.eng, f.multi})
}
