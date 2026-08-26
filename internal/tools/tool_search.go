package tools

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxToolSearchPayloadBytes = 2 * 1024
	MaxToolSearchQueryRunes   = 256
	DefaultToolSearchResults  = 5
	MaxToolSearchResults      = 8
	maxToolMatchDescription   = 256
)

type toolSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

// ToolSearchCandidate is metadata for one eligible but hidden capability.
type ToolSearchCandidate struct {
	Name        string
	Description string
	Source      string
}

// ToolSearchMatch is the bounded model-facing discovery result.
type ToolSearchMatch struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	score       int
}

func decodeToolSearchBody(call *Call) {
	if len(call.Body) > MaxToolSearchPayloadBytes {
		call.InputErr = fmt.Sprintf("tool_search arguments exceed the %d byte limit", MaxToolSearchPayloadBytes)
		return
	}
	var args toolSearchArgs
	if err := decodeOneJSONObject(call.Body, &args); err != nil {
		call.InputErr = "tool_search needs one JSON object in the tool block body: " + err.Error()
		return
	}
	call.SearchQuery, call.Max = args.Query, args.MaxResults
	if err := ValidateToolSearchCall(call); err != nil {
		call.InputErr = err.Error()
	}
}

// ValidateToolSearchCall normalizes and bounds one local discovery query.
func ValidateToolSearchCall(call *Call) error {
	if call == nil {
		return fmt.Errorf("tool_search call is missing")
	}
	call.SearchQuery = strings.TrimSpace(call.SearchQuery)
	if call.SearchQuery == "" {
		return fmt.Errorf("tool_search needs a non-empty query")
	}
	if utf8.RuneCountInString(call.SearchQuery) > MaxToolSearchQueryRunes {
		return fmt.Errorf("tool_search query exceeds %d characters", MaxToolSearchQueryRunes)
	}
	if call.Max == 0 {
		call.Max = DefaultToolSearchResults
	}
	if call.Max < 1 || call.Max > MaxToolSearchResults {
		return fmt.Errorf("tool_search max_results must be between 1 and %d", MaxToolSearchResults)
	}
	return nil
}

// SearchTools deterministically ranks eligible hidden tools without network,
// embeddings, or another model call.
func SearchTools(query string, maxResults int, candidates []ToolSearchCandidate) []ToolSearchMatch {
	query = strings.TrimSpace(strings.ToLower(query))
	queryTokens := toolSearchTokens(query)
	if query == "" || len(queryTokens) == 0 || maxResults <= 0 {
		return nil
	}
	if maxResults > MaxToolSearchResults {
		maxResults = MaxToolSearchResults
	}
	matches := make([]ToolSearchMatch, 0, len(candidates))
	for _, candidate := range candidates {
		score := scoreToolCandidate(query, queryTokens, candidate)
		if score <= 0 {
			continue
		}
		matches = append(matches, ToolSearchMatch{
			Name: candidate.Name, Description: truncateRunes(strings.TrimSpace(candidate.Description), maxToolMatchDescription),
			Source: candidate.Source, score: score,
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].Name < matches[j].Name
	})
	if len(matches) > maxResults {
		matches = matches[:maxResults]
	}
	return matches
}

func scoreToolCandidate(query string, queryTokens []string, candidate ToolSearchCandidate) int {
	name := strings.ToLower(candidate.Name)
	description := strings.ToLower(candidate.Description)
	source := strings.ToLower(candidate.Source)
	nameTokens := toolSearchTokens(name)
	descriptionTokens := toolSearchTokens(description)
	sourceTokens := toolSearchTokens(source)
	normalizedQuery := strings.Join(queryTokens, " ")
	normalizedName := strings.Join(nameTokens, " ")
	score := 0
	if query == name {
		score += 10_000
	}
	if strings.HasPrefix(name, query) || strings.HasPrefix(normalizedName, normalizedQuery) {
		score += 1_000
	}
	if strings.Contains(normalizedName, normalizedQuery) {
		score += 500
	}
	for _, token := range queryTokens {
		score += tokenScore(token, nameTokens, 120, 50)
		score += tokenScore(token, sourceTokens, 70, 25)
		score += tokenScore(token, descriptionTokens, 25, 8)
	}
	return score
}

func tokenScore(query string, fields []string, exact, prefix int) int {
	for _, field := range fields {
		if field == query {
			return exact
		}
		if strings.HasPrefix(field, query) || strings.HasPrefix(query, field) {
			return prefix
		}
	}
	return 0
}

func toolSearchTokens(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}
