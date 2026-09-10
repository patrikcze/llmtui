package provider

import (
	"errors"
	"strings"
	"testing"
)

func TestResolveModelProtocolGPTOSS(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, model, architecture string
		want                      bool
	}{
		{name: "openai id", model: "openai/gpt-oss-20b", want: true},
		{name: "ollama id", model: "gpt-oss:20b", want: true},
		{name: "gguf path", model: "/models/gpt-oss-20b-MXFP4.gguf", want: true},
		{name: "architecture wins", model: "custom.gguf", architecture: "gpt-oss", want: true},
		{name: "not substring", model: "mygpt-ossified-model", want: false},
		{name: "ordinary model", model: "qwen3:8b", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveModelProtocol(test.model, test.architecture)
			if (got.Family == ModelFamilyGPTOSS) != test.want {
				t.Fatalf("ResolveModelProtocol(%q, %q) = %+v", test.model, test.architecture, got)
			}
		})
	}
}

func TestGPTOSSReasoningEffort(t *testing.T) {
	t.Parallel()
	protocol := ResolveModelProtocol("gpt-oss:20b", "")
	for _, test := range []struct {
		configured, want string
		ok               bool
	}{
		{"", "medium", true}, {"auto", "medium", true}, {"on", "medium", true},
		{"low", "low", true}, {"medium", "medium", true}, {"high", "high", true},
		{"off", "", false}, {"max", "", false},
	} {
		got, ok := ReasoningEffort(protocol, test.configured)
		if got != test.want || ok != test.ok {
			t.Errorf("ReasoningEffort(%q) = %q, %v; want %q, %v", test.configured, got, ok, test.want, test.ok)
		}
	}
}

func TestContainsHarmonyControlToken(t *testing.T) {
	t.Parallel()
	if !ContainsHarmonyControlToken("answer<|channel|>analysis") {
		t.Fatal("expected Harmony control token to be rejected")
	}
	if ContainsHarmonyControlToken("ordinary <angle> text") {
		t.Fatal("ordinary text was rejected")
	}
}

// feedGuard pushes every delta through the guard and returns the
// concatenated emitted text, failing the test immediately if Feed rejects
// the stream.
func feedGuard(t *testing.T, g *HarmonyContentGuard, deltas ...string) string {
	t.Helper()
	var out strings.Builder
	for _, delta := range deltas {
		emitted, err := g.Feed(delta)
		if err != nil {
			t.Fatalf("Feed(%q) returned %v, want no error", delta, err)
		}
		out.WriteString(emitted)
	}
	return out.String()
}

func TestHarmonyContentGuardRejectsRecipientMarker(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		deltas []string
	}{
		{"known dotted form", []string{"to=functions.read_file"}},
		{"space-separated form", []string{"to=functions grep..."}},
		{"stripped remnant", []string{"to=...?"}},
		// This is the shape actually observed from LM Studio serving
		// gpt-oss: the recipient name is corrupted into unrelated bytes by
		// a decode failure, but the "to=" marker itself survives intact.
		// The guard must reject it on the marker, not on a catalog of
		// previously observed corruptions.
		{"arbitrary corruption after the marker", []string{"to=..??…?????…???—???……"}},
		{"leading whitespace before the marker", []string{"  \n", "to=functions.x"}},
		{"marker split across stream chunks", []string{"t", "o", "=functions.x"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var g HarmonyContentGuard
			var err error
			for _, delta := range test.deltas {
				var emitted string
				emitted, err = g.Feed(delta)
				if err != nil {
					break
				}
				if emitted != "" {
					t.Fatalf("Feed(%q) emitted %q before the marker was rejected", delta, emitted)
				}
			}
			if err == nil {
				_, err = g.Finish()
			}
			if !errors.Is(err, ErrHarmonyProtocol) {
				t.Fatalf("got err = %v, want ErrHarmonyProtocol", err)
			}
		})
	}
}

func TestHarmonyContentGuardPassesOrdinaryContent(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		deltas []string
	}{
		{"plain sentence", []string{"The event was created."}},
		// Shares a prefix with the marker but diverges before it completes;
		// must not be held back or rejected.
		{"prefix of the marker that diverges", []string{"together, we can plan this."}},
		{"marker-length prefix split across chunks", []string{"t", "o", "day is a good day"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var g HarmonyContentGuard
			var got strings.Builder
			got.WriteString(feedGuard(t, &g, test.deltas...))
			tail, err := g.Finish()
			if err != nil {
				t.Fatalf("Finish() returned %v, want no error", err)
			}
			got.WriteString(tail)
			want := strings.Join(test.deltas, "")
			if got.String() != want {
				t.Fatalf("got %q, want %q", got.String(), want)
			}
		})
	}
}

// TestHarmonyContentGuardFlushesUnresolvedPrefixAtFinish covers a turn whose
// entire visible content is a strict prefix of the marker (e.g. the model's
// whole answer is the word "to") with nothing more ever arriving. The
// marker never completes, so Finish must release it as ordinary content
// instead of treating an unresolved prefix as a confirmed violation — a
// real, no-arguments tool-call-only turn (see
// TestGPTOSSResponseKeepsThinkingPrivate in the ollama package) hits this
// same path with empty content.
func TestHarmonyContentGuardFlushesUnresolvedPrefixAtFinish(t *testing.T) {
	t.Parallel()
	var g HarmonyContentGuard
	emitted, err := g.Feed("to")
	if err != nil || emitted != "" {
		t.Fatalf("Feed(%q) = (%q, %v), want (\"\", nil) while still ambiguous", "to", emitted, err)
	}
	tail, err := g.Finish()
	if err != nil {
		t.Fatalf("Finish() = %v, want no error for an unresolved marker prefix", err)
	}
	if tail != "to" {
		t.Fatalf("Finish() = %q, want the held-back %q flushed through", tail, "to")
	}
}
