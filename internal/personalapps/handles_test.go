package personalapps

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeClock advances only when a test advances it.
type fakeClock struct{ t time.Time }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 6, 8, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func testRef(native string) ResourceRef {
	return ResourceRef{
		Kind:          KindMessage,
		Adapter:       AdapterMail,
		AccountID:     "acct-native-1",
		ContainerPath: []string{"INBOX", "Projects"},
		NativeID:      native,
		Fingerprint:   Fingerprint("unread=true", "mod=1"),
	}
}

func TestHandleFormat(t *testing.T) {
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	h, err := reg.Mint(testRef("m1"))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !h.WellFormed() {
		t.Fatalf("handle %q is not well formed", h)
	}
	if h.Kind() != KindMessage {
		t.Fatalf("Kind() = %q, want %q", h.Kind(), KindMessage)
	}
	if !strings.HasPrefix(string(h), "msg_") {
		t.Fatalf("handle %q lacks its kind prefix", h)
	}
	// A handle must not leak any part of the resource it points at.
	for _, secret := range []string{"acct-native-1", "INBOX", "Projects", "m1"} {
		if strings.Contains(string(h), secret) {
			t.Fatalf("handle %q leaks %q", h, secret)
		}
	}
}

func TestHandleWellFormedRejectsGarbage(t *testing.T) {
	for _, h := range []Handle{"", "msg", "msg_", "_abcdef", "xyz_0123456789abcdef01234567", "msg_zz23456789abcdef01234567", "msg_00", Handle("msg_" + strings.Repeat("a", 25))} {
		if h.WellFormed() {
			t.Errorf("Handle(%q).WellFormed() = true, want false", h)
		}
	}
}

func TestRegistryMintIsStableForSameObservation(t *testing.T) {
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	first, err := reg.Mint(testRef("m1"))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	second, err := reg.Mint(testRef("m1"))
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if first != second {
		t.Fatalf("identical observations minted %q and %q", first, second)
	}
	if got := reg.Len(); got != 1 {
		t.Fatalf("Len() = %d, want 1", got)
	}
}

func TestRegistryMintChangedFingerprintIsANewHandle(t *testing.T) {
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	ref := testRef("m1")
	first, _ := reg.Mint(ref)
	ref.Fingerprint = Fingerprint("unread=false", "mod=2")
	second, _ := reg.Mint(ref)
	if first == second {
		t.Fatal("a changed observation reused the old handle")
	}
}

func TestRegistryResolveRoundTrip(t *testing.T) {
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	want := testRef("m1")
	h, _ := reg.Mint(want)

	got, err := reg.Resolve(h)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.NativeID != want.NativeID || got.AccountID != want.AccountID || got.Fingerprint != want.Fingerprint {
		t.Fatalf("Resolve() = %+v, want %+v", got, want)
	}
	if strings.Join(got.ContainerPath, "/") != strings.Join(want.ContainerPath, "/") {
		t.Fatalf("ContainerPath = %v, want %v", got.ContainerPath, want.ContainerPath)
	}
}

func TestRegistryResolveReturnsIndependentCopy(t *testing.T) {
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	h, _ := reg.Mint(testRef("m1"))

	got, _ := reg.Resolve(h)
	got.ContainerPath[0] = "Trash"

	again, _ := reg.Resolve(h)
	if again.ContainerPath[0] != "INBOX" {
		t.Fatalf("stored ref was mutated through a returned slice: %v", again.ContainerPath)
	}
}

func TestRegistryMintCopiesCallerSlice(t *testing.T) {
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	ref := testRef("m1")
	h, _ := reg.Mint(ref)
	ref.ContainerPath[0] = "Trash"

	got, _ := reg.Resolve(h)
	if got.ContainerPath[0] != "INBOX" {
		t.Fatalf("stored ref aliased the caller's slice: %v", got.ContainerPath)
	}
}

func TestRegistryResolveUnknownAndMalformed(t *testing.T) {
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	for _, h := range []Handle{"", "msg_000000000000000000000000", "nonsense"} {
		if _, err := reg.Resolve(h); !errors.Is(err, ErrUnknownHandle) {
			t.Errorf("Resolve(%q) error = %v, want ErrUnknownHandle", h, err)
		}
	}
}

