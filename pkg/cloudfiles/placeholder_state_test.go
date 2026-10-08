package cloudfiles

import "testing"

// These states follow the CF_PLACEHOLDER_STATE contract, independently of a
// Windows build or provider. PARTIAL alone must not strand a hydrated file.
func TestContentMissingDistinguishesReadinessFromLocalBytes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state uint32
		want  bool
	}{
		{"ordinary", 0, false},
		{"sync root", statePlaceholder | stateSyncRoot, false},
		{"hydrated placeholder", statePlaceholder, false},
		{"hydrated awaiting validation", statePlaceholder | statePartial, false},
		{"hydrated in sync with partial readiness", statePlaceholder | statePartial | 0x08, false},
		{"missing bytes", statePlaceholder | statePartial | statePartiallyOnDisk, true},
		{"missing bytes while in sync", statePlaceholder | statePartial | statePartiallyOnDisk | 0x08, true},
		{"missing bytes without readiness bit", statePlaceholder | statePartiallyOnDisk, true},
		{"not a placeholder", statePartial | statePartiallyOnDisk, false},
		{"invalid observation", stateInvalid, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := contentMissing(tc.state); got != tc.want {
				t.Fatalf("contentMissing(%#x) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}
