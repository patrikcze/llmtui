package personalapps

import (
	"testing"
	"time"
)

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadZone(name)
	if err != nil {
		t.Fatalf("LoadZone(%q): %v", name, err)
	}
	return loc
}

func at(t *testing.T, layout, value string, loc *time.Location) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation(layout, value, loc)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestIntervalIsHalfOpen(t *testing.T) {
	base := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	a := Interval{Start: base, End: base.Add(time.Hour)}
	b := Interval{Start: base.Add(time.Hour), End: base.Add(2 * time.Hour)}

	if a.Overlaps(b) || b.Overlaps(a) {
		t.Fatal("touching intervals were treated as overlapping")
	}
	if !a.Overlaps(Interval{Start: base.Add(30 * time.Minute), End: base.Add(90 * time.Minute)}) {
		t.Fatal("genuinely overlapping intervals were treated as disjoint")
	}
	if a.Contains(a.End) {
		t.Fatal("a half-open interval contains its end")
	}
	if !a.Contains(a.Start) {
		t.Fatal("a half-open interval excludes its start")
	}
	if (Interval{Start: base, End: base}).Valid() {
		t.Fatal("an empty interval reported itself valid")
	}
}

func TestValidateOffsetAgreement(t *testing.T) {
	prague := mustZone(t, "Europe/Prague")
	summer, err := time.Parse(time.RFC3339, "2026-09-06T08:00:00+02:00")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOffsetAgreement("start", summer, prague); err != nil {
		t.Fatalf("matching offset rejected: %v", err)
	}

	utcForm, err := time.Parse(time.RFC3339, "2026-09-06T06:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateOffsetAgreement("start", utcForm, prague)
	if CodeOf(err) != CodeInvalidTimezone {
		t.Fatalf("code = %q, want %q; a mismatched offset must not be silently reinterpreted", CodeOf(err), CodeInvalidTimezone)
	}

	// Winter is +01:00, so the summer offset is wrong in January.
	winter, err := time.Parse(time.RFC3339, "2026-01-06T08:00:00+02:00")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOffsetAgreement("start", winter, prague); err == nil {
		t.Fatal("a summer offset was accepted for a winter instant")
	}
}

func TestResolveWallClockAcrossDST(t *testing.T) {
	prague := mustZone(t, "Europe/Prague")

	// Ordinary time resolves to exactly one instant.
	got, err := ResolveWallClock(2026, time.September, 7, 9, 30, prague)
	if err != nil {
		t.Fatalf("ResolveWallClock: %v", err)
	}
	if got.Hour() != 9 || got.Minute() != 30 {
		t.Fatalf("ResolveWallClock = %v, want 09:30 local", got)
	}

	// Spring forward 2026-03-29: 02:00-03:00 does not exist in Prague.
	if _, err := ResolveWallClock(2026, time.March, 29, 2, 30, prague); CodeOf(err) != CodeAmbiguousTime {
		t.Fatalf("nonexistent local time: code = %q, want %q (err %v)", CodeOf(err), CodeAmbiguousTime, err)
	}
	// The hour either side of the gap is fine.
	for _, hour := range []int{1, 3} {
		if _, err := ResolveWallClock(2026, time.March, 29, hour, 30, prague); err != nil {
			t.Errorf("ResolveWallClock(%02d:30) = %v, want it to resolve", hour, err)
		}
	}

	// Fall back 2026-10-25: 02:00-03:00 happens twice in Prague.
	if _, err := ResolveWallClock(2026, time.October, 25, 2, 30, prague); CodeOf(err) != CodeAmbiguousTime {
		t.Fatalf("repeated local time: code = %q, want %q (err %v)", CodeOf(err), CodeAmbiguousTime, err)
	}

	// A zone with no DST resolves everything.
	utc := mustZone(t, "UTC")
	if _, err := ResolveWallClock(2026, time.March, 29, 2, 30, utc); err != nil {
		t.Fatalf("UTC wall clock rejected: %v", err)
	}
}

func TestMergeIntervals(t *testing.T) {
	base := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	hour := func(h, m int) time.Time { return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }

	got := MergeIntervals([]Interval{
		{Start: hour(2, 0), End: hour(3, 0)},
		{Start: hour(0, 0), End: hour(1, 0)},
		{Start: hour(0, 30), End: hour(1, 30)}, // overlaps the previous
		{Start: hour(3, 0), End: hour(4, 0)},   // touches, so it coalesces
		{Start: hour(9, 0), End: hour(8, 0)},   // invalid, dropped
	})
	want := []Interval{
		{Start: hour(0, 0), End: hour(1, 30)},
		{Start: hour(2, 0), End: hour(4, 0)},
	}
	if len(got) != len(want) {
		t.Fatalf("MergeIntervals returned %d intervals, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) {
			t.Fatalf("interval %d = %v, want %v", i, got[i], want[i])
		}
	}
	if MergeIntervals(nil) != nil {
		t.Fatal("MergeIntervals(nil) returned a non-nil slice")
	}
}

func TestSubtractIntervals(t *testing.T) {
	base := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	window := Interval{Start: at(0), End: at(8)}

	got := SubtractIntervals(window, []Interval{
		{Start: at(1), End: at(2)},
		{Start: at(4), End: at(5)},
		{Start: at(-3), End: at(0)}, // entirely before the window
	})
	want := []Interval{
		{Start: at(0), End: at(1)},
		{Start: at(2), End: at(4)},
		{Start: at(5), End: at(8)},
	}
	if len(got) != len(want) {
		t.Fatalf("SubtractIntervals returned %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) {
			t.Fatalf("gap %d = %v, want %v", i, got[i], want[i])
		}
	}

	if got := SubtractIntervals(window, []Interval{{Start: at(-1), End: at(9)}}); len(got) != 0 {
		t.Fatalf("a fully covered window returned %v, want no gaps", got)
	}
}

func TestWorkingWindowsSkipsUnselectedDays(t *testing.T) {
	prague := mustZone(t, "Europe/Prague")
	// 2026-09-05 is a Saturday; the window runs Saturday to Tuesday.
	window := Interval{
		Start: at(t, time.DateTime, "2026-09-05 00:00:00", prague),
		End:   at(t, time.DateTime, "2026-09-09 00:00:00", prague),
	}
	wh := WorkingHours{Start: 9 * 60, End: 17 * 60}

	got := WorkingWindows(window, prague, wh)
	if len(got) != 2 {
		t.Fatalf("WorkingWindows returned %d windows, want Monday and Tuesday only: %v", len(got), got)
	}
	for _, w := range got {
		if w.Start.Weekday() == time.Saturday || w.Start.Weekday() == time.Sunday {
			t.Fatalf("a weekend window slipped through: %v", w)
		}
		if w.Start.Hour() != 9 || w.End.Hour() != 17 {
			t.Fatalf("window %v does not match the requested working hours", w)
		}
	}

	weekend := WorkingWindows(window, prague, WorkingHours{Start: 10 * 60, End: 12 * 60, Days: []string{"saturday"}})
	if len(weekend) != 1 || weekend[0].Start.Weekday() != time.Saturday {
		t.Fatalf("explicit weekend selection returned %v", weekend)
	}
}

// On the spring-forward day the working window still starts at the requested
// wall-clock time and is one hour shorter in real time.
func TestWorkingWindowsSurviveDST(t *testing.T) {
	prague := mustZone(t, "Europe/Prague")
	window := Interval{
		Start: at(t, time.DateTime, "2026-03-29 00:00:00", prague),
		End:   at(t, time.DateTime, "2026-03-30 00:00:00", prague),
	}
	got := WorkingWindows(window, prague, WorkingHours{Start: 1 * 60, End: 6 * 60, Days: []string{"sunday"}})
	if len(got) != 1 {
		t.Fatalf("WorkingWindows returned %v, want one Sunday window", got)
	}
	if got[0].Start.Hour() != 1 {
		t.Fatalf("window starts at %v, want 01:00 local", got[0].Start)
	}
	if d := got[0].Duration(); d != 4*time.Hour {
		t.Fatalf("the 01:00-06:00 window lasted %v on the spring-forward day, want 4h", d)
	}
}

func TestFreeSlots(t *testing.T) {
	prague := mustZone(t, "Europe/Prague")
	day := func(hour, min int) time.Time {
		return time.Date(2026, 9, 7, hour, min, 0, 0, prague) // a Monday
	}
	window := Interval{Start: day(0, 0), End: day(23, 59)}
	wh := WorkingHours{Start: 9 * 60, End: 17 * 60}
	busy := []Interval{
		{Start: day(9, 0), End: day(10, 0)},
		{Start: day(11, 0), End: day(11, 30)},
		{Start: day(13, 0), End: day(16, 45)},
	}

	got := FreeSlots(window, prague, wh, busy, 45*time.Minute, 0)
	want := []Interval{
		{Start: day(10, 0), End: day(11, 0)},
		{Start: day(11, 30), End: day(13, 0)},
	}
	if len(got) != len(want) {
		t.Fatalf("FreeSlots returned %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) {
			t.Fatalf("slot %d = %v, want %v", i, got[i], want[i])
		}
	}

	// A buffer shrinks the usable gaps and can eliminate one entirely.
	buffered := FreeSlots(window, prague, wh, busy, 45*time.Minute, 15*time.Minute)
	if len(buffered) != 1 {
		t.Fatalf("with a 15 minute buffer FreeSlots returned %v, want one slot", buffered)
	}
	// The 10:00-11:00 gap shrinks to 30 minutes and no longer fits.
	if !buffered[0].Start.Equal(day(11, 45)) || !buffered[0].End.Equal(day(12, 45)) {
		t.Fatalf("buffered slot = %v, want 11:45-12:45", buffered[0])
	}

	if got := FreeSlots(window, prague, wh, busy, 0, 0); got != nil {
		t.Fatal("FreeSlots returned slots for a zero duration")
	}
}

func TestFreeSlotsWithNoBusyIntervals(t *testing.T) {
	prague := mustZone(t, "Europe/Prague")
	window := Interval{
		Start: at(t, time.DateTime, "2026-09-07 00:00:00", prague),
		End:   at(t, time.DateTime, "2026-09-08 00:00:00", prague),
	}
	got := FreeSlots(window, prague, WorkingHours{Start: 9 * 60, End: 17 * 60}, nil, time.Hour, 0)
	if len(got) != 1 || got[0].Duration() != 8*time.Hour {
		t.Fatalf("FreeSlots on an empty day returned %v, want one 8h window", got)
	}
}

func TestAllDayIntervalUsesLocalMidnight(t *testing.T) {
	prague := mustZone(t, "Europe/Prague")
	iv, err := AllDayInterval(DateOnly{2026, time.September, 5}, DateOnly{2026, time.September, 6}, prague)
	if err != nil {
		t.Fatalf("AllDayInterval: %v", err)
	}
	if iv.Start.Hour() != 0 || iv.Start.Day() != 5 {
		t.Fatalf("start = %v, want local midnight on the 5th", iv.Start)
	}
	if iv.Duration() != 24*time.Hour {
		t.Fatalf("duration = %v, want 24h", iv.Duration())
	}
	// The exclusive end means the interval stops before the 6th begins.
	if iv.Contains(iv.End) {
		t.Fatal("the exclusive end date was included")
	}

	if _, err := AllDayInterval(DateOnly{2026, time.September, 6}, DateOnly{2026, time.September, 6}, prague); err == nil {
		t.Fatal("AllDayInterval accepted an inclusive end date")
	}
	if _, err := AllDayInterval(DateOnly{}, DateOnly{2026, time.September, 6}, prague); err == nil {
		t.Fatal("AllDayInterval accepted a missing start date")
	}
}

// An all-day interval that spans a DST transition stays anchored to local
// midnight instead of drifting an hour.
func TestAllDayIntervalAcrossDST(t *testing.T) {
	prague := mustZone(t, "Europe/Prague")
	iv, err := AllDayInterval(DateOnly{2026, time.October, 24}, DateOnly{2026, time.October, 26}, prague)
	if err != nil {
		t.Fatalf("AllDayInterval: %v", err)
	}
	if iv.Start.Hour() != 0 || iv.End.Hour() != 0 {
		t.Fatalf("interval %v does not start and end at local midnight", iv)
	}
	if iv.Duration() != 49*time.Hour {
		t.Fatalf("duration = %v, want 49h across the autumn transition", iv.Duration())
	}
}

func TestLoadZoneRejectsImplicitZones(t *testing.T) {
	for _, name := range []string{"", "Local", "Mars/Olympus", "CEST"} {
		if _, err := LoadZone(name); err == nil {
			t.Errorf("LoadZone(%q) accepted an implicit or unknown zone", name)
		}
	}
}

func TestOffsetString(t *testing.T) {
	tests := map[int]string{0: "+00:00", 3600: "+01:00", 7200: "+02:00", -18000: "-05:00", 20700: "+05:45"}
	for seconds, want := range tests {
		if got := offsetString(seconds); got != want {
			t.Errorf("offsetString(%d) = %q, want %q", seconds, got, want)
		}
	}
}
