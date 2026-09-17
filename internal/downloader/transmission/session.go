package transmission

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DownloadDir returns Transmission's default download-dir. The request asks for
// that one field so the reply carries nothing else, and the decode target has
// no other field. A Transmission too old to honour "fields" returns the whole
// session, which is still decoded down to the one value.
func (c *Client) DownloadDir(ctx context.Context) (string, error) {
	req, err := c.buildRequest(ctx, "session-get", map[string]interface{}{
		"fields": []string{"download-dir"},
	})
	if err != nil {
		return "", err
	}
	body, err := c.doRequest(req)
	if err != nil {
		return "", err
	}
	var resp struct {
		Result    string `json:"result"`
		Arguments struct {
			DownloadDir string `json:"download-dir"`
		} `json:"arguments"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode session-get response: %w", err)
	}
	if resp.Result != "success" {
		return "", fmt.Errorf("session-get failed: %s", resp.Result)
	}
	return strings.TrimSpace(resp.Arguments.DownloadDir), nil
}
