package tui

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/tools"
	"github.com/patrikcze/llmtui/internal/web"
)

// webSnapshotStore is a metadata-only URL index. The entity registry owns the
// retained bytes; this map owns only the session-local identity and freshness
// metadata needed to decide whether a fetch may be reused.
type webSnapshotStore struct {
	mu       sync.Mutex
	registry *entity.Registry
	refs     map[string]webSnapshotRef
}

type webSnapshotRef struct {
	id       entity.ID
	metadata entity.ResourceMetadata
}

var _ tools.WebSnapshotStore = (*webSnapshotStore)(nil)
var _ tools.WebSnapshotIndexer = (*webSnapshotStore)(nil)

func newWebSnapshotStore(registry *entity.Registry) *webSnapshotStore {
	return &webSnapshotStore{registry: registry, refs: make(map[string]webSnapshotRef)}
}

func (s *webSnapshotStore) GetWebSnapshot(ctx context.Context, requestedURL, mode, refreshEpoch string, maxAge time.Duration) (web.Page, bool, error) {
	key := webSnapshotKey(requestedURL)
	s.mu.Lock()
	ref, ok := s.refs[key]
	s.mu.Unlock()
	if !ok || ref.metadata.NoStore || strings.EqualFold(mode, string(web.FetchRefresh)) {
		return web.Page{}, false, nil
	}
	now := time.Now().UTC()
	fresh := true
	if strings.EqualFold(mode, string(web.FetchAuto)) {
		if ref.metadata.FreshUntil.IsZero() || now.After(ref.metadata.FreshUntil) {
			fresh = false
		}
		if maxAge > 0 && !ref.metadata.Observation.ObservedAt.IsZero() && now.Sub(ref.metadata.Observation.ObservedAt) > maxAge {
			fresh = false
		}
	}
	if refreshEpoch != "" && ref.metadata.Observation.Freshness != "" && ref.metadata.Observation.Freshness != refreshEpoch {
		return web.Page{}, false, nil
	}
	if s.registry == nil {
		return web.Page{}, false, fmt.Errorf("web snapshot registry is unavailable")
	}
	view, lease, err := s.registry.OpenBody(ctx, ref.id)
	if err != nil {
		return web.Page{}, false, err
	}
	defer func() { _ = lease.Close() }()
	data := make([]byte, lease.Size())
	if len(data) > 0 {
		if _, err := lease.ReadAt(data, 0); err != nil && err != io.EOF {
			return web.Page{}, false, err
		}
	}
	metadata := view.Resource
	page := web.Page{
		URL: metadata.FinalURL, RequestedURL: metadata.RequestedURL, Content: metadata.Preview,
		Body: string(data), ContentType: metadata.ContentType, Bytes: len(data), Status: 200,
		ETag: metadata.ETag, LastModified: metadata.LastModified, CacheControl: metadata.CacheControl,
		Vary: metadata.Vary, NoStore: metadata.NoStore, FreshUntil: metadata.FreshUntil,
		BodyDigest: metadata.BodyDigest, SourceDigest: metadata.SourceDigest,
		AcquiredAt: metadata.Observation.ObservedAt,
	}
	return page, fresh, nil
}

func (s *webSnapshotStore) PutWebSnapshot(ctx context.Context, requestedURL string, page web.Page) (string, error) {
	if s.registry == nil {
		return "", fmt.Errorf("web snapshot registry is unavailable")
	}
	body := []byte(page.Body)
	if len(body) == 0 {
		body = []byte(page.Content)
	}
	metadata := webMetadata(requestedURL, page)
	label := page.Title
	if label == "" {
		label = metadata.FinalURL
	}
	view, err := s.registry.Publish(ctx, entity.Candidate{
		Kind: entity.KindWebPage, Label: label, Trust: entity.TrustWebUntrusted,
		Scope: entity.ScopeSession, Resource: metadata,
	}, body)
	if err != nil {
		return "", err
	}
	s.IndexWebSnapshot(requestedURL, view.ID, view.Resource)
	return view.ID.String(), nil
}

func (s *webSnapshotStore) IndexWebSnapshot(requestedURL string, id entity.ID, metadata entity.ResourceMetadata) {
	if metadata.NoStore || strings.TrimSpace(requestedURL) == "" || id == "" {
		return
	}
	s.mu.Lock()
	s.refs[webSnapshotKey(requestedURL)] = webSnapshotRef{id: id, metadata: metadata}
	s.mu.Unlock()
}

func (s *webSnapshotStore) Reset() {
	s.mu.Lock()
	s.refs = make(map[string]webSnapshotRef)
	s.mu.Unlock()
}

func webMetadata(requestedURL string, page web.Page) entity.ResourceMetadata {
	return entity.ResourceMetadata{
		ContentType: page.ContentType, BodyDigest: page.BodyDigest, SourceDigest: page.SourceDigest,
		RequestedURL: requestedURL, FinalURL: page.URL, ETag: page.ETag, LastModified: page.LastModified,
		CacheControl: page.CacheControl, Vary: page.Vary, NoStore: page.NoStore, FreshUntil: page.FreshUntil,
		Preview: page.Content, Observation: entity.ObservationMetadata{ObservedAt: page.AcquiredAt, Acquisition: "network"},
	}
}

func webSnapshotKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimSpace(raw)
	}
	u.Fragment = ""
	u.User = nil
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String()
}
