package review

import (
	"strings"
	"testing"
)

func TestStrictestKeepsTheStrongestConstraint(t *testing.T) {
	cases := []struct {
		name     string
		verdicts []Verdict
		want     Verdict
	}{
		{name: "no reviews is no judgement", verdicts: nil, want: ""},
		{name: "one review stands alone", verdicts: []Verdict{VerdictReady}, want: VerdictReady},
		{
			name:     "a later approval cannot lift an escalation",
			verdicts: []Verdict{VerdictEscalate, VerdictReady},
			want:     VerdictEscalate,
		},
		{
			name:     "changes requested outranks ready in either order",
			verdicts: []Verdict{VerdictReady, VerdictChangesRequested},
			want:     VerdictChangesRequested,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := Strictest(test.verdicts...)
			if err != nil {
				t.Fatalf("Strictest: %v", err)
			}
			if got != test.want {
				t.Fatalf("verdict = %q, want %q", got, test.want)
			}
		})
	}
}

func TestStrictestRefusesAVerdictOutsideTheContract(t *testing.T) {
	got, err := Strictest(VerdictEscalate, Verdict("approved"))
	if err == nil {
		t.Fatalf("Strictest accepted an unknown verdict and returned %q", got)
	}
	if !strings.Contains(err.Error(), "approved") {
		t.Fatalf("error %q does not name the offending verdict", err)
	}
}
