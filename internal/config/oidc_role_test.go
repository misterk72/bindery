package config

import "testing"

// BINDERY_OIDC_DEFAULT_ROLE accepts the three role names, case-insensitively,
// and falls back to user for anything else. requester is supported so an
// operator can make every new OIDC login a requester until an admin says
// otherwise, which is the safer default for a shared instance.
func TestLoad_OIDCDefaultRole(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", "user"},
		{"user", "user"},
		{"admin", "admin"},
		{" Admin ", "admin"},
		{"requester", "requester"},
		{"REQUESTER", "requester"},
		{"moderator", "user"},
		{"requesters", "user"},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			t.Setenv("BINDERY_OIDC_DEFAULT_ROLE", c.raw)
			if got := Load().OIDCDefaultRole; got != c.want {
				t.Fatalf("BINDERY_OIDC_DEFAULT_ROLE=%q gives %q, want %q", c.raw, got, c.want)
			}
		})
	}
}
