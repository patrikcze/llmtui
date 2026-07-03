package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const userAgent = "llmtui (+https://github.com/patrikcze/llmtui)"

// Fetch downloads one page. Full content processing lands with the fetch
// pipeline; this establishes URL vetting and the guarded transport.
func (c *Client) Fetch(ctx context.Context, rawURL string) (Page, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Page{URL: rawURL}, fmt.Errorf("parse URL: %w", err)
	}
	if err := checkURL(u); err != nil {
		return Page{URL: rawURL}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{URL: rawURL}, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return Page{URL: rawURL}, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	return Page{URL: u.String(), Status: resp.StatusCode}, nil
}
