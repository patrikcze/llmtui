package tools

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const maxSearchArgsBytes = 8 * 1024

type searchWireArgs struct {
	Pattern       string `json:"pattern"`
	Path          string `json:"path"`
	ResourceID    string `json:"resource_id"`
	Glob          string `json:"glob"`
	Literal       bool   `json:"literal"`
	CaseSensitive *bool  `json:"case_sensitive"`
	Context       int    `json:"context"`
	Limit         int    `json:"limit"`
	Cursor        string `json:"cursor"`
}

func decodeSearchBody(call *Call) {
	body := strings.TrimSpace(call.Body)
	if !strings.HasPrefix(body, "{") || len(body) > maxSearchArgsBytes {
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		return // legacy regex body, including patterns beginning with '{'
	}
	known := map[string]bool{"pattern": true, "path": true, "resource_id": true, "glob": true, "literal": true, "case_sensitive": true, "context": true, "limit": true, "cursor": true}
	for key := range fields {
		if !known[key] {
			return // preserve a legacy JSON-shaped regex body
		}
	}
	var args searchWireArgs
	if err := json.Unmarshal([]byte(body), &args); err != nil {
		call.InputErr = fmt.Sprintf("grep search arguments are not valid JSON: %v", err)
		return
	}
	if args.Path != "" {
		if call.Path != "" && filepathCleanSlash(call.Path) != filepathCleanSlash(args.Path) {
			call.InputErr = "grep path is specified both in the fence and JSON body"
			return
		}
		call.Path = strings.TrimSpace(args.Path)
	}
	call.Body, call.Filter, call.ResourceID = args.Pattern, strings.TrimSpace(args.Glob), strings.TrimSpace(args.ResourceID)
	call.SearchLiteral, call.SearchCaseSensitive = args.Literal, args.CaseSensitive
	call.SearchContext, call.SearchLimit, call.SearchCursor = args.Context, args.Limit, strings.TrimSpace(args.Cursor)
	if err := ValidateSearchCall(call); err != nil {
		call.InputErr = err.Error()
	}
}

func decodeListingBody(call *Call) {
	body := strings.TrimSpace(call.Body)
	if !strings.HasPrefix(body, "{") || len(body) > maxSearchArgsBytes {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return
	}
	known := map[string]bool{"pattern": true, "path": true, "limit": true, "cursor": true}
	for key := range raw {
		if !known[key] {
			return
		}
	}
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Limit   int    `json:"limit"`
		Cursor  string `json:"cursor"`
	}
	if err := json.Unmarshal([]byte(body), &args); err != nil {
		call.InputErr = err.Error()
		return
	}
	if args.Path != "" {
		if call.Path != "" && filepathCleanSlash(call.Path) != filepathCleanSlash(args.Path) {
			call.InputErr = "listing path is specified both in the fence and JSON body"
			return
		}
		call.Path = strings.TrimSpace(args.Path)
	}
	if call.Tool == ToolGlob {
		call.Body = args.Pattern
	}
	call.SearchLimit, call.SearchCursor = args.Limit, strings.TrimSpace(args.Cursor)
	if limit, err := defaultSearchLimit(call.SearchLimit); err != nil {
		call.InputErr = err.Error()
	} else {
		call.SearchLimit = limit
	}
}

func filepathCleanSlash(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
}

const (
	maxSearchPageLimit       = 200
	defaultSearchPageLimit   = 100
	maxSearchContext         = 5
	maxSearchCursorCount     = 64
	searchCursorIdleLifetime = 10 * time.Minute
	maxSearchRows            = 10_000
	maxSearchFiles           = 10_000
	maxSearchResultBodyBytes = 4 << 20
	maxSearchFileBytes       = 8 << 20
	maxSearchSourceBytes     = 64 << 20
	maxSearchLineBytes       = 16 << 10
)

type searchRow struct {
	Path         string   `json:"path,omitempty"`
	ResourceID   string   `json:"resource_id,omitempty"`
	Line         int64    `json:"line"`
	StartByte    int64    `json:"start_byte"`
	EndByte      int64    `json:"end_byte"`
	Text         string   `json:"text"`
	Before       []string `json:"before,omitempty"`
	After        []string `json:"after,omitempty"`
	TextComplete bool     `json:"text_complete"`
}

type searchCursor struct {
	Kind      string
	QueryHash string
	Rows      []searchRow
	Entries   []string
	Offset    int
	ExpiresAt time.Time
	LastUsed  time.Time
}

func defaultSearchLimit(limit int) (int, error) {
	if limit == 0 {
		return defaultSearchPageLimit, nil
	}
	if limit < 1 || limit > maxSearchPageLimit {
		return 0, fmt.Errorf("search limit must be between 1 and %d", maxSearchPageLimit)
	}
	return limit, nil
}

func searchCaseSensitive(v *bool) bool {
	return v == nil || *v
}

