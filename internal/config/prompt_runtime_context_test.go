package config

import "testing"

func TestFreshRuntimeContextAsMessage(t *testing.T) {
	for _, tc := range []struct {
		value    string
		embedded bool
		want     bool
	}{
		{value: "", embedded: true, want: true},
		{value: "", embedded: false, want: false},
		{value: FreshRuntimeContextAuto, embedded: true, want: true},
		{value: FreshRuntimeContextAuto, embedded: false, want: false},
		{value: FreshRuntimeContextMessage, embedded: false, want: true},
		{value: " Message ", embedded: false, want: true},
		{value: FreshRuntimeContextSystem, embedded: true, want: false},
		{value: "typo", embedded: true, want: true},
		{value: "typo", embedded: false, want: false},
	} {
		got := PromptConfig{FreshRuntimeContext: tc.value}.FreshRuntimeContextAsMessage(tc.embedded)
		if got != tc.want {
			t.Errorf("FreshRuntimeContext=%q embedded=%t: got %t, want %t", tc.value, tc.embedded, got, tc.want)
		}
	}
}
