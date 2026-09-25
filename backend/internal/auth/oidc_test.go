package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"bkt/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// ── Fake identity provider ───────────────────────────────────────────────────

// fakeIdP is a minimal OIDC provider: discovery, JWKS, token and userinfo
// endpoints. It records what the client sent so tests can assert on the
// PKCE verifier and client authentication.
type fakeIdP struct {
	srv       *httptest.Server
	rsaKey    *rsa.PrivateKey
	ecKey     *ecdsa.PrivateKey
	clientID  string
	secret    string
	authKinds []string // token_endpoint_auth_methods_supported

	// expectations
	wantChallenge string // S256 challenge the /token endpoint must see a verifier for
	issueNonce    string // nonce to put in the ID token (normally = the one the client sent)
	signWith      string // "RS256" (default), "ES256", "HS256"
	audience      string // defaults to clientID
	extraIDClaims map[string]interface{}
	userInfo      map[string]interface{}
	advIssuer     *string // overrides the issuer advertised in discovery

	// recordings
	gotVerifier   string
	gotBasicUser  string
	gotBasicPass  string
	gotPostSecret string
	userInfoHits  int
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{rsaKey: rsaKey, ecKey: ecKey, clientID: "bkt-client", signWith: "RS256"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := f.srv.URL
		if f.advIssuer != nil {
			issuer = *f.advIssuer
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"issuer":                                issuer,
			"jwks_uri":                              f.srv.URL + "/jwks",
			"authorization_endpoint":                f.srv.URL + "/authorize",
			"token_endpoint":                        f.srv.URL + "/token",
			"userinfo_endpoint":                     f.srv.URL + "/userinfo",
			"token_endpoint_auth_methods_supported": f.authKinds,
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pub := f.rsaKey.PublicKey
		ecPoint, _ := f.ecKey.PublicKey.Bytes() // 0x04 || X || Y (32 bytes each for P-256)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"keys": []map[string]interface{}{
			{"kty": "RSA", "kid": "rsa1", "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())},
			{"kty": "EC", "kid": "ec1", "use": "sig", "alg": "ES256", "crv": "P-256",
				"x": base64.RawURLEncoding.EncodeToString(ecPoint[1:33]),
				"y": base64.RawURLEncoding.EncodeToString(ecPoint[33:65])},
			{"kty": "RSA", "kid": "enc1", "use": "enc", "n": "AQAB", "e": "AQAB"}, // must be ignored
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f.gotVerifier = r.PostForm.Get("code_verifier")
		f.gotPostSecret = r.PostForm.Get("client_secret")
		f.gotBasicUser, f.gotBasicPass, _ = r.BasicAuth()
		if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") != "good-code" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		if f.wantChallenge != "" && generateCodeChallenge(f.gotVerifier) != f.wantChallenge {
			http.Error(w, `{"error":"invalid_grant","error_description":"PKCE verification failed"}`, http.StatusBadRequest)
			return
		}
		if f.secret != "" && f.gotBasicPass != f.secret && f.gotPostSecret != f.secret {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "at-123", "token_type": "Bearer", "expires_in": 300,
			"id_token": f.mintIDToken(t),
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		f.userInfoHits++
		if r.Header.Get("Authorization") != "Bearer at-123" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if f.userInfo == nil {
			http.Error(w, "none", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.userInfo)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) mintIDToken(t *testing.T) string {
	t.Helper()
	aud := f.audience
	if aud == "" {
		aud = f.clientID
	}
	claims := jwt.MapClaims{
		"iss": f.srv.URL, "aud": aud, "sub": "sub-42",
		"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
		"nonce": f.issueNonce, "email": "Jane.Doe@Example.com", "email_verified": true,
		"name": "Jane Doe", "preferred_username": "jdoe",
	}
	for k, v := range f.extraIDClaims {
		claims[k] = v
	}
	var tok *jwt.Token
	var key interface{}
	switch f.signWith {
	case "ES256":
		tok = jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = "ec1"
		key = f.ecKey
	case "HS256":
		tok = jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		tok.Header["kid"] = "rsa1"
		key = []byte("attacker-knows-the-public-key")
	default:
		tok = jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = "rsa1"
		key = f.rsaKey
	}
	s, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fakeIdP) handler(t *testing.T, mutate func(*OIDCProviderSettings)) *OIDCHandler {
	t.Helper()
	cfg := &config.Config{}
	cfg.Server.FrontendURL = "https://console.example"
	s := OIDCProviderSettings{
		Key: "oidc", DisplayName: "TestIdP", IssuerURL: f.srv.URL, ClientID: f.clientID, ClientSecret: f.secret,
		RedirectURL: "https://console.example/api/auth/oidc/callback", Scopes: "profile email",
		FrontendCallbackPath: "/auth/oidc/callback", CookiePrefix: "oidc_",
	}
	if mutate != nil {
		mutate(&s)
	}
	return newOIDCHandler(cfg, s)
}

// ── Flow tests against the fake provider ─────────────────────────────────────

func TestOIDCAuthenticate_PKCEConfidentialClient(t *testing.T) {
	f := newFakeIdP(t)
	f.secret = "s3cret"
	f.authKinds = []string{"client_secret_basic", "client_secret_post"}
	verifier, _ := generateCodeVerifier()
	f.wantChallenge = generateCodeChallenge(verifier)
	f.issueNonce = "nonce-1"
	f.extraIDClaims = map[string]interface{}{"groups": []string{"bkt-users"}}
	f.userInfo = map[string]interface{}{"sub": "sub-42", "groups": []interface{}{"bkt-admins", "bkt-users"}, "preferred_username": "jane.doe", "policies": "readers writers"}

	h := f.handler(t, nil)
	id, err := h.authenticate(context.Background(), "good-code", verifier, "nonce-1")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if f.gotVerifier != verifier {
		t.Errorf("token endpoint did not receive the PKCE verifier")
	}
	if f.gotBasicUser != f.clientID || f.gotBasicPass != f.secret {
		t.Errorf("expected client_secret_basic auth, got user=%q pass=%q post=%q", f.gotBasicUser, f.gotBasicPass, f.gotPostSecret)
	}
	if id.Subject != "sub-42" || id.Email != "Jane.Doe@Example.com" || !id.EmailVerified {
		t.Errorf("identity mismatch: %+v", id)
	}
	// UserInfo wins for profile/groups; policies parsed from a space-separated string.
	if id.Username != "jane.doe" {
		t.Errorf("username = %q, want jane.doe (from userinfo preferred_username)", id.Username)
	}
	if !id.HasGroups || strings.Join(id.Groups, ",") != "bkt-admins,bkt-users" {
		t.Errorf("groups = %v", id.Groups)
	}
	if strings.Join(id.Policies, ",") != "readers,writers" {
		t.Errorf("policies = %v", id.Policies)
	}
	if f.userInfoHits != 1 {
		t.Errorf("userinfo hits = %d", f.userInfoHits)
	}
}

func TestOIDCAuthenticate_PublicClientAndPostSecret(t *testing.T) {
	f := newFakeIdP(t)
	verifier, _ := generateCodeVerifier()
	f.wantChallenge = generateCodeChallenge(verifier)
	f.issueNonce = "n"
	h := f.handler(t, nil) // no secret → public client
	if _, err := h.authenticate(context.Background(), "good-code", verifier, "n"); err != nil {
		t.Fatalf("public client: %v", err)
	}
	if f.gotBasicUser != "" || f.gotPostSecret != "" {
		t.Errorf("public client must not send credentials")
	}

	// Provider that only supports client_secret_post.
	f.secret = "post-only"
	f.authKinds = []string{"client_secret_post"}
	h = f.handler(t, nil)
	if _, err := h.authenticate(context.Background(), "good-code", verifier, "n"); err != nil {
		t.Fatalf("post-only client: %v", err)
	}
	if f.gotPostSecret != "post-only" || f.gotBasicPass != "" {
		t.Errorf("expected client_secret_post, got basic=%q post=%q", f.gotBasicPass, f.gotPostSecret)
	}
}

func TestOIDCAuthenticate_WrongVerifierRejectedByProvider(t *testing.T) {
	f := newFakeIdP(t)
	verifier, _ := generateCodeVerifier()
	f.wantChallenge = generateCodeChallenge(verifier)
	f.issueNonce = "n"
	h := f.handler(t, nil)
	_, err := h.authenticate(context.Background(), "good-code", "not-the-verifier-at-all-1234567890abcdef", "n")
	if err == nil || !strings.Contains(err.Error(), "PKCE") {
		t.Fatalf("expected PKCE failure from provider, got %v", err)
	}
}

func TestOIDCAuthenticate_NonceAndAudienceChecked(t *testing.T) {
	f := newFakeIdP(t)
	f.issueNonce = "expected"
	h := f.handler(t, nil)
	if _, err := h.authenticate(context.Background(), "good-code", "v", "different"); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Errorf("nonce mismatch not rejected: %v", err)
	}
	f.audience = "someone-else"
	if _, err := h.authenticate(context.Background(), "good-code", "v", "expected"); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Errorf("audience mismatch not rejected: %v", err)
	}
}

func TestOIDCAuthenticate_ES256Accepted_HS256Rejected(t *testing.T) {
	f := newFakeIdP(t)
	f.issueNonce = "n"
	f.signWith = "ES256"
	h := f.handler(t, nil)
	if _, err := h.authenticate(context.Background(), "good-code", "v", "n"); err != nil {
		t.Fatalf("ES256 ID token should verify: %v", err)
	}
	f.signWith = "HS256"
	_, err := h.authenticate(context.Background(), "good-code", "v", "n")
	if err == nil || !strings.Contains(err.Error(), "signing method") {
		t.Fatalf("HS256 must be rejected (key confusion), got %v", err)
	}
}

func TestOIDCAuthenticate_UserInfoForOtherSubjectIgnored(t *testing.T) {
	f := newFakeIdP(t)
	f.issueNonce = "n"
	f.userInfo = map[string]interface{}{"sub": "someone-else", "preferred_username": "mallory", "groups": []interface{}{"bkt-admins"}}
	h := f.handler(t, nil)
	id, err := h.authenticate(context.Background(), "good-code", "v", "n")
	if err != nil {
		t.Fatal(err)
	}
	if id.Username != "jdoe" || id.HasGroups {
		t.Errorf("userinfo for a different subject must be ignored: %+v", id)
	}
}

func TestOIDCInitiate_RedirectCarriesPKCEAndSetsCookies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newFakeIdP(t)
	h := f.handler(t, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login", nil)
	c.Request.Header.Set("X-Forwarded-Proto", "https")
	h.Initiate(c)

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := loc.Query()
	if !strings.HasPrefix(loc.String(), f.srv.URL+"/authorize?") {
		t.Errorf("redirect to %s, want provider authorize endpoint", loc)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("state") == "" || q.Get("nonce") == "" {
		t.Errorf("missing PKCE/state/nonce params: %v", q)
	}
	if q.Get("response_type") != "code" || !strings.Contains(q.Get("scope"), "openid") || q.Get("client_id") != f.clientID {
		t.Errorf("unexpected params: %v", q)
	}
	cookies := map[string]*http.Cookie{}
	for _, ck := range w.Result().Cookies() {
		cookies[ck.Name] = ck
	}
	for _, n := range []string{"oidc_oauth_state", "oidc_pkce_verifier", "oidc_oidc_nonce"} {
		ck, ok := cookies[n]
		if !ok {
			t.Errorf("cookie %s not set", n)
			continue
		}
		if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie %s flags: httponly=%v secure=%v samesite=%v", n, ck.HttpOnly, ck.Secure, ck.SameSite)
		}
	}
	// The challenge in the URL must correspond to the verifier in the cookie.
	if generateCodeChallenge(cookies["oidc_pkce_verifier"].Value) != q.Get("code_challenge") {
		t.Errorf("code_challenge does not match cookie verifier")
	}
	if cookies["oidc_oauth_state"].Value != q.Get("state") || cookies["oidc_oidc_nonce"].Value != q.Get("nonce") {
		t.Errorf("state/nonce cookies do not match URL params")
	}
}

func TestOIDCCallback_StateMismatchRedirectsWithError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := newFakeIdP(t)
	h := f.handler(t, nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=good-code&state=attacker", nil)
	c.Request.AddCookie(&http.Cookie{Name: "oidc_oauth_state", Value: "legit"})
	h.Callback(c)
	loc := w.Header().Get("Location")
	if w.Code != http.StatusTemporaryRedirect || !strings.HasPrefix(loc, "https://console.example/auth/oidc/callback#error=invalid_state") {
		t.Fatalf("status=%d location=%s", w.Code, loc)
	}
}

// ── Pure helpers ─────────────────────────────────────────────────────────────

func TestResolveRole(t *testing.T) {
	cases := []struct {
		name         string
		groups       []string
		has          bool
		admin, user  string
		wantAdmin    bool
		wantDecision roleDecision
	}{
		{"nothing configured: allow, never admin", []string{"bkt-admins"}, true, "", "", false, roleAllow},
		{"admin group only: member is admin", []string{"x", "bkt-admins"}, true, "bkt-admins", "", true, roleAllow},
		{"admin group only: non-member is user", []string{"x"}, true, "bkt-admins", "", false, roleAllow},
		{"admin group only: no claim still allowed", nil, false, "bkt-admins", "", false, roleAllow},
		{"user group: member allowed", []string{"bkt-users"}, true, "bkt-admins", "bkt-users", false, roleAllow},
		{"user group: admin takes precedence", []string{"bkt-users", "bkt-admins"}, true, "bkt-admins", "bkt-users", true, roleAllow},
		{"user group: not member denied", []string{"other"}, true, "bkt-admins", "bkt-users", false, roleDenyNotMember},
		{"user group: no claim denied distinctly", nil, false, "bkt-admins", "bkt-users", false, roleDenyNoGroupsClaim},
		{"user group only, empty admin", []string{"bkt-users"}, true, "", "bkt-users", false, roleAllow},
	}
	for _, tc := range cases {
		gotAdmin, gotDec := resolveRole(tc.groups, tc.has, tc.admin, tc.user)
		if gotAdmin != tc.wantAdmin || gotDec != tc.wantDecision {
			t.Errorf("%s: got (%v,%v) want (%v,%v)", tc.name, gotAdmin, gotDec, tc.wantAdmin, tc.wantDecision)
		}
	}
}

func TestDeriveUsernameAndSanitize(t *testing.T) {
	claims := map[string]string{"upn": "jane@corp.example", "preferred_username": "Jane Doe!", "name": "Jane"}
	str := func(k string) string { return claims[k] }
	if got := deriveUsername("upn", str, "jane@corp.example", "sub"); got != "jane_corp.example" {
		t.Errorf("configured claim: %q", got)
	}
	if got := deriveUsername("", str, "jane@corp.example", "sub"); got != "Jane_Doe" {
		t.Errorf("preferred_username sanitized: %q", got)
	}
	if got := deriveUsername("missing", func(string) string { return "" }, "Bob.Smith@x.io", "sub"); got != "Bob.Smith" {
		t.Errorf("email local part: %q", got)
	}
	if got := deriveUsername("", func(string) string { return "" }, "", "  ///  "); got != "user" {
		t.Errorf("fallback: %q", got)
	}
	if got := sanitizeUsername(strings.Repeat("a", 100)); len(got) != 64 {
		t.Errorf("length cap: %d", len(got))
	}
}

func TestClaimToStringSlice(t *testing.T) {
	if got := claimToStringSlice([]interface{}{"a", " b ", 3.0, ""}); strings.Join(got, ",") != "a,b,3" {
		t.Errorf("array: %v", got)
	}
	if got := claimToStringSlice("a, b ,c"); strings.Join(got, ",") != "a,b,c" {
		t.Errorf("comma string: %v", got)
	}
	if got := claimToStringSlice("a b"); strings.Join(got, ",") != "a,b" {
		t.Errorf("space string: %v", got)
	}
	if got := claimToStringSlice(42); got != nil {
		t.Errorf("unsupported: %v", got)
	}
}

func TestEnsureOpenIDScopeAndAuthMethod(t *testing.T) {
	if got := ensureOpenIDScope("profile  email"); got != "openid profile email" {
		t.Errorf("scope: %q", got)
	}
	if got := ensureOpenIDScope("openid profile"); got != "openid profile" {
		t.Errorf("scope kept: %q", got)
	}
	if !clientSecretBasicPreferred(nil) || !clientSecretBasicPreferred([]string{"client_secret_basic", "client_secret_post"}) {
		t.Error("basic should be preferred by default")
	}
	if clientSecretBasicPreferred([]string{"client_secret_post"}) {
		t.Error("post-only providers should get client_secret_post")
	}
}

func TestPKCEChallengeIsS256(t *testing.T) {
	// RFC 7636 appendix B test vector.
	v := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	if got := generateCodeChallenge(v); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("challenge = %s", got)
	}
	verifier, err := generateCodeVerifier()
	if err != nil || len(verifier) < 43 || len(verifier) > 128 {
		t.Errorf("verifier length %d err %v", len(verifier), err)
	}
}

func TestOIDCDiscoveryIssuerMustMatchConfiguredIssuer(t *testing.T) {
	f := newFakeIdP(t)
	f.issueNonce = "n"
	// Configured issuer differs from what discovery advertises.
	h := f.handler(t, func(s *OIDCProviderSettings) { s.IssuerURL = f.srv.URL + "/" })
	if _, err := h.authenticate(context.Background(), "good-code", "v", "n"); err != nil {
		t.Fatalf("trailing slash difference should be tolerated: %v", err)
	}
	for _, adv := range []string{"https://evil.example", ""} {
		adv := adv
		f.advIssuer = &adv
		h = f.handler(t, nil) // fresh handler: no cached discovery
		if _, err := h.authenticate(context.Background(), "good-code", "v", "n"); err == nil || !strings.Contains(err.Error(), "issuer") {
			t.Errorf("advertised issuer %q must be rejected, got %v", adv, err)
		}
	}
}

func TestOIDCUserInfoWithoutSubIgnored(t *testing.T) {
	f := newFakeIdP(t)
	f.issueNonce = "n"
	f.userInfo = map[string]interface{}{"preferred_username": "mallory", "groups": []interface{}{"bkt-admins"}, "policies": []interface{}{"admin"}}
	h := f.handler(t, nil)
	id, err := h.authenticate(context.Background(), "good-code", "v", "n")
	if err != nil {
		t.Fatal(err)
	}
	if id.Username != "jdoe" || id.HasGroups || id.HasPolicies {
		t.Errorf("userinfo without sub must be ignored: %+v", id)
	}
}

func TestOIDCPoliciesClaimPresenceTracked(t *testing.T) {
	f := newFakeIdP(t)
	f.issueNonce = "n"
	h := f.handler(t, nil)
	id, err := h.authenticate(context.Background(), "good-code", "v", "n")
	if err != nil {
		t.Fatal(err)
	}
	if id.HasPolicies {
		t.Error("no policies claim: HasPolicies must be false")
	}
	// An explicitly empty claim is still "present" → policies get cleared.
	f.extraIDClaims = map[string]interface{}{"policies": []interface{}{}}
	id, err = h.authenticate(context.Background(), "good-code", "v", "n")
	if err != nil {
		t.Fatal(err)
	}
	if !id.HasPolicies || len(id.Policies) != 0 {
		t.Errorf("empty policies claim: %+v", id)
	}
}
