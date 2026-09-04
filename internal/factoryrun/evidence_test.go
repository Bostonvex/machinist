package factoryrun

import "testing"

func TestValidateRequiresIdentity(t *testing.T) {
	if err := (Evidence{}).Validate(); err == nil {
		t.Fatal("expected validation error for empty evidence")
	}
	e := Evidence{JobID: "j", RunID: "r", Repo: "o/r"}
	if err := e.Validate(); err != nil {
		t.Fatalf("valid evidence rejected: %v", err)
	}
}
