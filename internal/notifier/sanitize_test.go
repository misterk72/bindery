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

// TestSafeText_BareURLsDoNotLink: chat services autolink a bare URL, so a
// provider title could carry a clickable phishing link. The scheme separator
// is broken; the text still reads, and ordinary formatting is untouched.
func TestSafeText_BareURLsDoNotLink(t *testing.T) {
	for _, in := range []string{
		"Free book at https://evil.example/login",
		"http://evil.example",
		"ftp://files.example/x",
		"HTTPS://EVIL.EXAMPLE",
	} {
		got := SafeText(in, 300)
		if strings.Contains(got, "://") {
			t.Errorf("SafeText(%q) = %q still has a linkable scheme", in, got)
		}
		if !strings.Contains(got, "evil.example") && !strings.Contains(got, "EVIL.EXAMPLE") && !strings.Contains(got, "files.example") {
			t.Errorf("SafeText(%q) = %q lost the readable address", in, got)
		}
	}
	if got := SafeText("`code` _under_ *bold* ~strike~", 300); got != "`code` _under_ *bold* ~strike~" {
		t.Errorf("formatting changed: %q", got)
	}
}
