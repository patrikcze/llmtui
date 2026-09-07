package personalapps

import (
	"sort"
	"time"
)

// Interval is a half-open time range [Start, End). Half-open is the only
// interval convention this package uses: two adjacent intervals never both
// claim the boundary instant, so an event that ends when another begins is
// not a conflict.
type Interval struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Valid reports whether the interval has a positive duration.
func (i Interval) Valid() bool {
	return !i.Start.IsZero() && !i.End.IsZero() && i.Start.Before(i.End)
}

// Duration returns the interval length, or zero when it is not valid.
func (i Interval) Duration() time.Duration {
	if !i.Valid() {
		return 0
	}
	return i.End.Sub(i.Start)
}

// Overlaps reports whether two half-open intervals share any instant.
func (i Interval) Overlaps(o Interval) bool {
	return i.Start.Before(o.End) && o.Start.Before(i.End)
}

// Contains reports whether t falls inside the half-open interval.
func (i Interval) Contains(t time.Time) bool {
	return !t.Before(i.Start) && t.Before(i.End)
}

// LoadZone resolves an IANA zone name. An empty or unknown name is
// CodeInvalidTimezone: this package never falls back to the host's local
// zone, because a silently assumed zone is how a meeting lands an hour off.
func LoadZone(name string) (*time.Location, error) {
	if name == "" {
		return nil, Errorf(CodeInvalidTimezone, "timezone is required, for example \"Europe/Prague\"")
	}
	if name == "Local" {
		return nil, Errorf(CodeInvalidTimezone, "timezone must be an explicit IANA zone, not %q", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, Errorf(CodeInvalidTimezone, "unknown IANA timezone %q", clip(name, 64))
	}
	return loc, nil
}

// ValidateOffsetAgreement rejects an instant whose RFC3339 offset disagrees
// with loc at that instant. A request that says 09:00+01:00 in a zone that
// is at +02:00 then is a unit mismatch, not a preference, and guessing which
// half the caller meant is exactly the class of error this integration must
// not make.
func ValidateOffsetAgreement(field string, t time.Time, loc *time.Location) error {
	if t.IsZero() {
		return Errorf(CodeInvalidRequest, "%s is required", field)
	}
	_, given := t.Zone()
	_, want := t.In(loc).Zone()
	if given != want {
		return Errorf(CodeInvalidTimezone, "%s carries offset %s but the requested timezone is %s at that instant",
			field, offsetString(given), offsetString(want))
	}
	return nil
}

// ValidateInterval checks a requested query window end to end: the zone
// resolves, both endpoints agree with it, the window is half-open and
// non-empty, and it stays inside the configured maximum span.
func ValidateInterval(start, end time.Time, timezone string, l Limits) (*time.Location, error) {
	full := l.withDefaults()
	loc, err := LoadZone(timezone)
	if err != nil {
		return nil, err
	}
	if err := ValidateOffsetAgreement("start", start, loc); err != nil {
		return nil, err
	}
	if err := ValidateOffsetAgreement("end", end, loc); err != nil {
		return nil, err
	}
	if !start.Before(end) {
		return nil, Errorf(CodeInvalidRequest, "start must be earlier than end; the interval is half-open")
	}
	if max := time.Duration(full.MaxCalendarDays) * 24 * time.Hour; end.Sub(start) > max {
		return nil, Errorf(CodeInvalidRequest, "the window spans more than the maximum %d days", full.MaxCalendarDays)
	}
	return loc, nil
}

// ResolveWallClock converts a wall-clock time in loc to an instant, refusing
// the two cases a DST transition creates: a time that does not exist because
// the clock jumped forward, and a time that happens twice because it jumped
// back. Both need a human decision, so both are errors rather than a guess.
func ResolveWallClock(year int, month time.Month, day, hour, minute int, loc *time.Location) (time.Time, error) {
	if loc == nil {
		return time.Time{}, Errorf(CodeInvalidTimezone, "timezone is required")
	}
	wall := time.Date(year, month, day, hour, minute, 0, 0, time.UTC).Unix()
	approx := time.Date(year, month, day, hour, minute, 0, 0, loc)

	// The zone can only have two offsets around a transition; probe both
	// sides of the requested wall clock and keep every candidate whose local
	// rendering matches what was asked for.
	var candidates []time.Time
	seen := make(map[int64]bool, 2)
	for _, probe := range []time.Time{approx.Add(-13 * time.Hour), approx.Add(13 * time.Hour)} {
		_, offset := probe.In(loc).Zone()
		cand := time.Unix(wall-int64(offset), 0).In(loc)
		y, mo, d := cand.Date()
		if y != year || mo != month || d != day || cand.Hour() != hour || cand.Minute() != minute {
			continue
		}
		if seen[cand.Unix()] {
			continue
		}
		seen[cand.Unix()] = true
		candidates = append(candidates, cand)
	}

	switch len(candidates) {
	case 0:
		return time.Time{}, Errorf(CodeAmbiguousTime,
			"%04d-%02d-%02d %02d:%02d does not exist in %s because the clock moves forward",
			year, month, day, hour, minute, loc)
	case 1:
		return candidates[0], nil
	default:
		return time.Time{}, Errorf(CodeAmbiguousTime,
			"%04d-%02d-%02d %02d:%02d happens twice in %s; the offset must be stated explicitly",
			year, month, day, hour, minute, loc)
	}
}

// MergeIntervals sorts and coalesces overlapping or touching intervals. It
// is the busy-time union used before any free-slot computation; invalid
// entries are dropped rather than silently inverted.
func MergeIntervals(in []Interval) []Interval {
	valid := make([]Interval, 0, len(in))
	for _, iv := range in {
		if iv.Valid() {
			valid = append(valid, iv)
		}
	}
	if len(valid) == 0 {
		return nil
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].Start.Equal(valid[j].Start) {
			return valid[i].End.Before(valid[j].End)
		}
		return valid[i].Start.Before(valid[j].Start)
	})
	merged := []Interval{valid[0]}
	for _, iv := range valid[1:] {
		last := &merged[len(merged)-1]
		if iv.Start.After(last.End) {
			merged = append(merged, iv)
			continue
		}
		if iv.End.After(last.End) {
			last.End = iv.End
		}
	}
	return merged
}

