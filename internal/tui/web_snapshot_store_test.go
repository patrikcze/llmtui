package tui

import (
	"context"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/web"
)

func TestWebSnapshotStoreRetainsBodyAndHonorsFreshness(t *testing.T) {
	reg := entity.NewRegistry(entity.Limits{MaxEntities: 8, MaxTotalPayload: 1 << 20, MaxBodyBytes: 1 << 20})
	store := newWebSnapshotStore(reg)
	page := web.Page{URL: "https://example.test/weather", Content: "preview", Body: "full weather body", ContentType: "text/plain", Status: 200,
		ETag: `"v1"`, FreshUntil: time.Now().Add(time.Minute), AcquiredAt: time.Now().UTC(), BodyDigest: "digest"}
	if _, err := store.PutWebSnapshot(context.Background(), page.URL, page); err != nil {
		t.Fatal(err)
	}
	got, hit, err := store.GetWebSnapshot(context.Background(), page.URL, string(web.FetchAuto), "", 0)
	if err != nil || !hit || got.Body != page.Body || got.Content != page.Content || got.ETag != page.ETag {
		t.Fatalf("got=%+v hit=%v err=%v", got, hit, err)
	}
	got, hit, err = store.GetWebSnapshot(context.Background(), page.URL, string(web.FetchAuto), "", time.Nanosecond)
	if err != nil || hit || got.Body != page.Body {
		t.Fatalf("stale snapshot got=%+v hit=%v err=%v", got, hit, err)
	}
}

func TestWebSnapshotStoreDoesNotIndexNoStore(t *testing.T) {
	reg := entity.NewRegistry(entity.Limits{MaxEntities: 8, MaxTotalPayload: 1 << 20, MaxBodyBytes: 1 << 20})
	store := newWebSnapshotStore(reg)
	page := web.Page{URL: "https://example.test/private", Body: "secret", Content: "secret", NoStore: true, AcquiredAt: time.Now().UTC()}
	if _, err := store.PutWebSnapshot(context.Background(), page.URL, page); err != nil {
		t.Fatal(err)
	}
	if _, hit, err := store.GetWebSnapshot(context.Background(), page.URL, string(web.FetchCached), "", 0); err != nil || hit {
		t.Fatalf("no-store snapshot hit=%v err=%v", hit, err)
	}
}
