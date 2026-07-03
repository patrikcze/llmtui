package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
)

// rawReadCap bounds how much of a response body is read at all; maxPageKB
// then bounds what reaches the model.
const rawReadCap = 4 << 20

const userAgent = "llmtui (+https://github.com/patrikcze/llmtui)"

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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{URL: rawURL}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/*;q=0.9,application/json;q=0.8,*/*;q=0.1")

	resp, err := c.http.Do(req)
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
	return content[:limit] + fmt.Sprintf("\n… truncated (%d KB of %d bytes shown)", c.maxPageKB, rawBytes), true
}