func TestRegistryResolveKindRejectsWrongClass(t *testing.T) {
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	h, _ := reg.Mint(ResourceRef{Kind: KindMailbox, Adapter: AdapterMail, AccountID: "a", ContainerPath: []string{"INBOX"}})

	if _, err := reg.ResolveKind(h, KindMailbox); err != nil {
		t.Fatalf("ResolveKind(mailbox) = %v, want nil", err)
	}
	if _, err := reg.ResolveKind(h, KindMessage); !errors.Is(err, ErrUnknownHandle) {
		t.Fatalf("ResolveKind(message) error = %v, want ErrUnknownHandle", err)
	}
}

func TestRegistryMintRejectsUnknownKind(t *testing.T) {
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	if _, err := reg.Mint(ResourceRef{Kind: "folder"}); err == nil {
		t.Fatal("Mint accepted an unknown resource kind")
	}
}

func TestRegistryTTLExpiry(t *testing.T) {
	clock := newFakeClock()
	reg := NewRegistry(RegistryOptions{TTL: time.Minute, Now: clock.Now})
	h, _ := reg.Mint(testRef("m1"))

	clock.advance(30 * time.Second)
	if _, err := reg.Resolve(h); err != nil {
		t.Fatalf("Resolve before TTL: %v", err)
	}
	// Resolving refreshed the entry, so it survives another half-window.
	clock.advance(30 * time.Second)
	if _, err := reg.Resolve(h); err != nil {
		t.Fatalf("Resolve after refresh: %v", err)
	}
	clock.advance(time.Minute)
	if _, err := reg.Resolve(h); !errors.Is(err, ErrUnknownHandle) {
		t.Fatalf("Resolve after TTL error = %v, want ErrUnknownHandle", err)
	}
}

func TestRegistryEvictsLeastRecentlyUsed(t *testing.T) {
	clock := newFakeClock()
	reg := NewRegistry(RegistryOptions{Max: 2, TTL: time.Hour, Now: clock.Now})

	first, _ := reg.Mint(testRef("m1"))
	clock.advance(time.Second)
	second, _ := reg.Mint(testRef("m2"))
	clock.advance(time.Second)
	if _, err := reg.Resolve(first); err != nil { // first is now the newest use
		t.Fatalf("Resolve(first): %v", err)
	}
	clock.advance(time.Second)
	third, _ := reg.Mint(testRef("m3"))

	if got := reg.Len(); got != 2 {
		t.Fatalf("Len() = %d, want 2", got)
	}
	if _, err := reg.Resolve(second); !errors.Is(err, ErrUnknownHandle) {
		t.Fatalf("least recently used handle survived eviction: %v", err)
	}
	for _, h := range []Handle{first, third} {
		if _, err := reg.Resolve(h); err != nil {
			t.Fatalf("Resolve(%q): %v", h, err)
		}
	}
}

func TestRegistryInvalidateAndReset(t *testing.T) {
	reg := NewRegistry(RegistryOptions{Now: newFakeClock().Now})
	one, _ := reg.Mint(testRef("m1"))
	two, _ := reg.Mint(testRef("m2"))

	reg.Invalidate(one)
	if _, err := reg.Resolve(one); !errors.Is(err, ErrUnknownHandle) {
		t.Fatalf("invalidated handle still resolves: %v", err)
	}
	if _, err := reg.Resolve(two); err != nil {
		t.Fatalf("Invalidate dropped an unrelated handle: %v", err)
	}
	// Re-minting after invalidation must issue a fresh handle.
	again, _ := reg.Mint(testRef("m1"))
	if again == one {
		t.Fatal("Mint reissued an invalidated handle")
	}

	reg.Reset()
	if got := reg.Len(); got != 0 {
		t.Fatalf("Len() after Reset = %d, want 0", got)
	}
	if _, err := reg.Resolve(two); !errors.Is(err, ErrUnknownHandle) {
		t.Fatalf("handle survived Reset: %v", err)
	}
}

func TestFingerprintIsStableAndUnambiguous(t *testing.T) {
	if Fingerprint("a", "b") != Fingerprint("a", "b") {
		t.Fatal("Fingerprint is not deterministic")
	}
	if Fingerprint("a", "b") == Fingerprint("ab") {
		t.Fatal("Fingerprint concatenates fields without a separator")
	}
	if strings.Contains(Fingerprint("secret-subject"), "secret") {
		t.Fatal("Fingerprint leaks its input")
	}
}
