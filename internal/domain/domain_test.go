package domain

import "testing"

func TestWorkspaceKey(t *testing.T) {
	cases := map[string]string{
		"ABC-123":   "ABC-123",
		"AB/C 1":    "AB_C_1",
		"a.b_c-d":   "a.b_c-d",
		"weird*&^%": "weird____",
	}
	for in, want := range cases {
		if got := WorkspaceKey(in); got != want {
			t.Errorf("WorkspaceKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeState(t *testing.T) {
	if NormalizeState("  In Progress ") != "in progress" {
		t.Errorf("got %q", NormalizeState("  In Progress "))
	}
}
