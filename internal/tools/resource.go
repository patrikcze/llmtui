package tools

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/patrikcze/llmtui/internal/entity"
)

// This file is Phase 2b, part ii of the next-generation tool runtime plan
// (.claude/tasks/plans/next-generation-tool-runtime.md §12/§15/§26, evidence
// rows L10/L13): it wires exactly one producer (run_command) and one
// consumer (read_file's resource_id selector) onto the entity body substrate
// Phase 2b-i shipped (internal/entity: Registry.Publish/OpenBody). Nothing
// here decides retention policy — Runner.Resources being non-nil only makes
// recovery *possible*; the TUI layer (internal/tui/entity_context.go)
// decides whether to actually call Registry.Publish for a given Capture
// based on entities.output_storage.

// ResourceReader is what read_file's resource_id path needs from
// internal/entity.Registry. It is declared here (the consumer), not in
// internal/entity, because this package already imports internal/entity for
// entity.Candidate (see Result.Entities) — this introduces no new import
// direction. *entity.Registry.OpenBody already satisfies this interface
// exactly; the TUI layer still wraps it in a small adapter type
// (internal/tui/tool_resource_adapter.go) rather than assigning the registry
// pointer directly, leaving a seam for a later phase to add
// generation-checking or additional bookkeeping without changing this
// interface or Runner.Resources' type again.
type ResourceReader interface {
	OpenBody(ctx context.Context, id entity.ID) (entity.ResourceView, entity.BodyLease, error)
}

// Capture is an unpublished, bounded body a producer hands back alongside
// its normal Result for the TUI layer to publish (or not, if entities are
// disabled or entities.output_storage is "off") into the entity registry.
// It is NOT a second copy for Output to also carry — Output stays exactly
// what it was before this phase (the same capped preview text), and Body
// here is the producer's own already-bounded retained bytes (e.g.
// boundedCapture.Bytes() for run_command), never a fresh, larger read.
type Capture struct {
	Kind        entity.Kind
	Label       string
	Trust       entity.Trust
	ContentType string
	Body        []byte
	// BodyDigest, if the producer already computed one (run_command's
	// ContentDigest from Phase 2a), is reused rather than rehashed.
	BodyDigest string
}

// errResourceReadingDisabled is returned when read_file is called with
// resource_id but no ResourceReader is wired (mirrors errWebDisabled for
// web_fetch/web_search). Retrying will not help until the caller enables
// entity retention, so RetryNone applies exactly as it does for the web
// case.
var errResourceReadingDisabled = errors.New("resource retention is not enabled: no output was retained for read-back")

// readResourceMeta implements read_file's resource_id branch: recover a
// previously published entity body instead of reading a workspace path. It
// does not support offset/limit windowing this phase — a resource read is
// always "from the start, up to the runner's own maxKB display cap," exactly
// as a plain whole-file read already behaves today (see readFileMeta's
// !ranged branch, which this mirrors).
func (r *Runner) readResourceMeta(ctx context.Context, rawID string) (output string, meta ResultMeta, err error) {
	meta.Effect = EffectNone
	if r.Resources == nil {
		return "", meta, withCode(errResourceReadingDisabled, "unsupported_content", RetryNone)
	}
	id, perr := entity.ParseID(rawID)
	if perr != nil {
		return "", meta, withCode(fmt.Errorf("invalid resource_id %q: %w", rawID, perr), "invalid_arguments", RetryCorrectInput)
	}
	_, lease, oerr := r.Resources.OpenBody(ctx, id)
	if oerr != nil {
		// This phase does not distinguish entity.ErrNotAResourceBody from any
		// other not-found/expired case (both are Phase 3+ §23 granularity);
		// every OpenBody failure is a uniform resource_unavailable so the
		// model gets one stable, retryable-by-rereading code instead of a raw
		// Go error string it cannot switch on.
		return "", meta, withCode(fmt.Errorf("read_file resource_id %q: %w", rawID, oerr), "resource_unavailable", RetryReread)
	}
	defer func() { err = errors.Join(err, lease.Close()) }()

	byteLimit := int64(r.maxKB) * 1024
	size := lease.Size()
	readLen := size
	truncated := false
	if readLen > byteLimit {
		readLen = byteLimit
		truncated = true
	}
	buf := make([]byte, readLen)
	if readLen > 0 {
		if _, rerr := lease.ReadAt(buf, 0); rerr != nil && rerr != io.EOF {
			return "", meta, fmt.Errorf("read resource body: %w", rerr)
		}
	}

	text, consumed := boundedUTF8(buf, int(byteLimit))
	cov := Coverage{
		SourceComplete:  !truncated,
		CaptureComplete: !truncated,
		PreviewComplete: consumed >= len(buf),
		ObservedBytes:   size,
		RetainedBytes:   int64(consumed),
		TotalBytes:      int64Ptr(size),
	}
	outcome := OutcomeOK
	if truncated || consumed < len(buf) {
		cov.Reasons = []string{"bytes"}
		outcome = OutcomePartial
	}
	meta.Outcome, meta.Coverage = outcome, cov
	if truncated || consumed < len(buf) {
		return text + fmt.Sprintf("\n… truncated (%d of %d bytes shown)", consumed, size), meta, nil
	}
	return text, meta, nil
}
