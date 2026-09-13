package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	ctx, cancel := context.WithTimeout(ctx, c.http.Timeout)
	defer cancel()
	if max <= 0 {
		max = 5
	}
	form := url.Values{"q": {query}, "kl": {"wt-wt"}, "b": {""}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.searchURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", acceptLanguage)

	resp, err := c.http.Do(req)
	// Search POSTs are read-only and safe to replay. Do not retry HTTP
	// blocks/challenges: switching protocols does not remove a rate limit.
	if err != nil && c.shouldRetryHTTP1(ctx, resp, err) {
		if resp != nil {
			resp.Body.Close()
		}
		req = req.Clone(ctx)
		req.Body, err = req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("replay search: %w", err)
		}
		resp, err = c.httpH1.Do(req)
	}
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode == http.StatusAccepted {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search failed: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, rawReadCap+1))
	if err != nil {
		return nil, fmt.Errorf("read results: %w", err)
	}
	if len(body) > rawReadCap {
		return nil, errors.New("search response exceeds 4 MB")
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse results: %w", err)
	}
	challenge, noResults := ddgPageState(doc)
	if challenge {
		return nil, ErrRateLimited
	}
	results := parseDDG(doc, max)
	if len(results) == 0 && !noResults {
		return nil, errors.New("search returned an unrecognized page (possible bot challenge or changed result markup)")
	}
	return results, nil
}

// ddgPageState distinguishes an explicit empty result from a challenge or
// an unexpected page; neither should silently become "no results".
func ddgPageState(n *html.Node) (challenge, noResults bool) {
	if n.Type == html.ElementNode {
		challenge = attr(n, "id") == "challenge-form" || hasClass(n, "anomaly-modal")
		noResults = hasClass(n, "no-results") || hasClass(n, "no-results__message")
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		c, empty := ddgPageState(child)
		challenge = challenge || c
		noResults = noResults || empty
	}
	return challenge, noResults
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
