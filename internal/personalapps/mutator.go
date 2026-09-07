package personalapps

import "context"

// combinedMutator dispatches to the Mail or Calendar mutator based on which
// adapter a resolved change belongs to. Either half may be nil — a
// deployment can wire mutations for one adapter without the other — in
// which case a change for the unwired adapter reports unsupported rather
// than reaching a nil pointer.
type combinedMutator struct {
	mail     Mutator
	calendar Mutator
}

// NewMutator combines a Mail and a Calendar mutator behind one Mutator,
// dispatching by which adapter a resolved change belongs to (Change.
// Adapter()). Either argument may be nil; if both are nil, NewMutator
// itself returns nil, so a deployment that wires no mutator at all gets
// exactly today's ErrUnsupportedPlatform behavior at Service.changeApply,
// not a non-nil Mutator that fails every call.
func NewMutator(mail, calendar Mutator) Mutator {
	if mail == nil && calendar == nil {
		return nil
	}
	return &combinedMutator{mail: mail, calendar: calendar}
}

func (c *combinedMutator) Apply(ctx context.Context, rc ResolvedChange) ([]ItemOutcome, error) {
	switch rc.Change.Adapter() {
	case AdapterMail:
		if c.mail == nil {
			return nil, ErrUnsupportedPlatform
		}
		return c.mail.Apply(ctx, rc)
	case AdapterCalendar:
		if c.calendar == nil {
			return nil, ErrUnsupportedPlatform
		}
		return c.calendar.Apply(ctx, rc)
	default:
		return nil, Errorf(CodeInternal, "change has no adapter")
	}
}
