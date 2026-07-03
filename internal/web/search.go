package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// ErrRateLimited reports that DuckDuckGo is throttling us; the caller should
// tell the model to proceed without search rather than retry in a loop.
var ErrRateLimited = errors.New("search is being rate-limited — try again later or answer without it")

// Search queries DuckDuckGo's HTML endpoint (no API key) and returns up to
// max results.
func (c *Client) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	if max <= 0 {
		max = 5
	}
	form := url.Values{"q": {query}, "kl": {"wt-wt"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.searchURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search failed: status %d", resp.StatusCode)
	}
	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse results: %w", err)
	}
	return parseDDG(doc, max), nil
}

// parseDDG walks the result page: each hit has an <a class="result__a"> title
// link and an element with class "result__snippet". A hit is flushed when its
// snippet arrives, when the next title starts, or at the end of the walk.
func parseDDG(doc *html.Node, max int) []SearchResult {
	var results []SearchResult
	var current *SearchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= max && current == nil {
			return
		}
		if n.Type == html.ElementNode {
			switch {
			case hasClass(n, "result__a"):
				if current != nil {
					results = append(results, *current)
				}
				current = &SearchResult{Title: nodeText(n), URL: decodeDDGHref(attr(n, "href"))}
			case hasClass(n, "result__snippet") && current != nil:
				current.Snippet = nodeText(n)
				results = append(results, *current)
				current = nil
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if current != nil && len(results) < max {
		results = append(results, *current)
	}
	if len(results) > max {
		results = results[:max]
	}
	return results
}

func hasClass(n *html.Node, class string) bool {
	for _, f := range strings.Fields(attr(n, "class")) {
		if f == class {
			return true
		}
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

// decodeDDGHref resolves DuckDuckGo's redirect links (…/l/?uddg=<target>)
// to the real destination.
func decodeDDGHref(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return href
	}
	if strings.HasSuffix(u.Host, "duckduckgo.com") && strings.HasPrefix(u.Path, "/l/") {
		if target := u.Query().Get("uddg"); target != "" {
			return target
		}
	}
	return href
}
