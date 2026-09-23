package tools

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/personalapps"
)

// This file characterizes CURRENT personal_apps error-shape behavior (Phase
// 0 of the next-generation-tool-runtime plan, evidence row L20). It adds new
// tests beside personal_apps_test.go without changing any existing
// assertion there — in particular
// TestPersonalAppsResultNeverReturnsABareGoErrorForAServedRequest already
// covers status=denied; this file exercises status=partial and
// status=unsupported, the other two domain-failure shapes the plan's Phase
// 1a proposes generalizing.

// TestPersonalAppsPartialAndUnsupportedStatusesCarryNilOuterError documents
// today's actual contract: personal_apps only ever returns a Go error
// (Result.Err) when the call could not reach the Service at all. A served
// request that the Service itself judged partial or unsupported reports that
// outcome purely inside the JSON payload — the outer error stays nil.
// PHASE 0: current gap, closed generically in Phase 1a (§L20).
func TestPersonalAppsPartialAndUnsupportedStatusesCarryNilOuterError(t *testing.T) {
	cases := []struct {
		name       string
		res        personalapps.Result
		wantSubstr string
	}{
		{
			name: "partial",
			res: personalapps.Result{
				Version: personalapps.Version, Operation: personalapps.OpMailSearch,
				Status: personalapps.StatusPartial,
			},
			wantSubstr: `"partial"`,
		},
		{
			name: "unsupported",
			res: personalapps.Result{
				Version: personalapps.Version, Operation: personalapps.OpCalendarList,
				Status: personalapps.StatusUnsupported,
				Error:  &personalapps.ResultError{Code: personalapps.CodeUnsupportedOperation, Message: "calendar companion not configured"},
			},
			wantSubstr: `"unsupported"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubPersonalApps{res: tc.res}
			r := personalAppsRunner(t, stub)
			res := r.Execute(Call{Tool: ToolPersonalApps, Body: `{"operation":"status"}`})
			if res.Err != nil {
				t.Fatalf("Execute returned an outer error %v for a served %s result; the domain outcome is meant to travel only inside the JSON payload", res.Err, tc.name)
			}
			if !strings.Contains(res.Output, tc.wantSubstr) {
				t.Fatalf("output %q does not carry the %s status", res.Output, tc.name)
			}
		})
	}
}
