package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

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
	Resource   entity.ResourceMetadata
}

// errResourceReadingDisabled is returned when read_file is called with
// resource_id but no ResourceReader is wired (mirrors errWebDisabled for
// web_fetch/web_search). Retrying will not help until the caller enables
// entity retention, so RetryNone applies exactly as it does for the web
// case.
var errResourceReadingDisabled = errors.New("resource retention is not enabled: no output was retained for read-back")

// readResourceMeta implements read_file's resource_id branch: recover a
// previously published entity body instead of reading a workspace path. It
// supports the same bounded line and raw-byte windows as path reads, while
// keeping the retained-body size as the exact total when the lease exposes it.
func (r *Runner) readResourceMeta(ctx context.Context, rawID string, offset, limit int, byteOffset *int64) (output string, meta ResultMeta, err error) {
	meta.Effect = EffectNone
	if r.Resources == nil {
		return "", meta, withCode(errResourceReadingDisabled, "unsupported_content", RetryNone)
	}
	id, perr := entity.ParseID(rawID)
	if perr != nil {
		return "", meta, withCode(fmt.Errorf("invalid resource_id %q: %w", rawID, perr), "invalid_arguments", RetryCorrectInput)
	}
	view, lease, oerr := r.Resources.OpenBody(ctx, id)
	if oerr != nil {
		// This phase does not distinguish entity.ErrNotAResourceBody from any
		// other not-found/expired case (both are Phase 3+ §23 granularity);
		// every OpenBody failure is a uniform resource_unavailable so the
		// model gets one stable, retryable-by-rereading code instead of a raw
		// Go error string it cannot switch on.
		code := "resource_unavailable"
		if strings.Contains(strings.ToLower(oerr.Error()), "lifetime has ended") {
			code = "resource_expired"
		}
		return "", meta, withCode(fmt.Errorf("read_file resource_id %q: %w", rawID, oerr), code, RetryReread)
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	meta.SourceDigest = view.Resource.SourceDigest
	meta.ContentDigest = view.Resource.BodyDigest
	meta.FileVersion = view.Resource.FileVersion

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

	if byteOffset != nil {
		if *byteOffset > size {
			return "", meta, withCode(fmt.Errorf("read_file byte_offset %d is past retained body size %d", *byteOffset, size), "range_after_eof", RetryCorrectInput)
		}
		start := *byteOffset
		length := size - start
		if length > byteLimit+1 {
			length = byteLimit + 1
		}
		buf = make([]byte, length)
		if length > 0 {
			if _, rerr := lease.ReadAt(buf, start); rerr != nil && rerr != io.EOF {
				return "", meta, fmt.Errorf("read resource body: %w", rerr)
			}
		}
		output, windowMeta, renderErr := renderResourceByteWindow(buf, start, size, int(byteLimit), view.Resource.FileVersion == nil || view.Resource.FileVersion.Complete)
		windowMeta.SourceDigest, windowMeta.FileVersion = meta.SourceDigest, meta.FileVersion
		return output, windowMeta, renderErr
	}
	start, count, ranged := CanonicalReadRange(offset, limit)
	if !ranged && r.defaultReadLines > 0 {
		start, count, ranged = 1, r.defaultReadLines, true
	}
	if ranged {
		if size > int64(^uint(0)>>1) {
			return "", meta, withCode(fmt.Errorf("retained body is too large to page"), "unsupported_content", RetryNone)
		}
		buf = make([]byte, size)
		if size > 0 {
			if _, rerr := lease.ReadAt(buf, 0); rerr != nil && rerr != io.EOF {
				return "", meta, fmt.Errorf("read resource body: %w", rerr)
			}
		}
		output, windowMeta, renderErr := renderResourceLineWindow(buf, start, count, int(byteLimit), size, view.Resource.FileVersion == nil || view.Resource.FileVersion.Complete)
		windowMeta.SourceDigest, windowMeta.FileVersion = meta.SourceDigest, meta.FileVersion
		return output, windowMeta, renderErr
	}
	text, consumed := boundedUTF8(buf, int(byteLimit))
	meta.ContentDigest = digestBytes([]byte(text))
	meta.Encoding = encodingInfo(buf, !truncated)
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

func renderResourceLineWindow(data []byte, start, count, byteLimit int, sourceSize int64, sourceComplete bool) (string, ResultMeta, error) {
	var meta ResultMeta
	if start < 1 || count < 1 {
		return "", meta, withCode(fmt.Errorf("invalid line window"), "invalid_arguments", RetryCorrectInput)
	}
	if len(data) == 0 && start > 1 {
		return "", meta, withCode(fmt.Errorf("read_file offset %d is past the end of retained body (0 lines)", start), "range_after_eof", RetryCorrectInput)
	}
	type line struct{ start, end int }
	lines := make([]line, 0, 64)
	lineStart := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, line{lineStart, i + 1})
			lineStart = i + 1
		}
	}
	if lineStart < len(data) {
		lines = append(lines, line{lineStart, len(data)})
	}
	if start > len(lines) {
		return "", meta, withCode(fmt.Errorf("read_file offset %d is past the end of retained body (%d lines)", start, len(lines)), "range_after_eof", RetryCorrectInput)
	}
	endLine := start + count - 1
	if endLine > len(lines) {
		endLine = len(lines)
	}
	raw := data[lines[start-1].start:lines[endLine-1].end]
	selected := raw
	lineCapped := len(selected) > byteLimit
	if lineCapped {
		selected = selected[:byteLimit]
	}
	text, consumed := boundedUTF8(selected, byteLimit)
	startByte := int64(lines[start-1].start)
	endByte := startByte + int64(consumed)
	meta.ContentDigest = digestBytes([]byte(text))
	meta.Encoding = encodingInfo(selected, false)
	meta.Coverage = Coverage{SourceComplete: sourceComplete, CaptureComplete: true, PreviewComplete: !lineCapped, ObservedBytes: int64(len(data)), RetainedBytes: int64(consumed), TotalBytes: int64Ptr(sourceSize), TotalLines: int64Ptr(int64(len(lines)))}
	if !sourceComplete {
		meta.Coverage.Reasons = append(meta.Coverage.Reasons, "upstream_incomplete")
	}
	meta.Outcome = OutcomeOK
	meta.Window = &Window{StartLine: int64(start), EndLine: int64(endLine), StartByte: startByte, EndByte: endByte, PartialLine: lineCapped}
	if endLine < len(lines) && !lineCapped {
		next := endLine + 1
		meta.Window.NextOffset = int64Ptr(int64(next))
	}
	if lineCapped {
		meta.Outcome = OutcomePartial
		meta.Coverage.CaptureComplete = false
		meta.Coverage.Reasons = []string{"line"}
		meta.Window.NextByteOffset = int64Ptr(endByte)
	}
	var header strings.Builder
	fmt.Fprintf(&header, "[read_file: retained body lines %d-%d of %d", start, endLine, len(lines))
	if meta.Window.NextOffset != nil {
		fmt.Fprintf(&header, ", next_offset=%d", *meta.Window.NextOffset)
	} else if meta.Window.NextByteOffset != nil {
		fmt.Fprintf(&header, ", next_byte_offset=%d", *meta.Window.NextByteOffset)
	} else {
		header.WriteString(", end of body")
	}
	header.WriteByte(']')
	if text == "" {
		return header.String(), meta, nil
	}
	return header.String() + "\n\n" + text, meta, nil
}

