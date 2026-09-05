package review

import (
	"errors"
	"testing"
)

// IsReviewer is the single role-matching rule the whole tree relies on. A role
// spelled with different case or padding is still the role it names.
func TestIsReviewerRecognisesOddSpellings(t *testing.T) {
	for _, role := range []string{RoleReviewer, "Reviewer", "  REVIEWER  ", "\treviewer\n"} {
		if !IsReviewer(role) {
			t.Errorf("role %q was not recognised as a reviewer", role)
		}
	}
}

func TestIsReviewerRejectsNonReviewers(t *testing.T) {
	for _, role := range []string{RoleImplementer, "auditor", "diagnostic", ""} {
		if IsReviewer(role) {
			t.Errorf("role %q was treated as a reviewer", role)
		}
	}
}

func TestCheckIndependenceAcceptsSeparateAgents(t *testing.T) {
	author := Party{Role: RoleImplementer, Agent: "implementer_01", RunID: "run-a"}
	reviewer := Party{Role: RoleReviewer, Agent: "reviewer_02", RunID: "run-b"}
	if err := CheckIndependence(author, reviewer); err != nil {
		t.Fatalf("CheckIndependence: %v", err)
	}
}

func TestCheckIndependenceFailsClosed(t *testing.T) {
	cases := map[string]struct{ author, reviewer Party }{
		"same agent": {
			Party{Role: RoleImplementer, Agent: "agent_01", RunID: "run-a"},
			Party{Role: RoleReviewer, Agent: "Agent_01", RunID: "run-b"},
		},
		"same run": {
			Party{Role: RoleImplementer, Agent: "implementer_01", RunID: "run-a"},
			Party{Role: RoleReviewer, Agent: "reviewer_02", RunID: "run-a"},
		},
		"unknown author": {
			Party{Role: RoleImplementer, RunID: "run-a"},
			Party{Role: RoleReviewer, Agent: "reviewer_02", RunID: "run-b"},
		},
		"unknown reviewer": {
			Party{Role: RoleImplementer, Agent: "implementer_01", RunID: "run-a"},
			Party{Role: RoleReviewer, RunID: "run-b"},
		},
		"reviewer is not a reviewer": {
			Party{Role: RoleImplementer, Agent: "implementer_01", RunID: "run-a"},
			Party{Role: RoleImplementer, Agent: "implementer_02", RunID: "run-b"},
		},
		"author reviewed itself into existence": {
			Party{Role: RoleReviewer, Agent: "reviewer_01", RunID: "run-a"},
			Party{Role: RoleReviewer, Agent: "reviewer_02", RunID: "run-b"},
		},
	}
	for name, tc := range cases {
		err := CheckIndependence(tc.author, tc.reviewer)
		if !errors.Is(err, ErrNotIndependent) {
			t.Fatalf("%s: err = %v, want ErrNotIndependent", name, err)
		}
	}
}

// Independence decides the reviewer role the same way the rest of the tree
// does: a reviewer spelled with different case or padding is still recognised,
// and an implementer never is.
func TestCheckIndependenceUsesTheSharedRoleRule(t *testing.T) {
	author := Party{Role: RoleImplementer, Agent: "implementer_01", RunID: "run-a"}
	reviewer := Party{Role: "  REVIEWER  ", Agent: "reviewer_02", RunID: "run-b"}
	if err := CheckIndependence(author, reviewer); err != nil {
		t.Fatalf("oddly-spelled reviewer was refused: %v", err)
	}
	implementerSaysReview := Party{Role: RoleImplementer, Agent: "implementer_02", RunID: "run-b"}
	if err := CheckIndependence(author, implementerSaysReview); !errors.Is(err, ErrNotIndependent) {
		t.Fatalf("implementer reviewer err = %v, want ErrNotIndependent", err)
	}
}
