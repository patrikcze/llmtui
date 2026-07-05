package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	name := NewSessionName(time.Date(2026, 7, 2, 16, 30, 5, 0, time.Local))

	s := Session{
		Provider: "lmstudio",
		Model:    "test-model",
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "be helpful"},
			{Role: provider.RoleUser, Content: "hi"},
			{Role: provider.RoleAssistant, Content: "hello!"},
		},
		Prompt:    10,
		Reply:     20,
		Estimated: true,
	}
	path, err := Save(dir, name, s)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if filepath.Base(path) != "session-20260702-163005.json" {
		t.Errorf("path = %s, want timestamped name", path)
	}

	got, err := Load(dir, name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Model != "test-model" || len(got.Messages) != 3 || got.Prompt != 10 || !got.Estimated {
		t.Errorf("loaded session = %+v", got)
	}
	if got.Messages[2].Content != "hello!" {
		t.Errorf("message content lost: %+v", got.Messages[2])
	}
}

func TestSaveOverwritesSameName(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, "s1", Session{Model: "a"}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if _, err := Save(dir, "s1", Session{Model: "b"}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := Load(dir, "s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Model != "b" {
		t.Errorf("Model = %q, want overwritten value b", got.Model)
	}
	metas, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 1 {
		t.Errorf("List = %d entries, want 1", len(metas))
	}
}

// TestSaveIsAtomic guards against a truncate-in-place write: Save must not
// leave a .tmp file behind, and a second save under the same name must fully
// replace the first save's content rather than leaving any of it mixed in.
func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, "s1", Session{Model: "a", Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "first save, long content that would show partial-write corruption"},
	}}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if _, err := Save(dir, "s1", Session{Model: "b", Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "second"},
	}}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "s1.json.tmp")); !os.IsNotExist(err) {
		t.Errorf("temp file left behind: err = %v", err)
	}
	got, err := Load(dir, "s1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Model != "b" || len(got.Messages) != 1 || got.Messages[0].Content != "second" {
		t.Errorf("loaded session = %+v, want fully replaced by the second save", got)
	}
}

func TestListNewestFirstAndSkipsForeignFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, "old", Session{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := Save(dir, "new", Session{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	// Foreign and malformed files are ignored.
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	metas, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(metas) != 2 {
		t.Fatalf("List = %d entries, want 2", len(metas))
	}
	if metas[0].Name != "new" {
		t.Errorf("first entry = %s, want newest", metas[0].Name)
	}
}

func TestLatestReturnsNewestSession(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, "old", Session{Model: "m", Prompt: 1}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := Save(dir, "new", Session{Model: "m", Prompt: 2}); err != nil {
		t.Fatal(err)
	}
	name, s, err := Latest(dir)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if name != "new" {
		t.Errorf("name = %q, want new", name)
	}
	if s.Prompt != 2 {
		t.Errorf("loaded session = %+v, want the newest save", s)
	}
}

func TestLatestSingleSession(t *testing.T) {
	dir := t.TempDir()
	if _, err := Save(dir, "only", Session{Model: "solo"}); err != nil {
		t.Fatal(err)
	}
	name, s, err := Latest(dir)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if name != "only" || s.Model != "solo" {
		t.Errorf("Latest = (%q, %+v), want only/solo", name, s)
	}
}

func TestLatestNoSessionsErrors(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Latest(dir); err == nil {
		t.Fatal("Latest should error when no sessions are saved")
	}
}

func TestListMissingDir(t *testing.T) {
	metas, err := List(filepath.Join(t.TempDir(), "nope"))
	if err != nil || metas != nil {
		t.Errorf("List on missing dir = (%v, %v), want (nil, nil)", metas, err)
	}
}

func TestUsageAppendReadAggregate(t *testing.T) {
	dir := t.TempDir()
	day1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 7, 2, 10, 0, 0, 0, time.Local)

	recs := []UsageRecord{
		{Time: day1, Provider: "mock", Model: "m", PromptTokens: 10, CompletionTokens: 20, DurationMS: 500},
		{Time: day1.Add(time.Hour), Provider: "mock", Model: "m", PromptTokens: 5, CompletionTokens: 5, DurationMS: 200},
		{Time: day2, Provider: "lmstudio", Model: "x", PromptTokens: 100, CompletionTokens: 200, DurationMS: 900},
	}
	for _, r := range recs {
		if err := AppendUsage(dir, r); err != nil {
			t.Fatalf("AppendUsage: %v", err)
		}
	}

	got, err := ReadUsage(dir)
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ReadUsage = %d records, want 3", len(got))
	}

	days := AggregateByDay(got)
	if len(days) != 2 {
		t.Fatalf("AggregateByDay = %d days, want 2", len(days))
	}
	if days[0].Day != "2026-07-01" || days[0].Requests != 2 || days[0].TotalTokens() != 40 {
		t.Errorf("day1 = %+v", days[0])
	}
	if days[1].TotalTokens() != 300 {
		t.Errorf("day2 = %+v", days[1])
	}
}

func TestReadUsageSkipsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	if err := AppendUsage(dir, UsageRecord{Time: time.Now(), PromptTokens: 1}); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "usage.jsonl"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("garbage line\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := AppendUsage(dir, UsageRecord{Time: time.Now(), PromptTokens: 2}); err != nil {
		t.Fatal(err)
	}

	got, err := ReadUsage(dir)
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("ReadUsage = %d records, want 2 (garbage skipped)", len(got))
	}
}

func TestReadUsageMissingFile(t *testing.T) {
	recs, err := ReadUsage(t.TempDir())
	if err != nil || recs != nil {
		t.Errorf("ReadUsage on empty dir = (%v, %v), want (nil, nil)", recs, err)
	}
}

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	got, err := ExpandHome("~/x/y")
	if err != nil {
		t.Fatalf("ExpandHome: %v", err)
	}
	if got != filepath.Join(home, "x", "y") {
		t.Errorf("ExpandHome = %q", got)
	}
	if got, _ := ExpandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("absolute path changed: %q", got)
	}
}
