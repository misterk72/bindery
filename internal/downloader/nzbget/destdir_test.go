package nzbget

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGrabDestDir(t *testing.T) {
	base := []configEntry{
		{Name: "MainDir", Value: "/data/nzbget"},
		{Name: "DestDir", Value: "${MainDir}/completed"},
		{Name: "Category1.Name", Value: "Books"},
		{Name: "Category2.Name", Value: "audio"},
		{Name: "Category2.DestDir", Value: "/library/audio"},
	}
	tests := []struct {
		name, category string
		extra          []configEntry
		want           string
	}{
		{name: "category without DestDir appends its name by default", category: "Books", want: "/data/nzbget/completed/Books"},
		{name: "AppendCategoryDir yes", category: "Books", extra: []configEntry{{Name: "AppendCategoryDir", Value: "yes"}}, want: "/data/nzbget/completed/Books"},
		{name: "AppendCategoryDir no", category: "Books", extra: []configEntry{{Name: "AppendCategoryDir", Value: "no"}}, want: "/data/nzbget/completed"},
		{name: "category with its own DestDir", category: "audio", want: "/library/audio"},
		{name: "no category", category: "", want: "/data/nzbget/completed"},
		// Scanner::ResolveCategory swaps in the configured name on a case
		// insensitive match, so the subfolder uses the config's casing.
		{name: "sent in other case uses the configured name", category: "books", want: "/data/nzbget/completed/Books"},
		// A trailing space stops the match; SanitizeRelativePath then trims it.
		{name: "trailing space does not match but is trimmed", category: "Books ", want: "/data/nzbget/completed/Books"},
		{name: "unknown category keeps its own name", category: "comics.", want: "/data/nzbget/completed/comics"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := append(append([]configEntry{}, base...), tc.extra...)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(configResponse{Result: cfg})
			}))
			defer srv.Close()
			host, port := serverHostPort(t, srv.URL)
			got, _, err := New(host, port, "", "", "", false).GrabDestDir(context.Background(), tc.category)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("GrabDestDir(%q) = %q, want %q", tc.category, got, tc.want)
			}
		})
	}
}

func TestDestDirConfigKeepsOnlyNeededKeys(t *testing.T) {
	var cfg destDirConfig
	body := `{"result":[{"Name":"DestDir","Value":"/d"},{"Name":"Server1.Password","Value":"secret"},{"Name":"Category1.Name","Value":"books"},{"Name":"Category1.DestDir","Value":"/b"},{"Name":"Category1.PostScript","Value":"x"}]}`
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"DestDir": "/d", "Category1.Name": "books", "Category1.DestDir": "/b"}
	if len(cfg) != len(want) {
		t.Fatalf("kept %v, want %v", cfg, want)
	}
	for k, v := range want {
		if cfg[k] != v {
			t.Errorf("%s = %q, want %q", k, cfg[k], v)
		}
	}
}
