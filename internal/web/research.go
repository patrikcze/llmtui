package web

import (
	"math"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Research helpers for the web_research tool: query normalization, ranked
// result fusion, and query-focused passage selection. They are pure
// functions over text and URLs; the network work (search, fetch) stays in
// Client so every request keeps the SSRF guard.
const (
	// ResearchMaxQueries caps the searches one research call may run.
	ResearchMaxQueries = 3
	// ResearchDefaultSources is how many pages a call reads by default.
	ResearchDefaultSources = 4
	// ResearchMaxSources caps the pages one call may read.
	ResearchMaxSources = 6
	// ResearchPassagesPerSource caps the passages kept from one page.
	ResearchPassagesPerSource = 5
	// ResearchCharsPerSource caps the characters kept from one page.
	ResearchCharsPerSource = 2000
	// ResearchPerSiteLimit caps how many pages one site contributes.
	ResearchPerSiteLimit = 2
)

// NormalizeResearchQueries returns up to ResearchMaxQueries distinct,
// non-empty queries (compared case-insensitively), or the fallback alone
// when none were given.
func NormalizeResearchQueries(queries []string, fallback string) []string {
	seen := make(map[string]bool, len(queries))
	out := make([]string, 0, ResearchMaxQueries)
	for _, query := range queries {
		query = strings.TrimSpace(query)
		key := strings.ToLower(query)
		if query == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, query)
		if len(out) == ResearchMaxQueries {
			break
		}
	}
	if len(out) == 0 {
		return []string{strings.TrimSpace(fallback)}
	}
	return out
}

// FuseResults merges ranked result lists with reciprocal-rank fusion: a page
// found by several queries, or near the top of one, ranks first. Duplicate
// URLs (www, trailing slash, tracking parameters) collapse into one, search
// engine links and non-HTTP links are dropped, and each site contributes at
// most perSiteLimit pages so the notes come from several sources. Ties keep
// first-seen order, so the result is deterministic.
func FuseResults(lists [][]SearchResult, perSiteLimit int) []SearchResult {
	if perSiteLimit <= 0 {
		perSiteLimit = ResearchPerSiteLimit
	}
	type entry struct {
		order  int
		score  float64
		result SearchResult
	}
	entries := make(map[string]*entry)
	var keys []string
	for _, list := range lists {
		for rank, result := range list {
			key, ok := ResultKey(result.URL)
			if !ok {
				continue
			}
			e := entries[key]
			if e == nil {
				e = &entry{order: len(keys), result: result}
				entries[key] = e
				keys = append(keys, key)
			}
			e.score += 1 / float64(60+rank)
		}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		left, right := entries[keys[i]], entries[keys[j]]
		if left.score == right.score {
			return left.order < right.order
		}
		return left.score > right.score
	})
	perSite := make(map[string]int)
	fused := make([]SearchResult, 0, len(keys))
	for _, key := range keys {
		site := key
		if i := strings.IndexAny(key, "/?"); i >= 0 {
			site = key[:i]
		}
		if perSite[site] >= perSiteLimit {
			continue
		}
		perSite[site]++
		fused = append(fused, entries[key].result)
	}
	return fused
}

// ResultKey is a comparison key for a result URL: lowercase host without
// "www.", path without trailing slashes, and the query without tracking
// parameters. ok is false for anything that is not a public web page link
// (non-HTTP schemes, DuckDuckGo ads and redirects).
func ResultKey(rawURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host == "duckduckgo.com" || strings.HasSuffix(host, ".duckduckgo.com") {
		return "", false
	}
	host = strings.TrimPrefix(host, "www.")
	query := u.Query()
	for name := range query {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" || lower == "ref" {
			query.Del(name)
		}
	}
	key := host + strings.TrimRight(u.EscapedPath(), "/")
	if encoded := query.Encode(); encoded != "" {
		key += "?" + encoded
	}
	return key, true
}

// KeyTerms returns the meaningful words of text, lowercased and
// de-duplicated in order: words shorter than three characters and common
// filler words are dropped, numbers (years, prices) are kept.
func KeyTerms(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	seen := make(map[string]bool, len(fields))
	var terms []string
	for _, field := range fields {
		if seen[field] || researchStopWords[field] {
			continue
		}
		if utf8.RuneCountInString(field) < 3 && !allDigits(field) {
			continue
		}
		seen[field] = true
		terms = append(terms, field)
	}
	return terms
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsNumber(r) {
			return false
		}
	}
	return s != ""
}

