package embedded

import "strings"

// StopScanner watches an assembled text stream for configured stop strings.
// It holds back the shortest suffix that could still grow into a match so a
// stop string split across two pieces is never emitted, then either
// truncates and reports a stop, or releases the held-back text unchanged
// once it is clear no match is coming. Matching is case-sensitive. An empty
// stop list makes the scanner a passthrough.
type StopScanner struct {
	stops []string
	held  string
}

// NewStopScanner creates a scanner for the given stop strings. Empty strings
// are ignored (they would trivially "match" everything).
func NewStopScanner(stops []string) *StopScanner {
	filtered := make([]string, 0, len(stops))
	for _, s := range stops {
		if s != "" {
			filtered = append(filtered, s)
		}
	}
	return &StopScanner{stops: filtered}
}

// Push feeds the next assembled text piece. It returns text safe to emit
// now and whether a stop string was found (in which case text is truncated
// immediately before the stop string and no further Push calls should
// occur).
func (s *StopScanner) Push(text string) (emit string, stopped bool) {
	if len(s.stops) == 0 {
		return text, false
	}
	buf := s.held + text
	s.held = ""

	if idx, _ := s.firstMatch(buf); idx >= 0 {
		return buf[:idx], true
	}

	// No full match. Find the longest suffix of buf that is a prefix of any
	// stop string (a "partial match") and hold it back; release the rest.
	holdLen := s.longestPendingSuffix(buf)
	emit = buf[:len(buf)-holdLen]
	s.held = buf[len(buf)-holdLen:]
	return emit, false
}

// Flush releases any text held back because it could still have grown into
// a stop-string match, called once generation ends without a stop firing.
func (s *StopScanner) Flush() string {
	out := s.held
	s.held = ""
	return out
}

// firstMatch returns the earliest index at which any stop string fully
// matches within buf.
func (s *StopScanner) firstMatch(buf string) (index int, matched string) {
	best := -1
	bestStop := ""
	for _, stop := range s.stops {
		if idx := strings.Index(buf, stop); idx >= 0 && (best == -1 || idx < best) {
			best = idx
			bestStop = stop
		}
	}
	return best, bestStop
}

// longestPendingSuffix returns the length of the longest suffix of buf that
// is a non-empty proper (or full) prefix of any stop string — i.e. text
// that must be held back because appending more text could complete a stop
// match.
func (s *StopScanner) longestPendingSuffix(buf string) int {
	maxLen := 0
	for _, stop := range s.stops {
		limit := len(stop) - 1 // a full match would have been caught above
		if limit > len(buf) {
			limit = len(buf)
		}
		for n := limit; n > 0; n-- {
			suffix := buf[len(buf)-n:]
			if strings.HasPrefix(stop, suffix) {
				if n > maxLen {
					maxLen = n
				}
				break
			}
		}
	}
	return maxLen
}
