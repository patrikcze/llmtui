package tui

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/toolapi"
	"github.com/patrikcze/llmtui/internal/tools"
)

type toolRegistrySnapshotMsg struct {
	ctx   context.Context
	reply chan []toolapi.Tool
}

type toolRegistryServerErrMsg struct{ err error }

// toolRegistryCatalog returns the same native tool snapshot attached to the
// next provider request, enriched with llmtui's safety and approval metadata.
func (m *Model) toolRegistryCatalog() []toolapi.Tool {
	specs := m.activeToolSpecs()
	if len(specs) == 0 {
		return []toolapi.Tool{}
	}
	registry := tools.DefaultRegistry()
	registerMCPCapabilities(registry, m.mcpRegistry)
	catalog := make([]toolapi.Tool, 0, len(specs))
	for _, spec := range specs {
		info, _ := registry.Get(spec.Name)
		catalog = append(catalog, toolapi.Tool{
			Name:        spec.Name,
			Description: spec.Description,
			Source:      info.Source,
			Safety:      string(info.Safety),
			Approval:    info.Approval,
			InputSchema: spec.Parameters,
		})
	}
	return catalog
}

func toolRegistrySource(program *tea.Program) toolapi.Source {
	return func(ctx context.Context) ([]toolapi.Tool, error) {
		reply := make(chan []toolapi.Tool, 1)
		program.Send(toolRegistrySnapshotMsg{ctx: ctx, reply: reply})
		select {
		case catalog := <-reply:
			return catalog, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
