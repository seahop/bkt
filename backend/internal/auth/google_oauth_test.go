package auth

import (
	"testing"

	"bkt/internal/models"

	"github.com/google/uuid"
)

func TestGoogleDomainAllowed(t *testing.T) {
	allowed := []string{"example.com", "example.org"}
	cases := []struct {
		email, hd string
		want      bool
	}{
		{"a@example.com", "example.com", true},
		{"a@EXAMPLE.org", "example.org", true},
		{"a@gmail.com", "", false},           // consumer account
		{"a@example.com", "", false},         // no hd: not a Workspace account
		{"a@evil.com", "example.com", false}, // email domain not allowed
		{"a@example.com", "evil.com", false}, // hd not allowed
		{"no-at-sign", "example.com", false},
	}
	for _, tc := range cases {
		if got := googleDomainAllowed(allowed, &GoogleUserInfo{Email: tc.email, HostedDomain: tc.hd}); got != tc.want {
			t.Errorf("%s/%s: got %v want %v", tc.email, tc.hd, got, tc.want)
		}
	}
	// No list: the domain gate is open (provisioning is gated separately).
	if !googleDomainAllowed(nil, &GoogleUserInfo{Email: "a@gmail.com"}) {
		t.Error("empty allow-list should not reject here")
	}
}

func TestMergeManagedPoliciesKeepsManualOnes(t *testing.T) {
	pol := func(name string) models.Policy { return models.Policy{ID: uuid.New(), Name: name} }
	manual, eng, ops, sales := pol("manual-extra"), pol("engineering"), pol("ops"), pol("sales")
	managed := map[string]bool{"engineering": true, "ops": true, "sales": true}

	// User had manual + engineering + ops; groups now map to ops + sales.
	got := mergeManagedPolicies([]models.Policy{manual, eng, ops}, []models.Policy{ops, sales}, []string{"ops", "sales"}, managed)
	names := map[string]bool{}
	for _, p := range got {
		if names[p.Name] {
			t.Fatalf("duplicate %s", p.Name)
		}
		names[p.Name] = true
	}
	if !names["manual-extra"] || names["engineering"] || !names["ops"] || !names["sales"] || len(got) != 3 {
		t.Fatalf("got %v", names)
	}

	// Leaving every group removes only the managed ones.
	got = mergeManagedPolicies([]models.Policy{manual, eng}, nil, nil, managed)
	if len(got) != 1 || got[0].Name != "manual-extra" {
		t.Fatalf("got %+v", got)
	}
}
