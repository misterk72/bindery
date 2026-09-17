package sabnzbd

import "testing"

func TestResolveCompleteDir(t *testing.T) {
	tests := []struct {
		name, complete, cat, want string
	}{
		{"no category folder", "/downloads/complete", "", "/downloads/complete"},
		{"relative category folder", "/downloads/complete", "books", "/downloads/complete/books"},
		{"relative with trailing slash on complete", "/downloads/complete/", "books/", "/downloads/complete/books"},
		{"absolute category folder wins", "/downloads/complete", "/srv/books", "/srv/books"},
		{"asterisk means no job folder", "/downloads/complete", "books*", "/downloads/complete/books"},
		{"windows complete keeps backslashes", `C:\Downloads\complete`, "books", `C:\Downloads\complete\books`},
		{"windows absolute category folder", `C:\Downloads\complete`, `D:\Books`, `D:\Books`},
		{"relative complete cannot be anchored", "Downloads/complete", "books", ""},
		{"empty complete", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveCompleteDir(tt.complete, tt.cat); got != tt.want {
				t.Errorf("resolveCompleteDir(%q, %q) = %q, want %q", tt.complete, tt.cat, got, tt.want)
			}
		})
	}
}
