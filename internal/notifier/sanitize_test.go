package notifier

import (
	"strings"
	"testing"
)

// Every chat markup vector the review found must come out inert: no mention,
// no Slack escape, no markdown link. The text a person reads survives.
func TestSafeText_NeutralisesChatMarkup(t *testing.T) {
	cases := []struct {
		name, in string
	}{
		{"discord everyone", "@everyone new book"},
		{"discord here", "@here"},
		{"slack channel", "<!channel> read this"},
		{"slack here", "<!here>"},
		{"slack user", "<@U123ABC> look"},
		{"slack link", "<https://evil.example|Click me>"},
		{"discord role", "<@&123456>"},
		{"markdown link", "[Free nitro](https://evil.example)"},
		{"markdown image", "![x](https://evil.example/p.png)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SafeText(c.in, 300)
			for _, bad := range []string{"@", "<", ">", "[", "]", "]("} {
				if strings.Contains(got, bad) {
					t.Errorf("SafeText(%q) = %q still contains %q", c.in, got, bad)
				}
			}
			if got == "" {
				t.Errorf("SafeText(%q) emptied the text", c.in)
			}
		})
	}
	if got := SafeText("Dune: Part One", 300); got != "Dune: Part One" {
		t.Errorf("plain title changed: %q", got)
	}
}

func TestCleanText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  The   War\tof\nthe Worlds ", "The War of the Worlds"},
		{"Evil\u202Egnirts\u200B", "Evilgnirts"},
		{"bell\u0007ring", "bellring"},
		{strings.Repeat("a", 400), strings.Repeat("a", 300)},
		{"@keep <plain>", "@keep <plain>"},
	}
	for _, c := range cases {
		if got := CleanText(c.in, 300); got != c.want {
			t.Errorf("CleanText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
