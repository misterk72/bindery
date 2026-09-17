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
// Bindery adds it under category, and a phrase naming where that came from.
// A category with its own DestDir uses it. Otherwise the global DestDir is
// used, and when AppendCategoryDir is on (NZBGet's default) a non-empty
// category gets its own subfolder named after the category.
func (c *Client) GrabDestDir(ctx context.Context, category string) (string, string, error) {
	var cfg destDirConfig
	if err := c.call(ctx, "config", nil, &cfg); err != nil {
		return "", "", fmt.Errorf("read nzbget config: %w", err)
	}
	category = strings.TrimSpace(category)
	var matched string
	if category != "" {
		for name, value := range cfg {
			if !categoryNameKey.MatchString(name) || !strings.EqualFold(value, category) {
				continue
			}
			matched = value
			if m := categoryIndexKey.FindStringSubmatch(name); m != nil {
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
	if category != "" && !strings.EqualFold(strings.TrimSpace(cfg["AppendCategoryDir"]), "no") {
		name := category
		if matched != "" {
			name = matched
		}
		return pathmap.JoinClientPath(dest, name), "DestDir plus the category name (AppendCategoryDir)", nil
	}
	return dest, "DestDir", nil
}
