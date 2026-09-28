package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"bkt/internal/database"
	"bkt/internal/models"

	"github.com/google/uuid"
)

// The admin user list must report real policy counts: it used to return users
// without policies, so the console showed "0 policies" for everyone and the
// Manage Policies dialog treated attached policies as unattached.
func TestIntegrationListUsersPolicyCounts(t *testing.T) {
	cfg := integrationDB(t)
	admin := mkUser(t, "pc-admin-"+uuid.NewString()[:6], "password-123", true, false)
	alice := mkUser(t, "pc-alice-"+uuid.NewString()[:6], "password-123", false, false)
	bob := mkUser(t, "pc-bob-"+uuid.NewString()[:6], "password-123", false, false)

	mkPolicy := func(name string) models.Policy {
		p := models.Policy{ID: uuid.New(), Name: name + "-" + uuid.NewString()[:6],
			Document: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"]}]}`}
		if err := database.DB.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			database.DB.Exec(`DELETE FROM user_policies WHERE policy_id = ?`, p.ID)
			database.DB.Exec(`DELETE FROM group_policies WHERE policy_id = ?`, p.ID)
			database.DB.Exec(`DELETE FROM policies WHERE id = ?`, p.ID)
		})
		return p
	}
	p1, p2, p3 := mkPolicy("pc1"), mkPolicy("pc2"), mkPolicy("pc3")
	if err := database.DB.Model(&alice).Association("Policies").Append(&p1, &p2); err != nil {
		t.Fatal(err)
	}
	g := models.Group{ID: uuid.New(), Name: "pc-grp-" + uuid.NewString()[:6]}
	if err := database.DB.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.DB.Exec(`DELETE FROM user_groups WHERE group_id = ?`, g.ID)
		database.DB.Exec(`DELETE FROM group_policies WHERE group_id = ?`, g.ID)
		database.DB.Exec(`DELETE FROM groups WHERE id = ?`, g.ID)
	})
	database.DB.Exec(`INSERT INTO group_policies (group_id, policy_id) VALUES (?, ?), (?, ?)`, g.ID, p2.ID, g.ID, p3.ID)
	database.DB.Exec(`INSERT INTO user_groups (group_id, user_id) VALUES (?, ?), (?, ?)`, g.ID, alice.ID, g.ID, bob.ID)

	h := NewUserHandler(cfg)
	r := itRouter(admin)
	r.GET("/api/users", h.ListUsers)
	w := itDo(r, http.MethodGet, "/api/users", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list users: %d %s", w.Code, w.Body.String())
	}
	var users []struct {
		ID               uuid.UUID   `json:"id"`
		PolicyCount      *int        `json:"policy_count"`
		GroupPolicyCount *int        `json:"group_policy_count"`
		PolicyIDs        []uuid.UUID `json:"policy_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &users); err != nil {
		t.Fatal(err)
	}
	got := map[uuid.UUID][3]int{}
	ids := map[uuid.UUID]map[uuid.UUID]bool{}
	for _, u := range users {
		if u.PolicyCount == nil || u.GroupPolicyCount == nil {
			t.Fatalf("user %s: counts missing from response", u.ID)
		}
		got[u.ID] = [3]int{*u.PolicyCount, *u.GroupPolicyCount, len(u.PolicyIDs)}
		ids[u.ID] = map[uuid.UUID]bool{}
		for _, id := range u.PolicyIDs {
			ids[u.ID][id] = true
		}
	}
	if got[alice.ID] != [3]int{2, 2, 2} || !ids[alice.ID][p1.ID] || !ids[alice.ID][p2.ID] {
		t.Errorf("alice = %v ids=%v, want 2 direct (p1,p2), 2 via groups", got[alice.ID], ids[alice.ID])
	}
	if got[bob.ID] != [3]int{0, 2, 0} {
		t.Errorf("bob = %v, want 0 direct, 2 via groups", got[bob.ID])
	}
	if got[admin.ID] != [3]int{0, 0, 0} {
		t.Errorf("admin = %v, want 0/0/0", got[admin.ID])
	}
}
