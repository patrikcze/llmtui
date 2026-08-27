package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

func fixedClockCollector(t *testing.T, at time.Time, opts ...LocalContextOption) *defaultLocalContextCollector {
	t.Helper()
	all := append([]LocalContextOption{WithClock(func() time.Time { return at })}, opts...)
	return NewLocalContextCollector(t.TempDir(), all...).(*defaultLocalContextCollector)
}

func collectTime(t *testing.T, c *defaultLocalContextCollector) timeContext {
	t.Helper()
	data, err := c.Collect(context.Background(), LocalContextTime, DefaultLocalContextLimit)
	if err != nil {
		t.Fatalf("collect time: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("time result is not valid JSON: %s", data)
	}
	var got timeContext
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode time result: %v", err)
	}
	if got.Kind != LocalContextTime {
		t.Fatalf("kind = %q", got.Kind)
	}
	return got
}

func TestLocalContextTimeNativeAndFencedDecoding(t *testing.T) {
	native := CallsFromNative([]provider.ToolCall{{ID: "t1", Name: ToolLocalContext, Arguments: `{"kind":"time"}`}})[0]
	if native.InputErr != "" || native.ContextKind != LocalContextTime {
		t.Fatalf("native time call = %+v", native)
	}

	fenced := Parse("```tool local_context\n{\"kind\":\"time\"}\n```")
	if len(fenced) != 1 || fenced[0].InputErr != "" || fenced[0].ContextKind != LocalContextTime {
		t.Fatalf("fenced time call = %+v", fenced)
	}

	unknown := CallsFromNative([]provider.ToolCall{{Name: ToolLocalContext, Arguments: `{"kind":"clock"}`}})[0]
	if unknown.InputErr == "" {
		t.Fatalf("unknown kind was accepted: %+v", unknown)
	}
}

func TestLocalContextTimeDeterministicWithFixedClock(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// 2026-08-27 23:42:15 CEST (summer time, +02:00).
	at := time.Date(2026, time.August, 27, 23, 42, 15, 0, prague)
	c := fixedClockCollector(t, at)

	first := collectTime(t, c)
	second := collectTime(t, c)
	if first != second {
		t.Fatalf("fixed clock produced differing output:\n%+v\n%+v", first, second)
	}
	if first.Date != "2026-08-27" || first.Time != "23:42:15" {
		t.Fatalf("local date/time = %q %q", first.Date, first.Time)
	}
	if first.Weekday != "Thursday" {
		t.Fatalf("weekday = %q, want Thursday", first.Weekday)
	}
	if first.UTCOffset != "+02:00" {
		t.Fatalf("utc offset = %q, want +02:00", first.UTCOffset)
	}
	if first.UTCTime != "2026-08-27T21:42:15Z" {
		t.Fatalf("utc time = %q", first.UTCTime)
	}
	if first.UnixSeconds != at.Unix() {
		t.Fatalf("unix = %d, want %d", first.UnixSeconds, at.Unix())
	}
	if !strings.HasPrefix(first.CapturedAt, "2026-08-27T23:42:15+02:00") {
		t.Fatalf("captured_at = %q", first.CapturedAt)
	}
}

func TestLocalContextTimeRepeatedCallsObserveTheClock(t *testing.T) {
	calls := 0
	c := NewLocalContextCollector(t.TempDir(), WithClock(func() time.Time {
		calls++
		return time.Date(2026, time.January, 1, 0, 0, int(calls), 0, time.UTC)
	})).(*defaultLocalContextCollector)

	a := collectTime(t, c)
	b := collectTime(t, c)
	if a.UnixSeconds == b.UnixSeconds {
		t.Fatalf("clock was not re-read: %d == %d", a.UnixSeconds, b.UnixSeconds)
	}
	if calls < 2 {
		t.Fatalf("clock invoked %d times", calls)
	}
}

func TestLocalContextTimeOffsets(t *testing.T) {
	cases := []struct {
		name       string
		location   string
		at         time.Time
		wantOffset string
		wantDate   string
	}{
		{"utc", "UTC", time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), "+00:00", "2026-03-10"},
		{"positive", "Asia/Kolkata", time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC), "+05:30", "2026-03-10"},
		{"negative", "America/Los_Angeles", time.Date(2026, 1, 15, 2, 0, 0, 0, time.UTC), "-08:00", "2026-01-14"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.location)
			if err != nil {
				t.Skipf("tzdata unavailable: %v", err)
			}
			c := fixedClockCollector(t, tc.at.In(loc), WithTimezone(tc.location))
			got := collectTime(t, c)
			if got.UTCOffset != tc.wantOffset {
				t.Fatalf("offset = %q, want %q", got.UTCOffset, tc.wantOffset)
			}
			if got.Date != tc.wantDate {
				t.Fatalf("date = %q, want %q (date rollover across zones)", got.Date, tc.wantDate)
			}
			if got.TimezoneSource != "configured" || got.Timezone != tc.location {
				t.Fatalf("timezone source=%q name=%q", got.TimezoneSource, got.Timezone)
			}
		})
	}
}

