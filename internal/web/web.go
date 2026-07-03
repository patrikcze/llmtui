// Package web gives the assistant optional access to the public internet:
// DuckDuckGo search (no API key) and page fetching with readable-content
// extraction. Everything is guarded against reaching private networks.
package web

import (
	"net"
	"net/http"
	"time"
)

// SearchResult is one web search hit.
type SearchResult struct {
	Title, URL, Snippet string
}

// Page is one fetched document, already reduced to model-friendly text.
type Page struct {
	URL, Title, Content, ContentType string
	Bytes                            int // raw response bytes read
	Status                           int
	Truncated                        bool
}

// Client implements search and fetch over plain HTTP. The zero value is not
// usable; construct with NewClient.
type Client struct {
	http      *http.Client
	maxPageKB int
	searchURL string // test override; defaults to DuckDuckGo

	// allowPrivate disables the SSRF guard for same-package tests that
	// target httptest servers on loopback. Never set outside tests.
	allowPrivate bool
}

const defaultSearchURL = "https://html.duckduckgo.com/html/"

// NewClient builds a client whose requests time out after timeout and whose
// fetched pages are capped at maxPageKB for the model.
func NewClient(timeout time.Duration, maxPageKB int) *Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	if maxPageKB <= 0 {
		maxPageKB = 128
	}
	c := &Client{maxPageKB: maxPageKB, searchURL: defaultSearchURL}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext:       c.guardedDial(dialer),
		ForceAttemptHTTP2: true,
	}
	c.http = &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errTooManyRedirects
			}
			return checkURL(req.URL) // every hop must stay http(s)
		},
	}
	return c
}
