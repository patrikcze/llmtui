package tui

import (
	"context"

	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/tools"
)

// resourceAdapter is a thin, session-bound wrapper around *entity.Registry
// satisfying tools.ResourceReader. *entity.Registry's own OpenBody method
// already has the exact signature tools.ResourceReader needs, so no adapter
// code is technically required for interface satisfaction — this wrapper
// exists anyway (per the plan's explicit ask for "a session/generation-bound
// TUI adapter, no upward imports") so a later phase can add
// generation-checking or additional bookkeeping without changing
// Runner.Resources' type again.
type resourceAdapter struct {
	registry *entity.Registry
}

// newResourceAdapter builds the tools.ResourceReader the Runner uses to
// resolve read_file's resource_id selector. r may be nil (mirrors
// *entity.Registry's own nil-receiver safety on every method); callers
// should still prefer leaving Runner.Resources nil entirely when no
// registry exists, matching Web/Skills/PersonalApps' nil-means-disabled
// convention.
func newResourceAdapter(r *entity.Registry) tools.ResourceReader {
	return &resourceAdapter{registry: r}
}

func (a *resourceAdapter) OpenBody(ctx context.Context, id entity.ID) (entity.ResourceView, entity.BodyLease, error) {
	return a.registry.OpenBody(ctx, id)
}
