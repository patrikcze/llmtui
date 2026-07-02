package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const usageFile = "usage.jsonl"

// UsageRecord is one completed exchange, appended to the usage log.
type UsageRecord struct {
	Time             time.Time `json:"time"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	DurationMS       int64     `json:"duration_ms"`
	Estimated        bool      `json:"estimated,omitempty"`
}

// AppendUsage appends one record to the JSONL usage log.
func AppendUsage(dir string, rec UsageRecord) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create history directory: %w", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode usage record: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, usageFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open usage log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write usage record: %w", err)
	}
	return nil
}

// ReadUsage returns all records from the usage log, oldest first.
// Malformed lines are skipped so one bad write never breaks stats.
func ReadUsage(dir string) ([]UsageRecord, error) {
	f, err := os.Open(filepath.Join(dir, usageFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open usage log: %w", err)
	}
	defer f.Close()

	var records []UsageRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var rec UsageRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	if err := scanner.Err(); err != nil {
		return records, fmt.Errorf("read usage log: %w", err)
	}
	return records, nil
}

// DayTotal aggregates usage for one calendar day.
type DayTotal struct {
	Day              string // YYYY-MM-DD, local time
	Requests         int
	PromptTokens     int
	CompletionTokens int
}

// TotalTokens returns prompt + completion for the day.
func (d DayTotal) TotalTokens() int { return d.PromptTokens + d.CompletionTokens }

// AggregateByDay groups records into per-day totals, oldest day first.
func AggregateByDay(records []UsageRecord) []DayTotal {
	byDay := map[string]*DayTotal{}
	for _, r := range records {
		day := r.Time.Local().Format("2006-01-02")
		t, ok := byDay[day]
		if !ok {
			t = &DayTotal{Day: day}
			byDay[day] = t
		}
		t.Requests++
		t.PromptTokens += r.PromptTokens
		t.CompletionTokens += r.CompletionTokens
	}
	out := make([]DayTotal, 0, len(byDay))
	for _, t := range byDay {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}
