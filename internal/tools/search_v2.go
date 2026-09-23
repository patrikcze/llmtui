package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/patrikcze/llmtui/internal/entity"
)

type searchMatcher struct{ re *regexp.Regexp }

func newSearchMatcher(call Call) (searchMatcher, error) {
	pattern := call.Body
	if call.SearchLiteral {
		pattern = regexp.QuoteMeta(pattern)
	}
	if !searchCaseSensitive(call.SearchCaseSensitive) {
		pattern = "(?i:" + pattern + ")"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return searchMatcher{}, withCode(fmt.Errorf("grep pattern: %w", err), "invalid_pattern", RetryCorrectInput)
	}
	return searchMatcher{re: re}, nil
}

type searchScanStats struct {
	filesEligible int64
	filesScanned  int64
	sourceBytes   int64
	skipped       map[string]int64
	complete      bool
	textComplete  bool
}

func newSearchScanStats() searchScanStats {
	return searchScanStats{skipped: make(map[string]int64), complete: true, textComplete: true}
}

type searchLine struct {
	text         string
	start, end   int64
	completeText bool
}

func (r *Runner) grepFilesPage(ctx context.Context, call Call) (string, ResultMeta, []Capture, error) {
	meta := ResultMeta{Effect: EffectNone}
	if err := ValidateSearchCall(&call); err != nil {
		return "", meta, nil, withCode(err, "invalid_arguments", RetryCorrectInput)
	}
	limit, _ := defaultSearchLimit(call.SearchLimit)
	if call.SearchCursor != "" {
		cursor, err := r.takeSearchCursor(call.SearchCursor, "", limit, ToolGrep)
		if err != nil {
			return "", meta, nil, err
		}
		start := cursor.Offset
		end := min(start+limit, len(cursor.Rows))
		rows := cursor.Rows[start:end]
		meta.Outcome = OutcomeOK
		meta.Search = SearchCoverage{MatchesReturned: int64(len(rows)), MatchesCaptured: int64(len(cursor.Rows)), TextComplete: rowsTextComplete(rows)}
		meta.Coverage = Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true, RetainedBytes: int64(len(searchRowsOutput(rows)))}
		if end < len(cursor.Rows) {
			meta.Window = &Window{NextCursor: call.SearchCursor}
		}
		return searchRowsOutput(rows), meta, nil, nil
	}

	var rows []searchRow
	stats := newSearchScanStats()
	var err error
	if strings.TrimSpace(call.ResourceID) != "" {
		rows, stats, err = r.scanSearchResource(ctx, call, limit)
	} else {
		rows, stats, err = r.scanSearchWorkspace(ctx, call)
	}
	if err != nil {
		return "", meta, nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Path != rows[j].Path {
			return rows[i].Path < rows[j].Path
		}
		return rows[i].Line < rows[j].Line
	})
	if len(rows) > maxSearchRows {
		rows = rows[:maxSearchRows]
		stats.complete = false
		stats.skipped["matches"]++
	}
	body, bodyErr := canonicalSearchJSONL(rows)
	capturedRows := rows
	if bodyErr != nil {
		return "", meta, nil, fmt.Errorf("encode search result set: %w", bodyErr)
	}
	if n := bytes.Count(body, []byte{'\n'}); n < len(capturedRows) {
		capturedRows = capturedRows[:n]
		stats.complete = false
		stats.skipped["retention"]++
	}
	pageEnd := min(limit, len(capturedRows))
	page := capturedRows[:pageEnd]
	meta.Search = SearchCoverage{MatchesReturned: int64(len(page)), MatchesCaptured: int64(len(capturedRows)), TextComplete: rowsTextComplete(page), FilesEligible: stats.filesEligible, FilesScanned: stats.filesScanned, SourceBytes: stats.sourceBytes, Skipped: stats.skipped}
	if stats.complete {
		total := int64(len(capturedRows))
		meta.Search.MatchesTotal = &total
	}
	meta.Coverage = Coverage{SourceComplete: stats.complete, CaptureComplete: len(capturedRows) == len(rows), PreviewComplete: true, ObservedBytes: stats.sourceBytes, RetainedBytes: int64(len(searchRowsOutput(page))), Reasons: searchReasons(stats)}
	meta.Outcome = OutcomeOK
	if !stats.complete {
		meta.Outcome = OutcomePartial
	}
	if len(page) == 0 {
		output := fmt.Sprintf("no matches for %q", call.Body)
		if !stats.complete {
			output += " (partial search; incomplete source coverage)"
		}
		return output, meta, searchCaptures(call, capturedRows, body), nil
	}
	var token string
	if stats.complete && len(capturedRows) > pageEnd && r.searchCaptureEnabled {
		queryHash := searchQueryHash(call)
		token, err = r.putSearchCursor(searchCursor{Kind: ToolGrep, QueryHash: queryHash, Rows: capturedRows, Offset: pageEnd})
		if err != nil {
			meta.Coverage.Reasons = append(meta.Coverage.Reasons, "retention_unavailable")
		} else if token != "" {
			meta.Window = &Window{NextCursor: token}
		}
	}
	output := searchRowsOutput(page)
	if len(capturedRows) > pageEnd && token == "" {
		meta.Coverage.Reasons = append(meta.Coverage.Reasons, "retention_unavailable")
		output += "\n\n[pagination unavailable: retention_unavailable]"
	}
	if !stats.complete {
		output += fmt.Sprintf("\n\n[partial search: source coverage incomplete; captured=%d; returned=%d]", len(capturedRows), len(page))
	}
	if token != "" {
		output += fmt.Sprintf("\n\n[search cursor: %s; captured=%d; returned=%d]", token, len(capturedRows), len(page))
	}
	return output, meta, searchCaptures(call, capturedRows, body), nil
}

