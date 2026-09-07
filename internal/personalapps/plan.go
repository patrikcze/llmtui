package personalapps

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// planIDBytes is the unguessable part of a plan identifier.
const planIDBytes = 16

const planIDPrefix = "plan_"

// validPlanID reports whether s has the shape this host issues. It says
// nothing about whether the plan exists: guessing the shape gains nothing,
// because possessing a plan ID is explicitly not authorization to apply it.
func validPlanID(s string) bool {
	rest, ok := strings.CutPrefix(s, planIDPrefix)
	if !ok || len(rest) != planIDBytes*2 {
		return false
	}
	_, err := hex.DecodeString(rest)
	return err == nil
}

// Plan is an immutable prepared change set: exactly what a human reviews and
// exactly what apply may execute. Nothing outside this package can edit one
// after preparation, and the Digest covers the changes so an approval cannot
// be carried over to a materially different plan.
type Plan struct {
	// ID is the host-issued identifier the model passes to change_apply.
	ID string
	// Digest is the canonical digest of the change set. Approval binds to
	// it, so a regenerated but equivalent plan reuses the same digest while
	// any material edit produces a different one.
	Digest string
	// Adapters lists every application the plan touches, in stable order.
	Adapters []Adapter
	// ItemCount is the number of individual items affected, counted against
	// the bulk-item budget separately from the tool-call budget.
	ItemCount int
	// CreatedAt and ExpiresAt bound the plan's life. An expired plan must be
	// prepared again against fresh state rather than applied late.
	CreatedAt time.Time
	ExpiresAt time.Time

	changes []Change
}

// Changes returns a copy of the prepared changes. The copy exists so a
// caller rendering a preview cannot mutate what apply will execute.
func (p Plan) Changes() []Change {
	return CloneChanges(p.changes)
}

// Expired reports whether the plan may no longer be applied.
func (p Plan) Expired(now time.Time) bool {
	return !p.ExpiresAt.IsZero() && !now.Before(p.ExpiresAt)
}

// ApprovalKey is the scope a human approval binds to. It names the exact
// plan digest, so approving one change set never grants any other
// personal_apps operation, and "always allow" cannot widen into a blanket
// grant over a multi-operation tool.
func (p Plan) ApprovalKey() string {
	return "personal_apps:" + string(OpChangeApply) + ":" + p.Digest
}

// Empty reports whether the plan carries no changes.
func (p Plan) Empty() bool { return len(p.changes) == 0 }

// PlanDigest is the canonical digest of a change set. It is deterministic
// across processes and independent of plan ID, preparation time and the
// order the model listed the changes in, so two genuinely equivalent
// requests produce one digest and one approval decision.
func PlanDigest(changes []Change) (string, error) {
	canon, err := canonicalChanges(changes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalChanges renders the change set to stable bytes: each change is
// marshaled through its variant (so only the fields that variant allows can
// appear) and the encoded changes are sorted.
func canonicalChanges(changes []Change) ([]byte, error) {
	encoded := make([]string, 0, len(changes))
	for i := range changes {
		b, err := json.Marshal(changes[i])
		if err != nil {
			return nil, Errorf(CodeInternal, "canonicalize change %d", i)
		}
		encoded = append(encoded, string(b))
	}
	sort.Strings(encoded)
	var b strings.Builder
	b.WriteString("personalapps/plan/v")
	b.WriteString(itoa(Version))
	b.WriteByte(0x1e)
	for _, e := range encoded {
		b.WriteString(e)
		b.WriteByte(0x1e)
	}
	return []byte(b.String()), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// PlanStoreOptions bounds a PlanStore. Zero values take the package limits.
type PlanStoreOptions struct {
	// TTL is how long a prepared plan stays applicable.
	TTL time.Duration
	// Max is the number of simultaneously prepared plans.
	Max int
	// Now supplies the current time.
	Now Clock
}

// PlanStore holds the plans prepared in this process. Plans live in memory
// only: a prepared plan is not durable state, it is a proposal that expires,
// and it must not survive a restart into a world it no longer describes.
//
// A PlanStore is safe for concurrent use.
type PlanStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	now   Clock
	plans map[string]Plan
}

// NewPlanStore returns an empty store. It performs no I/O.
func NewPlanStore(opts PlanStoreOptions) *PlanStore {
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultPlanTTL
	}
	max := opts.Max
	if max <= 0 {
		max = defaultMaxActivePlans
	}
	return &PlanStore{ttl: ttl, max: max, now: opts.Now, plans: make(map[string]Plan)}
}

// Prepare records an immutable plan for the given changes and returns it.
// It performs no external write of any kind: preparing a draft change does
// not create a draft, and preparing a move does not move anything.
func (s *PlanStore) Prepare(changes []Change) (Plan, error) {
	if len(changes) == 0 {
		return Plan{}, Errorf(CodeInvalidRequest, "a plan needs at least one change")
	}
	digest, err := PlanDigest(changes)
	if err != nil {
		return Plan{}, err
	}
	id, err := newPlanID()
	if err != nil {
		return Plan{}, err
	}

	frozen := CloneChanges(changes)

	items := 0
	adapters := make([]Adapter, 0, 2)
	for _, c := range frozen {
		items += c.ItemCount()
		if a := c.Adapter(); a != AdapterNone && !containsAdapter(adapters, a) {
			adapters = append(adapters, a)
		}
	}
	sort.Slice(adapters, func(i, j int) bool { return adapters[i] < adapters[j] })

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now.now()
	s.expireLocked(now)
	if len(s.plans) >= s.max {
		return Plan{}, Errorf(CodeRateLimited, "%d plans are already prepared; apply or discard one first", len(s.plans))
	}

	plan := Plan{
		ID:        id,
		Digest:    digest,
		Adapters:  adapters,
		ItemCount: items,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
		changes:   frozen,
	}
	s.plans[id] = plan
	return plan, nil
}

// Get returns a prepared plan without consuming it, for rendering a preview.
func (s *PlanStore) Get(id string) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now.now()
	s.expireLocked(now)
	plan, ok := s.plans[id]
	if !ok {
		return Plan{}, ErrPlanNotFound
	}
	return plan, nil
}

// Consume returns a prepared plan and removes it, so one preparation can be
// applied at most once. A retry after an uncertain outcome therefore has to
// go through a fresh preview against fresh state instead of replaying an
// approval the human gave for a different observation.
func (s *PlanStore) Consume(id string) (Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now.now()
	s.expireLocked(now)
	plan, ok := s.plans[id]
	if !ok {
		return Plan{}, ErrPlanNotFound
	}
	delete(s.plans, id)
	return plan, nil
}

// Discard drops one plan, for example when the human denies it.
func (s *PlanStore) Discard(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.plans, id)
}

// Reset drops every prepared plan. Disconnecting an adapter must call it.
func (s *PlanStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plans = make(map[string]Plan)
}

// Len reports how many plans are currently prepared.
func (s *PlanStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(s.now.now())
	return len(s.plans)
}

func (s *PlanStore) expireLocked(now time.Time) {
	for id, plan := range s.plans {
		if plan.Expired(now) {
			delete(s.plans, id)
		}
	}
}

func containsAdapter(list []Adapter, a Adapter) bool {
	for _, existing := range list {
		if existing == a {
			return true
		}
	}
	return false
}

func newPlanID() (string, error) {
	buf := make([]byte, planIDBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", Errorf(CodeInternal, "generate plan id: %v", err)
	}
	return planIDPrefix + hex.EncodeToString(buf), nil
}
