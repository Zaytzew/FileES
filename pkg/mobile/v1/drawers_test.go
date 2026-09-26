package v1

import (
	"encoding/json"
	"testing"
)

func TestListDrawersEmptyFrameIsValid(t *testing.T) {
	if err := (ListDrawersResult{}).Validate(); err != nil {
		t.Fatal(err)
	}
	ok, err := NewSuccess(rid(), OpListDrawers, ListDrawersResult{Drawers: []DrawerSummary{}, Assignments: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ok)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseResponse(raw); err != nil {
		t.Fatal(err)
	}
}

func TestListDrawersRejectsAssignmentWithoutDrawer(t *testing.T) {
	err := (ListDrawersResult{Assignments: map[string]string{"repo-1": "missing"}}).Validate()
	if err == nil {
		t.Fatal("assignment to an unknown drawer must fail")
	}
}