// SubtractIntervals removes busy from window and returns the remaining
// half-open gaps in chronological order. busy need not be merged.
func SubtractIntervals(window Interval, busy []Interval) []Interval {
	if !window.Valid() {
		return nil
	}
	cursor := window.Start
	var free []Interval
	for _, b := range MergeIntervals(busy) {
		if !b.Overlaps(window) {
			continue
		}
		if b.Start.After(cursor) {
			free = append(free, Interval{Start: cursor, End: minTime(b.Start, window.End)})
		}
		if b.End.After(cursor) {
			cursor = b.End
		}
		if !cursor.Before(window.End) {
			return free
		}
	}
	if cursor.Before(window.End) {
		free = append(free, Interval{Start: cursor, End: window.End})
	}
	return free
}

// WorkingWindows expands the working hours over every selected weekday that
// intersects window, in loc. Boundaries are built from the local calendar
// date, not by adding 24 hours, so a DST day is 23 or 25 hours long and the
// window still starts at the requested wall-clock time.
func WorkingWindows(window Interval, loc *time.Location, wh WorkingHours) []Interval {
	if !window.Valid() || loc == nil {
		return nil
	}
	days := wh.Weekdays()
	var out []Interval
	local := window.Start.In(loc)
	year, month, day := local.Date()
	for i := 0; i < maxWorkingDays; i++ {
		midnight := time.Date(year, month, day+i, 0, 0, 0, 0, loc)
		if !midnight.Before(window.End) {
			break
		}
		if !days[midnight.Weekday()] {
			continue
		}
		start := time.Date(year, month, day+i, int(wh.Start)/60, int(wh.Start)%60, 0, 0, loc)
		end := time.Date(year, month, day+i, int(wh.End)/60, int(wh.End)%60, 0, 0, loc)
		clipped := Interval{Start: maxTime(start, window.Start), End: minTime(end, window.End)}
		if clipped.Valid() {
			out = append(out, clipped)
		}
	}
	return out
}

// maxWorkingDays bounds the day expansion independently of the configured
// window so a bad interval cannot spin here.
const maxWorkingDays = 400

// FreeSlots returns every gap of at least duration inside the working hours
// of window that no busy interval touches, after padding each busy interval
// by buffer on both sides.
//
// The result means "free in the calendars that were actually read, as
// observed at that moment". It is not other people's availability, and a
// remote change can invalidate it before anything is created.
func FreeSlots(window Interval, loc *time.Location, wh WorkingHours, busy []Interval, duration, buffer time.Duration) []Interval {
	if duration <= 0 {
		return nil
	}
	padded := make([]Interval, 0, len(busy))
	for _, b := range busy {
		if !b.Valid() {
			continue
		}
		padded = append(padded, Interval{Start: b.Start.Add(-buffer), End: b.End.Add(buffer)})
	}
	merged := MergeIntervals(padded)

	var out []Interval
	for _, work := range WorkingWindows(window, loc, wh) {
		for _, gap := range SubtractIntervals(work, merged) {
			if gap.Duration() >= duration {
				out = append(out, gap)
			}
		}
	}
	return out
}

// AllDayInterval converts an all-day date range to an instant interval in
// loc. End is exclusive in calendar terms, so a single all-day event on the
// 5th is 5th to 6th. The conversion goes through local midnight, never
// through UTC midnight, so an all-day event does not drift a day.
func AllDayInterval(start, end DateOnly, loc *time.Location) (Interval, error) {
	if loc == nil {
		return Interval{}, Errorf(CodeInvalidTimezone, "timezone is required")
	}
	if start.IsZero() || end.IsZero() {
		return Interval{}, Errorf(CodeInvalidRequest, "all-day events need both a start and an exclusive end date")
	}
	iv := Interval{Start: start.In(loc), End: end.In(loc)}
	if !iv.Valid() {
		return Interval{}, Errorf(CodeInvalidRequest, "the all-day end date %s must be after the start date %s; the end is exclusive", end, start)
	}
	return iv, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func offsetString(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return sign + time.Unix(int64(seconds), 0).UTC().Format("15:04")
}
