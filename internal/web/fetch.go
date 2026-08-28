package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"

	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// rawReadCap bounds how much of a response body is read at all; maxPageKB
// then bounds what reaches the model.
const rawReadCap = 4 << 20

// userAgent presents as a mainstream desktop browser. A tool-identifying
// string ("llmtui/…") is refused or served a bot challenge by a large share
// of public sites (news, weather, anything behind Cloudflare/Akamai), which
// made web_fetch fail on exactly the pages users ask for. The request stays
// GET-only, rate-unfriendly behaviour is not added, and robots are not
// bypassed — this only stops trivial UA-string filtering.
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"

const acceptLanguage = "en-US,en;q=0.9"

// retryableStatuses are answered again once over HTTP/1.1: they are the codes
// bot-protection layers use for "prove you're a browser" holds rather than a
// genuine "this page does not exist".
var retryableStatuses = map[int]bool{
	http.StatusForbidden:          true, // 403
	http.StatusTooManyRequests:    true, // 429
	http.StatusServiceUnavailable: true, // 503
}

// Fetch downloads one page and reduces it to Markdown/plain text. On non-2xx
// statuses the page (with any text body) and an error are both returned so
// the model can see what the server said.
func (c *Client) Fetch(ctx context.Context, rawURL string) (Page, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Page{URL: rawURL}, fmt.Errorf("parse URL: %w", err)
	}
	if err := checkURL(u); err != nil {
		return Page{URL: rawURL}, err
	}
	resp, err := c.fetchResponse(ctx, u)
	if err != nil {
		return Page{URL: rawURL}, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, rawReadCap))
	if err != nil {
		return Page{URL: rawURL, Status: resp.StatusCode}, fmt.Errorf("read response: %w", err)
	}
	page := Page{URL: u.String(), Status: resp.StatusCode, Bytes: len(body)}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct == "" {
		ct, _, _ = mime.ParseMediaType(http.DetectContentType(body))
	}
	page.ContentType = ct

	switch {
	case ct == "text/html" || ct == "application/xhtml+xml":
		page.Title, page.Content = htmlToMarkdown(body, u)
	case strings.HasPrefix(ct, "text/") || ct == "application/json" ||
		strings.HasSuffix(ct, "+json") || ct == "application/xml" || strings.HasSuffix(ct, "+xml"):
		page.Content = string(body)
	default:
		return page, fmt.Errorf("unsupported content type %q — only HTML, text, and JSON/XML pages can be fetched", ct)
	}

	page.Content, page.Truncated = c.capContent(page.Content, len(body))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return page, fmt.Errorf("fetch failed: status %d", resp.StatusCode)
	}
	return page, nil
}

// fetchResponse issues the GET over the default (HTTP/2-capable) client, then
// retries once over an HTTP/1.1-only client when the first attempt fails at
// the transport layer or comes back with a retryable block status. Public
// bot-protection layers routinely reset HTTP/2 streams for non-browser
// clients (surfacing as "stream error: …"), and an HTTP/1.1 retry with a
// browser User-Agent clears a large fraction of those without weakening any
// guardrail — the SSRF-checked dialer and redirect policy are shared by both
// clients.
func (c *Client) fetchResponse(ctx context.Context, u *url.URL) (*http.Response, error) {
	resp, err := c.doFetch(ctx, c.http, u)
	if !c.shouldRetryHTTP1(ctx, resp, err) {
		return resp, err
	}
	if resp != nil {
		resp.Body.Close()
	}
	return c.doFetch(ctx, c.httpH1, u)
}

func (c *Client) doFetch(ctx context.Context, client *http.Client, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/*;q=0.9,application/json;q=0.8,*/*;q=0.1")
	req.Header.Set("Accept-Language", acceptLanguage)
	return client.Do(req)
}

// shouldRetryHTTP1 reports whether the HTTP/1.1 fallback is worth trying. It
// never retries once the caller's context is done (the deadline covers both
// attempts) or when the failure is a deliberate guardrail rejection.
func (c *Client) shouldRetryHTTP1(ctx context.Context, resp *http.Response, err error) bool {
	if c.httpH1 == nil || ctx.Err() != nil {
		return false
	}
	if err != nil {
		return !errors.Is(err, errBlockedAddress)
	}
	return retryableStatuses[resp.StatusCode]
}

// htmlToMarkdown extracts the readable article and converts it to Markdown,
// falling back to a plain-text strip when extraction fails.
func htmlToMarkdown(body []byte, u *url.URL) (title, content string) {
	article, err := readability.FromReader(bytes.NewReader(body), u)
	if err == nil && strings.TrimSpace(article.Content) != "" {
		if md, mdErr := htmltomarkdown.ConvertString(article.Content); mdErr == nil && strings.TrimSpace(md) != "" {
			return article.Title, md
		}
	}
	return article.Title, stripText(body)
}

// stripText walks the HTML and emits its visible text.
func stripText(body []byte) string {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return string(body)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template", "iframe":
				return
			}
		}
		if n.Type == html.TextNode {
			if t := strings.TrimSpace(n.Data); t != "" {
				b.WriteString(t + "\n")
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return b.String()
}

// capContent truncates model-facing content to maxPageKB.
func (c *Client) capContent(content string, rawBytes int) (string, bool) {
	limit := c.maxPageKB * 1024
	if len(content) <= limit {
		return content, false
	}
	prefix, _ := terminaltext.TruncateBytes(content, limit)
	return prefix + fmt.Sprintf("\n… truncated (%d KB of %d bytes shown)", c.maxPageKB, rawBytes), true
}