func searchQueryHash(call Call) string {
	caseSensitive := searchCaseSensitive(call.SearchCaseSensitive)
	payload := struct {
		Tool          string
		Pattern       string
		Path          string
		Filter        string
		ResourceID    string
		Literal       bool
		CaseSensitive bool
		Context       int
	}{
		Tool: call.Tool, Pattern: call.Body, Path: call.Path, Filter: call.Filter,
		ResourceID: call.ResourceID, Literal: call.SearchLiteral,
		CaseSensitive: caseSensitive, Context: call.SearchContext,
	}
	b, _ := json.Marshal(payload)
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// ValidateSearchCall is shared by native and fenced decoders as well as
// direct Runner callers. A cursor is immutable: only that token and an
// optional smaller page limit may accompany a continuation.
func ValidateSearchCall(call *Call) error {
	if call == nil {
		return fmt.Errorf("search call is missing")
	}
	if call.Tool != ToolGrep {
		return fmt.Errorf("search arguments require grep")
	}
	if call.SearchContext < 0 || call.SearchContext > maxSearchContext {
		return fmt.Errorf("search context must be between 0 and %d", maxSearchContext)
	}
	if _, err := defaultSearchLimit(call.SearchLimit); err != nil {
		return err
	}
	if strings.TrimSpace(call.SearchCursor) != "" {
		if strings.TrimSpace(call.Body) != "" || strings.TrimSpace(call.Path) != "" ||
			strings.TrimSpace(call.Filter) != "" || strings.TrimSpace(call.ResourceID) != "" ||
			call.SearchLiteral || call.SearchCaseSensitive != nil || call.SearchContext != 0 {
			return fmt.Errorf("search cursor continuation accepts only cursor and optional limit")
		}
		return nil
	}
	if strings.TrimSpace(call.Body) == "" {
		return fmt.Errorf("grep needs a pattern")
	}
	if strings.TrimSpace(call.Path) != "" && strings.TrimSpace(call.ResourceID) != "" {
		return fmt.Errorf("grep accepts exactly one of path or resource_id, not both")
	}
	if strings.TrimSpace(call.Filter) != "" && strings.TrimSpace(call.ResourceID) != "" {
		return fmt.Errorf("grep glob filter is only valid for workspace paths")
	}
	return nil
}

func newSearchToken() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create search cursor: %w", err)
	}
	return "cur_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func (r *Runner) putSearchCursor(cursor searchCursor) (string, error) {
	if !r.searchCaptureEnabled {
		return "", nil
	}
	token, err := newSearchToken()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	cursor.ExpiresAt = now.Add(searchCursorIdleLifetime)
	cursor.LastUsed = now
	r.searchMu.Lock()
	defer r.searchMu.Unlock()
	if r.searchCursors == nil {
		r.searchCursors = make(map[string]searchCursor)
	}
	for len(r.searchCursors) >= maxSearchCursorCount {
		var oldest string
		var oldestAt time.Time
		for key, value := range r.searchCursors {
			if oldest == "" || value.LastUsed.Before(oldestAt) {
				oldest, oldestAt = key, value.LastUsed
			}
		}
		delete(r.searchCursors, oldest)
	}
	r.searchCursors[token] = cursor
	return token, nil
}

func (r *Runner) takeSearchCursor(token, queryHash string, limit int, kind string) (searchCursor, error) {
	if strings.TrimSpace(token) == "" {
		return searchCursor{}, fmt.Errorf("search cursor is empty")
	}
	now := time.Now().UTC()
	r.searchMu.Lock()
	defer r.searchMu.Unlock()
	cursor, ok := r.searchCursors[token]
	if !ok || !cursor.ExpiresAt.After(now) {
		delete(r.searchCursors, token)
		return searchCursor{}, withCode(fmt.Errorf("search cursor has expired or is unavailable"), "cursor_expired", RetryReread)
	}
	if kind != "" && cursor.Kind != kind {
		return searchCursor{}, withCode(fmt.Errorf("search cursor kind does not match this operation"), "invalid_arguments", RetryCorrectInput)
	}
	if queryHash != "" && cursor.QueryHash != queryHash {
		return searchCursor{}, withCode(fmt.Errorf("search cursor does not match this search"), "invalid_arguments", RetryCorrectInput)
	}
	cursor.LastUsed = now
	cursor.ExpiresAt = now.Add(searchCursorIdleLifetime)
	start := cursor.Offset
	cursor.Offset += limit
	r.searchCursors[token] = cursor
	cursor.Offset = start
	return cursor, nil
}

func canonicalSearchJSONL(rows []searchRow) ([]byte, error) {
	var out []byte
	for _, row := range rows {
		b, err := json.Marshal(row)
		if err != nil {
			return nil, err
		}
		if len(out)+len(b)+1 > maxSearchResultBodyBytes {
			break
		}
		out = append(out, b...)
		out = append(out, '\n')
	}
	return out, nil
}

func searchRowsOutput(rows []searchRow) string {
	var b strings.Builder
	for _, row := range rows {
		path := row.Path
		if path == "" {
			path = "resource:" + row.ResourceID
		}
		fmt.Fprintf(&b, "%s:%d:%s", path, row.Line, row.Text)
		for _, before := range row.Before {
			fmt.Fprintf(&b, "\n  -%s", before)
		}
		for _, after := range row.After {
			fmt.Fprintf(&b, "\n  +%s", after)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
