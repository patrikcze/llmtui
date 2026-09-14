package agent

import (
	"strings"
	"testing"
)

func TestObservationCachePutAndLatest(t *testing.T) {
	c := NewObservationCache()
	id, _, evicted := c.Put("read_file", "report.md", 1, "# Q3 report\nbody", true)
	if id != "o1" || evicted {
		t.Fatalf("id = %q, evicted = %v", id, evicted)
	}
	view, ok := c.Latest(resourceKeyFor("read_file", "report.md"))
	if !ok || view.Excerpt != "# Q3 report\nbody" || view.Truncated {
		t.Fatalf("view = %+v, ok = %v", view, ok)
	}
	if _, ok := c.Latest(resourceKeyFor("read_file", "other.md")); ok {
		t.Fatal("unrelated resource key must not match")
	}
}

func TestObservationCacheLatestPrefersMostRecent(t *testing.T) {
	c := NewObservationCache()
	c.Put("read_file", "report.md", 1, "first read", true)
	c.Put("read_file", "report.md", 2, "second read", true)
	view, ok := c.Latest(resourceKeyFor("read_file", "report.md"))
	if !ok || view.Excerpt != "second read" || view.Cycle != 2 {
		t.Fatalf("view = %+v, ok = %v, want the most recent read", view, ok)
	}
}

func TestObservationCacheTruncatesLongText(t *testing.T) {
	c := NewObservationCache()
	long := strings.Repeat("x", MaxObservationExcerpt+100)
	c.Put("read_file", "big.txt", 1, long, true)
	view, ok := c.Latest(resourceKeyFor("read_file", "big.txt"))
	if !ok {
		t.Fatal("expected a recorded view")
	}
	// truncate() appends an ellipsis marker after the byte cut, so allow a
	// few bytes of slack over the exact bound rather than requiring an exact
	// MaxObservationExcerpt-byte cutoff.
	if !view.Truncated || len(view.Excerpt) > MaxObservationExcerpt+4 {
		t.Fatalf("view = %+v, want a bounded, marked-truncated excerpt", view)
	}
	if view.TotalBytes != len(long) {
		t.Fatalf("total bytes = %d, want %d", view.TotalBytes, len(long))
	}
}

func TestObservationCacheEvictsOldestAndReportsIt(t *testing.T) {
	c := NewObservationCache()
	for i := 0; i < MaxObservations; i++ {
		if _, _, evicted := c.Put("read_file", string(rune('a'+i)), 1, "x", true); evicted {
			t.Fatalf("premature eviction at index %d", i)
		}
	}
	// The cache is now full; one more Put must evict the oldest entry
	// (resource "a") and report that entry's view.
	_, evictedView, evicted := c.Put("read_file", "overflow", 2, "y", true)
	if !evicted || evictedView.ResourceKey != resourceKeyFor("read_file", "a") || evictedView.ResourceLabel() != "read_file(a)" {
		t.Fatalf("evicted = %v, evictedView = %+v, want the oldest resource evicted and named", evicted, evictedView)
	}
	if _, ok := c.Latest(resourceKeyFor("read_file", "a")); ok {
		t.Fatal("evicted resource must no longer be found")
	}
	if len(c.Recent(MaxObservations)) != MaxObservations {
		t.Fatalf("cache size after eviction = %d, want bounded at %d", len(c.Recent(MaxObservations)), MaxObservations)
	}
}

func TestObservationCacheRecentNewestFirst(t *testing.T) {
	c := NewObservationCache()
	c.Put("read_file", "a.txt", 1, "a", true)
	c.Put("read_file", "b.txt", 2, "b", true)
	c.Put("read_file", "c.txt", 3, "c", true)
	recent := c.Recent(2)
	if len(recent) != 2 || recent[0].Detail != "c.txt" || recent[1].Detail != "b.txt" {
		t.Fatalf("recent = %+v, want newest first", recent)
	}
}

func TestObservationCacheNilReceiverIsSafe(t *testing.T) {
	var c *ObservationCache
	if id, _, evicted := c.Put("read_file", "x", 1, "y", true); id != "" || evicted {
		t.Fatalf("nil Put = %q, %v", id, evicted)
	}
	if _, ok := c.Latest("read_file\x1fx"); ok {
		t.Fatal("nil Latest must report not found")
	}
	if got := c.Recent(4); got != nil {
		t.Fatalf("nil Recent = %+v, want nil", got)
	}
}

func TestObservationViewFormatExcerptMarksTruncation(t *testing.T) {
	v := ObservationView{Tool: "read_file", Detail: "report.md", Cycle: 1, Excerpt: "heading", Truncated: true}
	got := v.FormatExcerpt()
	if !strings.Contains(got, "read_file(report.md)") || !strings.Contains(got, "cycle 1") ||
		!strings.Contains(got, "heading") || !strings.Contains(got, "truncated") {
		t.Fatalf("FormatExcerpt = %q", got)
	}
}
