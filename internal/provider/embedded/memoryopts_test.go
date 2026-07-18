package embedded

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseKVCacheType(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", KVCacheTypeF16, false},
		{"f16", KVCacheTypeF16, false},
		{" Q8_0 ", KVCacheTypeQ8_0, false},
		{"q4_0", "", true},
		{"fp32", "", true},
	}
	for _, tc := range cases {
		got, err := ParseKVCacheType(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseKVCacheType(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("ParseKVCacheType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseFlashAttention(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", FlashAttentionAuto, false},
		{"auto", FlashAttentionAuto, false},
		{"ON", FlashAttentionOn, false},
		{"off", FlashAttentionOff, false},
		{"enabled", "", true},
	}
	for _, tc := range cases {
		got, err := ParseFlashAttention(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseFlashAttention(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("ParseFlashAttention(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRuntimeFingerprintVariesWithMemoryOptions guards the cache-key
// completeness invariant: KV layout and attention-kernel settings change
// logits, so two configurations differing only in these knobs must never
// share a cached response.
func TestRuntimeFingerprintVariesWithMemoryOptions(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "m.gguf")
	if err := os.WriteFile(model, []byte("gguf"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := Options{ModelPath: model}
	fingerprint := func(opts Options) string {
		p := New("embedded", opts, func() Runtime { return nil })
		return p.RuntimeFingerprint()
	}
	baseline := fingerprint(base)

	swa := base
	swa.SWAFull = true
	q8 := base
	q8.KVCacheType = KVCacheTypeQ8_0
	fa := base
	fa.FlashAttention = FlashAttentionOff
	for name, variant := range map[string]Options{"swa_full": swa, "kv_cache_type": q8, "flash_attention": fa} {
		if fingerprint(variant) == baseline {
			t.Errorf("%s does not vary RuntimeFingerprint", name)
		}
	}

	// Explicit defaults must fingerprint identically to the zero values so a
	// config that spells out f16/auto does not needlessly invalidate caches.
	explicit := base
	explicit.KVCacheType = KVCacheTypeF16
	explicit.FlashAttention = FlashAttentionAuto
	if fingerprint(explicit) != baseline {
		t.Error("explicit f16/auto defaults changed the fingerprint")
	}

	// Spelling variants of the same setting must not split the cache.
	upper := base
	upper.KVCacheType = "Q8_0"
	if fingerprint(upper) != fingerprint(q8) {
		t.Error(`"Q8_0" and "q8_0" produced different fingerprints`)
	}
}

func TestValidateKVFlashCombination(t *testing.T) {
	if err := ValidateKVFlashCombination(KVCacheTypeQ8_0, FlashAttentionOff); err == nil {
		t.Error("q8_0 with flash_attention off must be rejected: llama.cpp deterministically refuses a quantized V cache without flash attention")
	}
	for _, fa := range []string{FlashAttentionAuto, FlashAttentionOn} {
		if err := ValidateKVFlashCombination(KVCacheTypeQ8_0, fa); err != nil {
			t.Errorf("q8_0 with flash_attention %s: unexpected error %v", fa, err)
		}
	}
	if err := ValidateKVFlashCombination(KVCacheTypeF16, FlashAttentionOff); err != nil {
		t.Errorf("f16 with flash_attention off: unexpected error %v", err)
	}
}
