package auth

import "testing"

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