// RelevantPassages returns the passages of text most relevant to terms, in
// page order, within limit passages and budget characters. Scores are a
// small BM25-style sum: rarer terms count more, repeats count with
// diminishing returns, and long passages are slightly discounted. When no
// passage mentions any term, the page's opening passages are returned
// (at most two, within half the budget).
func RelevantPassages(text string, terms []string, limit, budget int) []string {
	if limit <= 0 {
		limit = ResearchPassagesPerSource
	}
	if budget <= 0 {
		budget = ResearchCharsPerSource
	}
	passages := SplitPassages(text, 80, 600)
	if len(passages) == 0 {
		return nil
	}
	lowered := make([]string, len(passages))
	lengths := make([]int, len(passages))
	for i, passage := range passages {
		lowered[i] = strings.ToLower(passage)
		lengths[i] = utf8.RuneCountInString(passage)
	}
	type scored struct {
		index int
		score float64
	}
	var candidates []scored
	if len(terms) > 0 {
		count := float64(len(passages))
		frequency := make(map[string]int, len(terms))
		for _, term := range terms {
			for _, passage := range lowered {
				if strings.Contains(passage, term) {
					frequency[term]++
				}
			}
		}
		for i, passage := range lowered {
			score := 0.0
			for _, term := range terms {
				occurrences := strings.Count(passage, term)
				if occurrences == 0 {
					continue
				}
				rarity := math.Log((count+1)/float64(frequency[term]+1)) + 1
				score += rarity * (1 + math.Log(float64(occurrences)))
			}
			if score > 0 {
				candidates = append(candidates, scored{i, score / math.Max(1, math.Sqrt(float64(lengths[i])/400))})
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })

	var chosen []int
	used := 0
	for _, candidate := range candidates {
		if len(chosen) >= limit {
			break
		}
		if used+lengths[candidate.index] > budget {
			continue
		}
		chosen = append(chosen, candidate.index)
		used += lengths[candidate.index]
	}
	if len(chosen) == 0 {
		for i := range passages {
			if len(chosen) >= 2 {
				break
			}
			if used+lengths[i] > budget/2 {
				continue
			}
			chosen = append(chosen, i)
			used += lengths[i]
		}
	}
	sort.Ints(chosen)
	out := make([]string, len(chosen))
	for i, index := range chosen {
		out[i] = passages[index]
	}
	return out
}

// SplitPassages splits page text into passages of roughly minLength to
// maxLength characters: short lines are joined, long ones are split at
// sentence ends, and leftovers shorter than 40 characters (menus, buttons,
// footers) are dropped.
func SplitPassages(text string, minLength, maxLength int) []string {
	var passages []string
	current := ""
	flush := func() {
		if current != "" {
			passages = append(passages, current)
		}
		current = ""
	}
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lineLength := utf8.RuneCountInString(line)
		currentLength := utf8.RuneCountInString(current)
		switch {
		case lineLength > maxLength:
			flush()
			passages = append(passages, sentenceChunks(line, maxLength)...)
		case current == "":
			current = line
		case currentLength < minLength && currentLength+1+lineLength <= maxLength:
			current += " " + line
		default:
			flush()
			current = line
		}
	}
	flush()
	kept := passages[:0]
	for _, passage := range passages {
		if utf8.RuneCountInString(passage) >= 40 {
			kept = append(kept, passage)
		}
	}
	return kept
}

// sentenceChunks splits one long line at ". " boundaries into chunks of at
// most maxLength characters, hard-splitting (on rune boundaries) any single
// sentence that is longer than that.
func sentenceChunks(line string, maxLength int) []string {
	var chunks []string
	current := ""
	for _, sentence := range strings.Split(line, ". ") {
		if sentence == "" {
			continue
		}
		piece := sentence
		if !strings.HasSuffix(piece, ".") {
			piece += "."
		}
		if current != "" && utf8.RuneCountInString(current)+utf8.RuneCountInString(piece)+1 > maxLength {
			chunks = append(chunks, current)
			current = ""
		}
		for utf8.RuneCountInString(piece) > maxLength {
			runes := []rune(piece)
			chunks = append(chunks, string(runes[:maxLength]))
			piece = string(runes[maxLength:])
		}
		if current == "" {
			current = piece
		} else {
			current += " " + piece
		}
	}
	if current != "" {
		chunks = append(chunks, current)
	}
	return chunks
}

var researchStopWords = func() map[string]bool {
	words := strings.Fields(`the and for are but not you your with this that these
		those from what which who whom whose when where why how
		can could should would will shall may might must does did
		has have had was were been being its it's into about
		there their they them then than also any all some more
		most other such only own same very just our out over
		please tell find know get give need want like use using`)
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[word] = true
	}
	return set
}()
