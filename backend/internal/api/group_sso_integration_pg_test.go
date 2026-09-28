package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bkt/internal/auth"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Postgres-gated: SSO group → bkt group links through the real admin API
// routes (registerAPIRoutes, so the admin-only gate is the production one),
// and access granted through a synced membership.

func TestIntegrationGroupSSOLinksAPI(t *testing.T) {
	cfg := itConfig(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	registerAPIRoutes(r, cfg)

	admin := mkUser(t, "gsa-admin-"+uuid.NewString()[:6], "password-123", true, false)
	plain := mkUser(t, "gsa-user-"+uuid.NewString()[:6], "password-123", false, false)
	bearer := func(u models.User) string {
		at, _, err := auth.GenerateTokenPair(u.ID, u.Username, u.IsAdmin, u.TokenVersion, cfg.Auth.JWTSecret, 15*time.Minute, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + at
	}
	do := func(u models.User, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", bearer(u))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "bkt-test")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// Create with sso_groups: trimmed, deduplicated case-insensitively, sorted.
	name := "gsa-" + uuid.NewString()[:8]
	w := do(admin, http.MethodPost, "/api/groups", `{"name":"`+name+`","sso_groups":[" Eng ","ops","ENG"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created models.Group
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.DB.Exec(`DELETE FROM user_groups WHERE group_id = ?`, created.ID)
		database.DB.Exec(`DELETE FROM group_sso_links WHERE group_id = ?`, created.ID)
		database.DB.Exec(`DELETE FROM "groups" WHERE id = ?`, created.ID)
	})
	if strings.Join(created.SSOGroups, ",") != "Eng,ops" {
		t.Fatalf("created sso_groups = %v, want [Eng ops]", created.SSOGroups)
	}

	// Invalid sso_groups on create are rejected and create nothing.
	bad := "gsa-bad-" + uuid.NewString()[:6]
	if w := do(admin, http.MethodPost, "/api/groups", `{"name":"`+bad+`","sso_groups":["  "]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("create with empty sso group: %d, want 400", w.Code)
	}
	var n int64
	database.DB.Model(&models.Group{}).Where("name = ?", bad).Count(&n)
	if n != 0 {
		t.Fatal("group created despite invalid sso_groups")
	}

	// Group JSON (list) carries sso_groups; groups without links get [].
	plainGroup := models.Group{ID: uuid.New(), Name: "gsa-plain-" + uuid.NewString()[:6]}
	if err := database.DB.Create(&plainGroup).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Exec(`DELETE FROM "groups" WHERE id = ?`, plainGroup.ID) })
	listed := func() map[uuid.UUID]json.RawMessage {
		w := do(admin, http.MethodGet, "/api/groups", "")
		if w.Code != http.StatusOK {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var raw []map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		out := map[uuid.UUID]json.RawMessage{}
		for _, g := range raw {
			var id uuid.UUID
			_ = json.Unmarshal(g["id"], &id)
			out[id] = g["sso_groups"]
		}
		return out
	}
	l := listed()
	if string(l[created.ID]) != `["Eng","ops"]` || string(l[plainGroup.ID]) != `[]` {
		t.Fatalf("listed sso_groups: linked=%s plain=%s", l[created.ID], l[plainGroup.ID])
	}

	// PUT replaces; validation.
	path := "/api/groups/" + created.ID.String() + "/sso-groups"
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{}`, http.StatusBadRequest},                                                  // missing
		{`{"sso_groups":["ok",""]}`, http.StatusBadRequest},                            // empty name
		{`{"sso_groups":["` + strings.Repeat("x", 257) + `"]}`, http.StatusBadRequest}, // too long
		{`{"sso_groups":` + manyNames(101) + `}`, http.StatusBadRequest},               // too many
		{`{"sso_groups":` + manyNames(100) + `}`, http.StatusOK},                       // limit
		{`{"sso_groups":["` + strings.Repeat("y", 256) + `"]}`, http.StatusOK},         // max length
		{`{"sso_groups":["Platform","platform","  SRE "]}`, http.StatusOK},             // dedupe
	} {
		if w := do(admin, http.MethodPut, path, tc.body); w.Code != tc.want {
			t.Fatalf("PUT %.60s: %d %s, want %d", tc.body, w.Code, w.Body.String(), tc.want)
		}
	}
	if got := string(listed()[created.ID]); got != `["Platform","SRE"]` {
		t.Fatalf("after PUT sso_groups = %s", got)
	}
	// The change is audited.
	var audits int64
	database.DB.Model(&models.AuditLog{}).Where("action = ? AND resource_id = ?", "group.sso_groups_update", created.ID.String()).Count(&audits)
	if audits == 0 {
		t.Fatal("sso_groups update was not audited")
	}

	// Admin only: a regular user gets 403 on every group route, including PUT.
	if w := do(plain, http.MethodPut, path, `{"sso_groups":["x"]}`); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin PUT: %d, want 403", w.Code)
	}
	if w := do(plain, http.MethodGet, "/api/groups", ""); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin list: %d, want 403", w.Code)
	}
	if got := string(listed()[created.ID]); got != `["Platform","SRE"]` {
		t.Fatalf("non-admin PUT changed links: %s", got)
	}
	if w := do(admin, http.MethodPut, "/api/groups/"+uuid.NewString()+"/sso-groups", `{"sso_groups":[]}`); w.Code != http.StatusNotFound {
		t.Fatalf("PUT unknown group: %d, want 404", w.Code)
	}

	// Empty list unlinks (group becomes manual again).
	if w := do(admin, http.MethodPut, path, `{"sso_groups":[]}`); w.Code != http.StatusOK {
		t.Fatalf("clear: %d", w.Code)
	}
	if got := string(listed()[created.ID]); got != `[]` {
		t.Fatalf("after clear sso_groups = %s", got)
	}

	// Deleting a group removes its links (API path).
	if w := do(admin, http.MethodPut, path, `{"sso_groups":["gone"]}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := do(admin, http.MethodDelete, "/api/groups/"+created.ID.String(), ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	database.DB.Model(&models.GroupSSOLink{}).Where("group_id = ?", created.ID).Count(&n)
	if n != 0 {
		t.Fatalf("%d links left after group delete", n)
	}

	// ...and the FK cascades for a direct row delete too.
	casc := models.Group{ID: uuid.New(), Name: "gsa-casc-" + uuid.NewString()[:6]}
	if err := database.DB.Create(&casc).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Create(&models.GroupSSOLink{GroupID: casc.ID, SSOGroup: "x"}).Error; err != nil {
		t.Fatal(err)
	}
	// Case-insensitive uniqueness is enforced by the database as well.
	if err := database.DB.Create(&models.GroupSSOLink{GroupID: casc.ID, SSOGroup: "X"}).Error; err == nil {
		t.Fatal("duplicate (case-insensitive) link accepted by the database")
	}
	if err := database.DB.Exec(`DELETE FROM "groups" WHERE id = ?`, casc.ID).Error; err != nil {
		t.Fatal(err)
	}
	database.DB.Model(&models.GroupSSOLink{}).Where("group_id = ?", casc.ID).Count(&n)
	if n != 0 {
		t.Fatal("links not cascaded on group row delete")
	}
}

func manyNames(n int) string {
	names := make([]string, n)
	for i := range names {
		names[i] = uuid.NewString()
	}
	b, _ := json.Marshal(names)
	return string(b)
}

// A synced membership grants the group's policies through the normal policy
// evaluation, and losing the IdP group revokes them at the next sign-in.
func TestIntegrationSSOGroupSyncGrantsGroupPolicies(t *testing.T) {
	itConfig(t)
	owner := mkUser(t, "gsp-own-"+uuid.NewString()[:6], "password-123", true, false)
	b := itBucket(t, owner.ID, nil)

	sso := mkUser(t, "gsp-sso-"+uuid.NewString()[:6], "", false, false)
	if err := database.DB.Model(&models.User{}).Where("id = ?", sso.ID).
		Updates(map[string]interface{}{"sso_provider": "oidc", "sso_id": "sub-" + sso.ID.String()}).Error; err != nil {
		t.Fatal(err)
	}
	sso.SSOProvider = "oidc"

	pol := models.Policy{ID: uuid.New(), Name: "gsp-read-" + uuid.NewString()[:6],
		Document: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::` + b.Name + `/*"]}]}`}
	if err := database.DB.Create(&pol).Error; err != nil {
		t.Fatal(err)
	}
	g := models.Group{ID: uuid.New(), Name: "gsp-eng-" + uuid.NewString()[:6]}
	if err := database.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.DB.Exec(`DELETE FROM user_groups WHERE group_id = ?`, g.ID)
		database.DB.Exec(`DELETE FROM group_policies WHERE group_id = ?`, g.ID)
		database.DB.Exec(`DELETE FROM "groups" WHERE id = ?`, g.ID)
		database.DB.Exec(`DELETE FROM policies WHERE id = ?`, pol.ID)
	})
	database.DB.Exec(`INSERT INTO group_policies (group_id, policy_id) VALUES (?, ?)`, g.ID, pol.ID)
	if err := database.DB.Create(&models.GroupSSOLink{GroupID: g.ID, SSOGroup: "Engineering"}).Error; err != nil {
		t.Fatal(err)
	}

	ps := services.NewPolicyService()
	canRead := func() bool {
		ok, err := ps.CheckObjectAccess(sso.ID, b.Name, "file.txt", "s3:GetObject")
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if canRead() {
		t.Fatal("access before any sync")
	}
	if _, _, err := auth.SyncSSOGroupMemberships(database.DB, &sso, []string{"engineering"}, true, "test"); err != nil {
		t.Fatal(err)
	}
	if !canRead() {
		t.Fatal("synced group membership did not grant the group's policy")
	}
	// The admin user list's "+N via groups" count reflects the synced group.
	if _, viaGroups, err := userPolicyInfo(); err != nil || viaGroups[sso.ID] != 1 {
		t.Fatalf("group_policy_count = %d (err %v), want 1", viaGroups[sso.ID], err)
	}
	if _, _, err := auth.SyncSSOGroupMemberships(database.DB, &sso, []string{"sales"}, true, "test"); err != nil {
		t.Fatal(err)
	}
	if canRead() {
		t.Fatal("access kept after leaving the IdP group")
	}
}