func searchCaptures(call Call, rows []searchRow, body []byte) []Capture {
	if len(body) == 0 || len(rows) == 0 {
		return nil
	}
	resource := entity.ResourceMetadata{ContentType: "application/x-ndjson", BodyDigest: digestBytes(body), Observation: entity.ObservationMetadata{Acquisition: "local", Freshness: "snapshot"}}
	if call.ResourceID != "" {
		if id, err := entity.ParseID(call.ResourceID); err == nil {
			resource.ParentID, resource.Relation = id, "search_of"
		}
	}
	return []Capture{{Kind: entity.KindSearchResult, Label: "grep result set", Trust: entity.TrustWorkspaceUntrusted, ContentType: "application/x-ndjson", Body: body, BodyDigest: digestBytes(body), Resource: resource}}
}

func rowsTextComplete(rows []searchRow) bool {
	for _, row := range rows {
		if !row.TextComplete {
			return false
		}
	}
	return true
}

func searchReasons(stats searchScanStats) []string {
	if len(stats.skipped) == 0 {
		return nil
	}
	out := make([]string, 0, len(stats.skipped))
	for key := range stats.skipped {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func (r *Runner) scanSearchWorkspace(ctx context.Context, call Call) ([]searchRow, searchScanStats, error) {
	stats := newSearchScanStats()
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return nil, stats, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { _ = root.Close() }()
	base, err := r.resolve(call.Path)
	if err != nil {
		return nil, stats, withCode(err, "safety_block", RetryCorrectInput)
	}
	baseRel, err := filepath.Rel(r.root, base)
	if err != nil {
		return nil, stats, withCode(err, "safety_block", RetryCorrectInput)
	}
	baseRel = filepath.ToSlash(baseRel)
	if baseRel == "." {
		baseRel = "."
	}
	info, err := root.Stat(baseRel)
	if err != nil {
		return nil, stats, withCode(fmt.Errorf("grep: %w", err), "not_found", RetryCorrectInput)
	}
	matcher, err := newSearchMatcher(call)
	if err != nil {
		return nil, stats, err
	}
	paths := make([]string, 0, 128)
	explicitPath := filepath.ToSlash(filepath.Clean(strings.TrimSpace(call.Path)))
	if !info.IsDir() {
		paths = append(paths, baseRel)
		if !IsSecretPath(baseRel) || baseRel == explicitPath {
			stats.filesEligible = 1
		}
	} else {
		walkPath := baseRel
		err = fs.WalkDir(root.FS(), walkPath, func(name string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				stats.skipped["unreadable"]++
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			workspaceRel := filepath.ToSlash(name)
			if entry.IsDir() {
				if workspaceRel != baseRel && isGitMetadataPath(workspaceRel) {
					return fs.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 || isGitMetadataPath(workspaceRel) {
				return nil
			}
			if call.Filter != "" {
				relToBase, _ := filepath.Rel(base, filepath.Join(r.root, filepath.FromSlash(workspaceRel)))
				matched, matchErr := matchSearchGlob(call.Filter, filepath.ToSlash(relToBase))
				if matchErr != nil {
					return matchErr
				}
				if !matched {
					return nil
				}
			}
			if !IsSecretPath(workspaceRel) || workspaceRel == explicitPath {
				paths = append(paths, workspaceRel)
				stats.filesEligible++
			}
			if len(paths) >= maxSearchFiles {
				stats.complete = false
				stats.skipped["files"]++
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, stats, fmt.Errorf("grep: %w", ctx.Err())
			}
			return nil, stats, fmt.Errorf("grep: %w", err)
		}
	}
	sort.Strings(paths)
	var rows []searchRow
	remaining := int64(maxSearchSourceBytes)
	for _, rel := range paths {
		if err := ctx.Err(); err != nil {
			return nil, stats, fmt.Errorf("grep: %w", err)
		}
		if remaining <= 0 {
			stats.complete = false
			stats.skipped["bytes"]++
			break
		}
		fileRows, used, complete, textComplete, reason, err := scanSearchFile(ctx, root, rel, matcher, call.SearchContext, remaining, int64(r.maxKB)*1024)
		if err != nil {
			return nil, stats, err
		}
		stats.filesScanned++
		stats.sourceBytes += used
		remaining -= used
		rows = append(rows, fileRows...)
		if !complete {
			stats.complete = false
			if reason == "" {
				reason = "long_line"
			}
		}
		if !textComplete {
			stats.textComplete = false
		}
		if reason != "" {
			stats.skipped[reason]++
		}
	}
	return rows, stats, nil
}

func scanSearchFile(ctx context.Context, root *os.Root, rel string, matcher searchMatcher, contextLines int, budget, configuredFileLimit int64) ([]searchRow, int64, bool, bool, string, error) {
	file, err := root.Open(rel)
	if err != nil {
		return nil, 0, false, true, "unreadable", nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, false, true, "unreadable", nil
	}
	fileLimit := int64(maxSearchFileBytes)
	if configuredFileLimit > 0 && configuredFileLimit < fileLimit {
		fileLimit = configuredFileLimit
	}
	if info.Size() > fileLimit {
		return nil, 0, false, true, "large", nil
	}
	reader := bufio.NewReaderSize(file, 32*1024)
	lines := make([]searchLine, 0, 256)
	var lineBuf []byte
	var lineStart, scanned int64
	lineComplete := true
	complete := true
	for {
		if err := ctx.Err(); err != nil {
			return nil, scanned, false, true, "cancelled", fmt.Errorf("grep: %w", err)
		}
		part, readErr := reader.ReadSlice('\n')
		if scanned+int64(len(part)) > budget {
			complete = false
			break
		}
		scanned += int64(len(part))
		if bytes.IndexByte(part, 0) >= 0 {
			return nil, scanned, true, true, "binary", nil
		}
		if len(lineBuf) < maxSearchLineBytes {
			keep := min(maxSearchLineBytes-len(lineBuf), len(part))
			lineBuf = append(lineBuf, part[:keep]...)
			if keep < len(part) {
				lineComplete = false
			}
		}
		if readErr == bufio.ErrBufferFull {
			lineComplete = false
			continue
		}
		if readErr == io.EOF && len(part) == 0 && len(lineBuf) == 0 {
			break
		}
		end := lineStart + scanned
		text := strings.TrimSuffix(strings.TrimSuffix(string(lineBuf), "\n"), "\r")
		if !utf8.ValidString(text) {
			text = strings.ToValidUTF8(text, "�")
			lineComplete = false
		}
		lines = append(lines, searchLine{text: text, start: lineStart, end: end, completeText: lineComplete})
		lineStart = end
		lineBuf = lineBuf[:0]
		lineComplete = true
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, scanned, false, true, "unreadable", nil
		}
	}
	rows := make([]searchRow, 0)
	textComplete := true
	for i, line := range lines {
		if !matcher.re.MatchString(line.text) {
			continue
		}
		row := searchRow{Path: rel, Line: int64(i + 1), StartByte: line.start, EndByte: line.end, Text: line.text, TextComplete: line.completeText}
		for j := max(0, i-contextLines); j < i; j++ {
			row.Before = append(row.Before, lines[j].text)
		}
		for j := i + 1; j <= min(len(lines)-1, i+contextLines); j++ {
			row.After = append(row.After, lines[j].text)
		}
		if !line.completeText {
			textComplete = false
			complete = false
		}
		rows = append(rows, row)
	}
	return rows, scanned, complete, textComplete, "", nil
}

func (r *Runner) scanSearchResource(ctx context.Context, call Call, _ int) ([]searchRow, searchScanStats, error) {
	stats := newSearchScanStats()
	if r.Resources == nil {
		return nil, stats, withCode(fmt.Errorf("resource retention is not enabled"), "resource_unavailable", RetryReread)
	}
	id, err := entity.ParseID(call.ResourceID)
	if err != nil {
		return nil, stats, withCode(fmt.Errorf("invalid resource_id %q: %w", call.ResourceID, err), "invalid_arguments", RetryCorrectInput)
	}
	view, lease, err := r.Resources.OpenBody(ctx, id)
	if err != nil {
		code := "resource_unavailable"
		if errors.Is(err, entity.ErrNotAResourceBody) {
			code = "wrong_resource_kind"
		}
		if strings.Contains(strings.ToLower(err.Error()), "lifetime has ended") {
			code = "resource_expired"
		}
		return nil, stats, withCode(err, code, RetryReread)
	}
	defer func() { _ = lease.Close() }()
	matcher, err := newSearchMatcher(call)
	if err != nil {
		return nil, stats, err
	}
	if lease.Size() > maxSearchSourceBytes {
		stats.complete = false
		stats.skipped["bytes"]++
	}
	readSize := min(lease.Size(), maxSearchSourceBytes)
	data := make([]byte, readSize)
	if readSize > 0 {
		if _, err := lease.ReadAt(data, 0); err != nil && err != io.EOF {
			return nil, stats, fmt.Errorf("read retained search body: %w", err)
		}
	}
	stats.filesEligible, stats.filesScanned, stats.sourceBytes = 1, 1, int64(len(data))
	rows := scanSearchBytes(string(data), call.ResourceID, matcher, call.SearchContext)
	for _, raw := range strings.SplitAfter(string(data), "\n") {
		if len([]byte(strings.TrimRight(raw, "\r\n"))) > maxSearchLineBytes || !utf8.ValidString(raw) {
			stats.complete = false
			stats.skipped["long_line"]++
			break
		}
	}
	for i := range rows {
		rows[i].ResourceID = call.ResourceID
		rows[i].Path = ""
	}
	stats.textComplete = rowsTextComplete(rows)
	if view.Resource.FileVersion != nil && !view.Resource.FileVersion.Complete {
		stats.complete = false
	}
	return rows, stats, nil
}

func scanSearchBytes(data, resourceID string, matcher searchMatcher, contextLines int) []searchRow {
	lines := strings.SplitAfter(data, "\n")
	rows := make([]searchRow, 0)
	var offset int64
	for i, raw := range lines {
		if raw == "" {
			continue
		}
		text := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
		complete := len([]byte(text)) <= maxSearchLineBytes && utf8.ValidString(text)
		if len([]byte(text)) > maxSearchLineBytes {
			text = string([]byte(text)[:maxSearchLineBytes])
		}
		text = strings.ToValidUTF8(text, "�")
		if matcher.re.MatchString(text) {
			row := searchRow{ResourceID: resourceID, Line: int64(i + 1), StartByte: offset, EndByte: offset + int64(len(raw)), Text: text, TextComplete: complete}
			for j := max(0, i-contextLines); j < i; j++ {
				row.Before = append(row.Before, strings.TrimRight(lines[j], "\r\n"))
			}
			for j := i + 1; j <= min(len(lines)-1, i+contextLines); j++ {
				row.After = append(row.After, strings.TrimRight(lines[j], "\r\n"))
			}
			rows = append(rows, row)
		}
		offset += int64(len(raw))
	}
	return rows
}

func (r *Runner) globFilesPage(ctx context.Context, call Call) (string, ResultMeta, []Capture, error) {
	meta := ResultMeta{Effect: EffectNone}
	limit, err := listingLimit(call.SearchLimit)
	if err != nil {
		return "", meta, nil, withCode(err, "invalid_arguments", RetryCorrectInput)
	}
	if call.SearchCursor != "" {
		cursor, err := r.takeSearchCursor(call.SearchCursor, "", limit, ToolGlob)
		if err != nil {
			return "", meta, nil, err
		}
		start, end := cursor.Offset, min(cursor.Offset+limit, len(cursor.Entries))
		meta.Outcome = OutcomeOK
		meta.Coverage = Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true, TotalLines: int64Ptr(int64(len(cursor.Entries)))}
		if end < len(cursor.Entries) {
			meta.Window = &Window{NextCursor: call.SearchCursor}
		}
		displayToken := ""
		if end < len(cursor.Entries) {
			displayToken = call.SearchCursor
		}
		return formatListingPage(cursor.Entries[start:end], start, end, len(cursor.Entries), displayToken, &meta), meta, nil, nil
	}
	pattern, err := normalizeGlobPattern(call.Body)
	if err != nil {
		return "", meta, nil, withCode(err, "invalid_pattern", RetryCorrectInput)
	}
	entries, complete, err := r.collectGlob(ctx, call.Path, pattern)
	if err != nil {
		return "", meta, nil, err
	}
	pageEnd := min(limit, len(entries))
	meta.Outcome = OutcomeOK
	meta.Coverage = Coverage{SourceComplete: complete, CaptureComplete: complete, PreviewComplete: true, TotalLines: int64Ptr(int64(len(entries)))}
	if !complete {
		meta.Outcome = OutcomePartial
		meta.Coverage.Reasons = []string{"files"}
	}
	var token string
	if complete && pageEnd < len(entries) && r.searchCaptureEnabled {
		token, err = r.putSearchCursor(searchCursor{Kind: ToolGlob, QueryHash: listingHash(call), Entries: entries, Offset: pageEnd})
		if err != nil {
			meta.Coverage.Reasons = append(meta.Coverage.Reasons, "retention_unavailable")
		}
	}
	out := formatListingPage(entries[:pageEnd], 0, pageEnd, len(entries), token, &meta)
	if len(entries) > pageEnd && token == "" {
		meta.Coverage.Reasons = append(meta.Coverage.Reasons, "retention_unavailable")
		out += "\n\n[pagination unavailable: retention_unavailable]"
	}
	if token != "" {
		meta.Window = &Window{NextCursor: token}
	}
	return out, meta, listingCapture(entries, token), nil
}

func (r *Runner) listDirPage(ctx context.Context, rel string, requestedLimit int, token string) (string, ResultMeta, []Capture, error) {
	meta := ResultMeta{Effect: EffectNone}
	limit, err := listingLimit(requestedLimit)
	if err != nil {
		return "", meta, nil, withCode(err, "invalid_arguments", RetryCorrectInput)
	}
	if strings.TrimSpace(token) != "" {
		cursor, err := r.takeSearchCursor(token, "", limit, ToolListDir)
		if err != nil {
			return "", meta, nil, err
		}
		start, end := cursor.Offset, min(cursor.Offset+limit, len(cursor.Entries))
		meta.Outcome = OutcomeOK
		meta.Coverage = Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true, TotalLines: int64Ptr(int64(len(cursor.Entries)))}
		displayToken := ""
		if end < len(cursor.Entries) {
			displayToken = token
		}
		out := formatListingPage(cursor.Entries[start:end], start, end, len(cursor.Entries), displayToken, &meta)
		if end >= len(cursor.Entries) {
			meta.Window = nil
		} else {
			meta.Window = &Window{NextCursor: token}
		}
		return out, meta, nil, nil
	}

	root, err := os.OpenRoot(r.root)
	if err != nil {
		return "", meta, nil, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { _ = root.Close() }()
	base, err := r.resolve(rel)
	if err != nil {
		return "", meta, nil, withCode(err, "safety_block", RetryCorrectInput)
	}
	baseRel, err := filepath.Rel(r.root, base)
	if err != nil {
		return "", meta, nil, withCode(err, "safety_block", RetryCorrectInput)
	}
	baseRel = filepath.ToSlash(baseRel)
	if baseRel == "." {
		baseRel = "."
	}
	dir, err := root.Open(baseRel)
	if err != nil {
		return "", meta, nil, withCode(fmt.Errorf("list directory: %w", err), "not_found", RetryCorrectInput)
	}
	defer dir.Close()
	reader := dir
	entries := make([]string, 0, 128)
	complete := true
	for {
		if err := ctx.Err(); err != nil {
			return "", meta, nil, fmt.Errorf("list directory: %w", err)
		}
		batch, readErr := reader.ReadDir(256)
		for _, entry := range batch {
			if len(entries) >= maxSearchRows {
				complete = false
				break
			}
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			entries = append(entries, name)
		}
		if !complete || readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", meta, nil, withCode(fmt.Errorf("list directory: %w", readErr), "not_found", RetryCorrectInput)
		}
	}
	sort.Strings(entries)
	meta.Outcome = OutcomeOK
	meta.Coverage = Coverage{SourceComplete: complete, CaptureComplete: complete, PreviewComplete: true, TotalLines: int64Ptr(int64(len(entries)))}
	if !complete {
		meta.Outcome = OutcomePartial
		meta.Coverage.Reasons = []string{"entries"}
	}
	pageEnd := min(limit, len(entries))
	var next string
	if complete && pageEnd < len(entries) && r.searchCaptureEnabled {
		next, err = r.putSearchCursor(searchCursor{Kind: ToolListDir, Entries: entries, Offset: pageEnd})
		if err != nil {
			meta.Coverage.Reasons = append(meta.Coverage.Reasons, "retention_unavailable")
		}
	}
	out := formatListingPage(entries[:pageEnd], 0, pageEnd, len(entries), next, &meta)
	if len(entries) > pageEnd && next == "" {
		meta.Coverage.Reasons = append(meta.Coverage.Reasons, "retention_unavailable")
		out += "\n\n[pagination unavailable: retention_unavailable]"
	}
	if next != "" {
		meta.Window = &Window{NextCursor: next}
	}
	return out, meta, listingCapture(entries, next), nil
}

func listingLimit(limit int) (int, error) {
	if limit == 0 {
		return maxSearchPageLimit, nil
	}
	return defaultSearchLimit(limit)
}

func listingHash(call Call) string {
	return searchQueryHash(Call{Tool: call.Tool, Body: call.Body, Path: call.Path})
}

func formatListingPage(entries []string, start, end, total int, token string, meta *ResultMeta) string {
	if len(entries) == 0 {
		if total == 0 {
			return "(empty directory)"
		}
		return "no entries"
	}
	out := strings.Join(entries, "\n")
	if token != "" {
		out += fmt.Sprintf("\n\n[%s entries %d-%d of %d; cursor: %s]", "listing", start+1, end, total, token)
	}
	meta.Coverage.ObservedBytes, meta.Coverage.RetainedBytes = int64(len(out)), int64(len(out))
	return out
}

func listingCapture(entries []string, token string) []Capture {
	if len(entries) == 0 || token == "" {
		return nil
	}
	body, _ := json.Marshal(entries)
	return []Capture{{Kind: entity.KindSearchResult, Label: "listing result set", Trust: entity.TrustWorkspaceUntrusted, ContentType: "application/json", Body: body, BodyDigest: digestBytes(body)}}
}

func (r *Runner) collectGlob(ctx context.Context, rel, pattern string) ([]string, bool, error) {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return nil, false, fmt.Errorf("open workspace root: %w", err)
	}
	defer func() { _ = root.Close() }()
	base, err := r.resolve(rel)
	if err != nil {
		return nil, false, withCode(err, "safety_block", RetryCorrectInput)
	}
	baseRel, _ := filepath.Rel(r.root, base)
	baseRel = filepath.ToSlash(baseRel)
	if baseRel == "." {
		baseRel = "."
	}
	if _, err := root.Stat(baseRel); err != nil {
		return nil, false, withCode(fmt.Errorf("glob: %w", err), "not_found", RetryCorrectInput)
	}
	entries := make([]string, 0, 128)
	complete := true
	err = fs.WalkDir(root.FS(), baseRel, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			complete = false
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if name != baseRel && isGitMetadataPath(filepath.ToSlash(name)) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || isGitMetadataPath(filepath.ToSlash(name)) {
			return nil
		}
		relToBase, _ := filepath.Rel(filepath.FromSlash(baseRel), filepath.FromSlash(name))
		matched, matchErr := matchSearchGlob(pattern, filepath.ToSlash(relToBase))
		if matchErr != nil {
			return matchErr
		}
		if matched {
			entries = append(entries, filepath.ToSlash(name))
			if len(entries) >= maxSearchRows {
				complete = false
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, fmt.Errorf("glob: %w", ctx.Err())
		}
		return nil, false, fmt.Errorf("glob: %w", err)
	}
	sort.Strings(entries)
	return entries, complete, nil
}