func renderResourceByteWindow(data []byte, start, sourceSize int64, byteLimit int, sourceComplete bool) (string, ResultMeta, error) {
	var meta ResultMeta
	truncated := len(data) > byteLimit
	if truncated {
		data = data[:byteLimit]
	}
	text, consumed := boundedUTF8(data, byteLimit)
	end := start + int64(consumed)
	meta.ContentDigest = digestBytes([]byte(text))
	meta.Encoding = encodingInfo(data, false)
	meta.Outcome = OutcomePartial
	meta.Coverage = Coverage{SourceComplete: sourceComplete, CaptureComplete: !truncated, PreviewComplete: !truncated, ObservedBytes: int64(len(data)), RetainedBytes: int64(consumed), TotalBytes: int64Ptr(sourceSize), Reasons: []string{"byte_window"}}
	if !sourceComplete {
		meta.Coverage.Reasons = append(meta.Coverage.Reasons, "upstream_incomplete")
	}
	meta.Window = &Window{StartByte: start, EndByte: end, PartialLine: end < sourceSize}
	if end < sourceSize {
		meta.Window.NextByteOffset = int64Ptr(end)
	}
	var header strings.Builder
	fmt.Fprintf(&header, "[read_file: retained body bytes %d-%d", start, end)
	if meta.Window.NextByteOffset != nil {
		fmt.Fprintf(&header, ", next_byte_offset=%d", *meta.Window.NextByteOffset)
	} else {
		header.WriteString(", end of body")
	}
	header.WriteByte(']')
	if text == "" {
		return header.String(), meta, nil
	}
	return header.String() + "\n\n" + text, meta, nil
}