func TestLocalContextTimeDaylightSavingTransition(t *testing.T) {
	prague, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// CET (+01:00) before the last-Sunday-of-March spring-forward.
	winter := fixedClockCollector(t, time.Date(2026, time.March, 29, 1, 30, 0, 0, prague), WithTimezone("Europe/Prague"))
	if got := collectTime(t, winter); got.UTCOffset != "+01:00" {
		t.Fatalf("pre-DST offset = %q, want +01:00", got.UTCOffset)
	}
	// CEST (+02:00) after the transition.
	summer := fixedClockCollector(t, time.Date(2026, time.March, 29, 3, 30, 0, 0, prague), WithTimezone("Europe/Prague"))
	if got := collectTime(t, summer); got.UTCOffset != "+02:00" {
		t.Fatalf("post-DST offset = %q, want +02:00", got.UTCOffset)
	}
}

func TestLocalContextTimeRollovers(t *testing.T) {
	cases := []struct {
		name          string
		at            time.Time
		wantDate      string
		wantWeekday   string
		wantLocalTime string
	}{
		{"midnight", time.Date(2026, 6, 1, 0, 0, 30, 0, time.UTC), "2026-06-01", "Monday", "00:00:30"},
		{"year", time.Date(2026, 1, 1, 0, 0, 5, 0, time.UTC), "2026-01-01", "Thursday", "00:00:05"},
		{"leap-day", time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC), "2028-02-29", "Tuesday", "12:00:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fixedClockCollector(t, tc.at, WithTimezone("UTC"))
			got := collectTime(t, c)
			if got.Date != tc.wantDate || got.Weekday != tc.wantWeekday || got.Time != tc.wantLocalTime {
				t.Fatalf("got date=%q weekday=%q time=%q", got.Date, got.Weekday, got.Time)
			}
		})
	}
}

func TestLocalContextTimeInvalidConfiguredZoneErrors(t *testing.T) {
	c := fixedClockCollector(t, time.Now(), WithTimezone("Not/AZone"))
	_, err := c.Collect(context.Background(), LocalContextTime, DefaultLocalContextLimit)
	if err == nil || !strings.Contains(err.Error(), "context.timezone") {
		t.Fatalf("invalid zone error = %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "utc") {
		t.Fatalf("error implies a silent UTC fallback: %v", err)
	}
}

func TestLocalContextTimeIsBoundedAndOmitsIdentity(t *testing.T) {
	c := fixedClockCollector(t, time.Date(2026, 8, 27, 23, 42, 15, 0, time.UTC), WithTimezone("UTC"))
	data, err := c.Collect(context.Background(), LocalContextTime, DefaultLocalContextLimit)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 512 {
		t.Fatalf("time result unexpectedly large: %d bytes", len(data))
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"hostname", "username", "environment", "os", "cpu_model", "user", "home"} {
		if _, ok := raw[forbidden]; ok {
			t.Fatalf("time result exposed %q: %s", forbidden, data)
		}
	}
}

func TestLocalContextTimeDoesNotRequireApproval(t *testing.T) {
	runner := NewRunner(t.TempDir(), 64)
	if runner.NeedsApproval(Call{Tool: ToolLocalContext, ContextKind: LocalContextTime}) {
		t.Fatal("kind=time unexpectedly requires approval")
	}
	if NeedsApproval(Call{Tool: ToolLocalContext, ContextKind: LocalContextTime}) {
		t.Fatal("kind=time unexpectedly requires approval (policy-free view)")
	}
	if !runner.NeedsApproval(Call{Tool: ToolLocalContext, ContextKind: LocalContextClipboard}) {
		t.Fatal("clipboard approval regressed")
	}
}

func TestLocalContextTimeSystemZoneIsHonest(t *testing.T) {
	// No configured override: the source must be one of the honest markers
	// and, when no IANA name is resolved, the field is omitted rather than
	// claiming "Local" is a zone name.
	c := fixedClockCollector(t, time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	got := collectTime(t, c)
	switch got.TimezoneSource {
	case "iana":
		if got.Timezone == "" || got.Timezone == "Local" {
			t.Fatalf("iana source but name = %q", got.Timezone)
		}
	case "system":
		if got.Timezone != "" {
			t.Fatalf("system source should omit the IANA name, got %q", got.Timezone)
		}
	default:
		t.Fatalf("unexpected timezone source %q", got.TimezoneSource)
	}
}
