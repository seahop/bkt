package auth

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestPlanSSOGroupSync(t *testing.T) {
	eng, ops, both, manual := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	links := []ssoGroupLink{
		{GroupID: eng, SSOGroup: "Engineering"},
		{GroupID: ops, SSOGroup: "ops"},
		// One bkt group linked to two IdP groups.
		{GroupID: both, SSOGroup: "eng-leads"},
		{GroupID: both, SSOGroup: " OPS "},
	}
	set := func(ids ...uuid.UUID) map[uuid.UUID]bool {
		m := map[uuid.UUID]bool{}
		for _, id := range ids {
			m[id] = true
		}
		return m
	}
	eq := func(got []uuid.UUID, want ...uuid.UUID) bool {
		if len(got) != len(want) {
			return false
		}
		w := set(want...)
		for _, g := range got {
			if !w[g] {
				return false
			}
		}
		return true
	}

	cases := []struct {
		name        string
		current     map[uuid.UUID]bool
		idp         []string
		known       bool
		add, remove []uuid.UUID
	}{
		{"case-insensitive match, one IdP group maps to several bkt groups",
			set(), []string{"ENGINEERING", "Ops"}, true, []uuid.UUID{eng, ops, both}, nil},
		{"left an IdP group", set(eng, ops, both), []string{"ops"}, true, nil, []uuid.UUID{eng}},
		{"manual group membership never touched", set(manual), []string{}, true, nil, nil},
		{"empty-but-present claim removes linked memberships", set(eng, manual), []string{}, true, nil, []uuid.UUID{eng}},
		{"no group info fails closed", set(eng, ops, manual), []string{"Engineering"}, false, nil, []uuid.UUID{eng, ops}},
		{"already in sync", set(eng), []string{"engineering", "unmapped"}, true, nil, nil},
		{"blank IdP group names ignored", set(), []string{"", "  "}, true, nil, nil},
		{"exact match only (no prefix/substring)", set(), []string{"eng", "Engineering-EU"}, true, nil, nil},
	}
	for _, tc := range cases {
		add, remove := planSSOGroupSync(links, tc.current, tc.idp, tc.known)
		if !eq(add, tc.add...) || !eq(remove, tc.remove...) {
			t.Errorf("%s: add=%v remove=%v, want add=%v remove=%v", tc.name, add, remove, tc.add, tc.remove)
		}
	}

	// No links at all: nothing is SSO-managed.
	if add, remove := planSSOGroupSync(nil, set(eng), nil, false); len(add)+len(remove) != 0 {
		t.Errorf("no links: add=%v remove=%v", add, remove)
	}
}

func TestVaultJWTGroupsClaim(t *testing.T) {
	sign := func(claims jwt.MapClaims) string {
		s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("k"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	tok := sign(jwt.MapClaims{"sub": "x", "groups": []string{"eng", "ops"}, "teams": "a,b"})
	if g, ok := vaultJWTGroups(tok, "groups"); !ok || len(g) != 2 || g[0] != "eng" || g[1] != "ops" {
		t.Errorf("groups = %v %v", g, ok)
	}
	if g, ok := vaultJWTGroups(tok, "teams"); !ok || len(g) != 2 || g[1] != "b" {
		t.Errorf("custom claim = %v %v", g, ok)
	}
	if _, ok := vaultJWTGroups(tok, "missing"); ok {
		t.Error("absent claim reported as present")
	}
	if g, ok := vaultJWTGroups(sign(jwt.MapClaims{"sub": "x", "groups": []string{}}), "groups"); !ok || len(g) != 0 {
		t.Errorf("empty claim = %v %v, want present and empty", g, ok)
	}
}

func TestAddSSOGroupAudit(t *testing.T) {
	m := addSSOGroupAudit(map[string]interface{}{"provider": "oidc"}, []string{"a"}, nil)
	if m["sso_groups_added"] == nil || m["sso_groups_removed"] != nil || m["provider"] != "oidc" {
		t.Errorf("meta = %v", m)
	}
}
