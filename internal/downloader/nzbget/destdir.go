package nzbget

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vavallee/bindery/internal/pathmap"
)

// destDirKeys are the only NZBGet options GrabDestDir keeps. NZBGet's config
// RPC has no server side filter, so the whole option list (server passwords
// included) comes over the wire; each entry is decoded and dropped at once
// unless its name is one of these or a category's Name or DestDir.
var destDirKeys = map[string]bool{
	"MainDir": true, "DestDir": true, "InterDir": true, "AppDir": true, "ConfigDir": true,
	"ScriptDir": true, "QueueDir": true, "NzbDir": true, "AppendCategoryDir": true,
}

// destDirConfig collects the needed options while decoding the config reply.
type destDirConfig map[string]string

func (d *destDirConfig) UnmarshalJSON(data []byte) error {
	var envelope struct {
		Result []json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	out := destDirConfig{}
	for _, raw := range envelope.Result {
		var e struct {
			Name  string `json:"Name"`
			Value string `json:"Value"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			continue
		}
		if destDirKeys[e.Name] || categoryNameKey.MatchString(e.Name) ||
			(strings.HasSuffix(e.Name, ".DestDir") && categoryIndexKey.MatchString(e.Name)) {
			out[e.Name] = e.Value
		}
	}
	*d = out
	return nil
}

// GrabDestDir returns the folder NZBGet puts a finished download in when
// Bindery adds it with category, the category exactly as Add sends it, and a
// phrase naming where that came from.
//
// This mirrors NZBGet (github.com/nzbgetcom/nzbget, checked at 67033a6):
//   - Bindery adds NZB content, which goes through Scanner::AddExternalFile.
//     Scanner::ResolveCategory (daemon/queue/Scanner.cpp) replaces the sent
//     category with the configured category's own name when
//     Options::Categories::FindCategory matches it, and that match is
//     strcasecmp, so case does not matter but surrounding spaces do.
//   - NzbInfo::BuildFinalDirName (daemon/queue/DownloadInfo.cpp) uses the
//     category's DestDir when it has one; otherwise DestDir, plus a subfolder
//     named FileSystem::SanitizeRelativePath(category) when AppendCategoryDir
//     is on. SanitizeRelativePath trims spaces and tabs around each path
//     segment and trailing dots, which sanitizeCategoryDir follows. It also
//     replaces characters invalid in file names, which is not mirrored here.
//
// Category aliases, which FindCategory also matches, are not read.
func (c *Client) GrabDestDir(ctx context.Context, category string) (string, string, error) {
	var cfg destDirConfig
	if err := c.call(ctx, "config", nil, &cfg); err != nil {
		return "", "", fmt.Errorf("read nzbget config: %w", err)
	}
	name := category
	if category != "" {
		for key, value := range cfg {
			if !categoryNameKey.MatchString(key) || !strings.EqualFold(value, category) {
				continue
			}
			name = value
			if m := categoryIndexKey.FindStringSubmatch(key); m != nil {
				if d := strings.TrimSpace(cfg["Category"+m[1]+".DestDir"]); d != "" {
					return expandNZBGetDir(d, cfg), "the category DestDir", nil
				}
			}
			break
		}
	}
	dest := strings.TrimSpace(cfg["DestDir"])
	if dest == "" {
		return "", "", nil
	}
	dest = expandNZBGetDir(dest, cfg)
	if !strings.EqualFold(strings.TrimSpace(cfg["AppendCategoryDir"]), "no") {
		if sub := sanitizeCategoryDir(name); sub != "" {
			return pathmap.JoinClientPath(dest, sub), "DestDir plus the category name (AppendCategoryDir)", nil
		}
	}
	return dest, "DestDir", nil
}

// sanitizeCategoryDir follows the trimming FileSystem::SanitizeRelativePath
// and SanitizePathSegment apply in NZBGet: split on either slash, trim spaces
// and tabs before and spaces, tabs and dots after each segment, drop empty
// segments, and join with a forward slash.
func sanitizeCategoryDir(category string) string {
	segments := strings.FieldsFunc(category, func(r rune) bool { return r == '/' || r == '\\' })
	out := make([]string, 0, len(segments))
	for _, seg := range segments {
		seg = strings.TrimLeft(seg, " \t")
		seg = strings.TrimRight(seg, " \t.")
		if seg != "" {
			out = append(out, seg)
		}
	}
	return strings.Join(out, "/")
}
