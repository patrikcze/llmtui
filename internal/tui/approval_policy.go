package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/tools"
)

const scopedApprovalTTL = 15 * time.Minute

type capabilityGrant struct {
	tool        string
	target      string
	pathPattern bool
	// variant fingerprints the part of the call that a target alone does not
	// capture — for write_file, the bytes being written. A grant only
	// matches a call with the identical variant, so approving one write can
	// never silently authorise a later write of different content to the
	// same path.
	variant   string
	expiresAt time.Time
}

// capabilityPolicy holds narrowly-scoped, temporary approvals created by
// the approval menu. It never grants a different tool, path, command, MCP
// server/tool pair, or a call after the grant expires.
type capabilityPolicy struct {
	grants []capabilityGrant
}

func (p *capabilityPolicy) Allows(c tools.Call, now time.Time) bool {
	tool, target, variant := approvalScope(c)
	kept := p.grants[:0]
	allowed := false
	for _, grant := range p.grants {
		if !grant.expiresAt.IsZero() && !now.Before(grant.expiresAt) {
			continue
		}
		kept = append(kept, grant)
		if grant.tool != tool || grant.variant != variant {
			continue
		}
		if grant.pathPattern {
			if matched, _ := filepath.Match(grant.target, target); matched {
				allowed = true
			}
		} else if grant.target == target {
			allowed = true
		}
	}
	p.grants = kept
	return allowed
}

func (p *capabilityPolicy) GrantCall(c tools.Call, now time.Time, ttl time.Duration) string {
	tool, target, variant := approvalScope(c)
	if tool == "" {
		return ""
	}
	expires := time.Time{}
	if ttl > 0 {
		expires = now.Add(ttl)
	}
	p.grants = append(p.grants, capabilityGrant{
		tool: tool, target: target, variant: variant, expiresAt: expires,
	})
	return approvalScopeDescription(c)
}

// GrantPath grants a glob-scoped approval. It deliberately leaves variant
// empty, which means a pattern grant can only ever match tools whose scope
// has no variant — read_file and list_dir. A pattern can never blanket-approve
// write_file, because every write carries a content fingerprint.
func (p *capabilityPolicy) GrantPath(tool, pattern string, now time.Time, ttl time.Duration) {
	expires := time.Time{}
	if ttl > 0 {
		expires = now.Add(ttl)
	}
	p.grants = append(p.grants, capabilityGrant{
		tool: tool, target: filepath.Clean(strings.TrimSpace(pattern)),
		pathPattern: true, expiresAt: expires,
	})
}

func (p *capabilityPolicy) Clear() {
	p.grants = nil
}

func (p *capabilityPolicy) Active(now time.Time) int {
	_ = p.Allows(tools.Call{}, now) // prune expired grants
	return len(p.grants)
}

// approvalScope reduces a call to the (tool, target, variant) triple a grant
// is keyed on. target identifies *what* is acted upon; variant pins any
// remaining input that changes the effect of acting on it.
func approvalScope(c tools.Call) (tool, target, variant string) {
	if c.MCPServer != "" {
		return "mcp:" + c.MCPServer + "/" + c.MCPTool, "", ""
	}
	switch c.Tool {
	case tools.ToolWriteFile:
		// The path says which file is clobbered; the body says with what.
		// Approving "overwrite README.md with these bytes" must not approve
		// "overwrite README.md with anything at all".
		sum := sha256.Sum256([]byte(c.Body))
		return c.Tool, filepath.Clean(strings.TrimSpace(c.Path)), hex.EncodeToString(sum[:])
	case tools.ToolReadFile, tools.ToolListDir:
		return c.Tool, filepath.Clean(strings.TrimSpace(c.Path)), ""
	case tools.ToolRunCommand:
		sum := sha256.Sum256([]byte(strings.TrimSpace(c.Body)))
		return c.Tool, hex.EncodeToString(sum[:]), ""
	case tools.ToolWebFetch:
		return c.Tool, strings.TrimSpace(c.Path), ""
	case tools.ToolWebSearch:
		return c.Tool, strings.TrimSpace(c.Body), ""
	case tools.ToolSkillLoad:
		return "", "", ""
	default:
		return c.Tool, "", ""
	}
}

func approvalScopeDescription(c tools.Call) string {
	if c.MCPServer != "" {
		return fmt.Sprintf("%s/%s", c.MCPServer, c.MCPTool)
	}
	switch c.Tool {
	case tools.ToolWriteFile:
		return fmt.Sprintf("%s %s with this exact content", c.Tool, strings.TrimSpace(c.Path))
	case tools.ToolReadFile, tools.ToolListDir, tools.ToolWebFetch:
		return fmt.Sprintf("%s %s", c.Tool, strings.TrimSpace(c.Path))
	case tools.ToolRunCommand:
		return "this exact command"
	default:
		return c.Tool
	}
}
