package gatekeeper

import (
	"errors"
	"testing"
)

func deployRequest() DeployRequest {
	return DeployRequest{
		Target: "mac-mini control plane", Procedure: "launchctl kickstart after binary swap",
		Tag: "v0.5.0-rc.6", OwnerAuthorized: true,
	}
}

func TestDeployNeedsTheOwnerToNameTargetAndProcedure(t *testing.T) {
	decision, err := AuthorizeDeploy(deployRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed || decision.Tier != TierOwnerNamed {
		t.Fatalf("decision = %#v", decision)
	}
}

// The whole point of the deploy gate: no tier reaches it. A merge authorization,
// of any tier, authorizes nothing beyond the merge.
func TestNoTierAuthorizesADeploy(t *testing.T) {
	request := deployRequest()
	request.OwnerAuthorized = false
	decision, err := AuthorizeDeploy(request)
	if err == nil {
		t.Fatalf("deploy authorized without the owner, as tier %q", decision.Tier)
	}
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("%v does not wrap ErrRefused", err)
	}
	if decision.Allowed {
		t.Fatal("a refused deploy returned an allowed decision")
	}
}

// "Deploy something, somehow" is not a named target and a named procedure.
func TestAnUnnamedDeployIsRefused(t *testing.T) {
	for name, spoil := range map[string]func(DeployRequest) DeployRequest{
		"no target":    func(r DeployRequest) DeployRequest { r.Target = "  "; return r },
		"no procedure": func(r DeployRequest) DeployRequest { r.Procedure = ""; return r },
	} {
		if decision, err := AuthorizeDeploy(spoil(deployRequest())); err == nil {
			t.Fatalf("%s: authorized as tier %q", name, decision.Tier)
		}
	}
}

// Deploy happens by tagging a release artifact — a recorded, reversible act —
// never by an agent reaching a host directly.
func TestADeployMustPublishAReleaseArtifact(t *testing.T) {
	request := deployRequest()
	request.Tag = ""
	if decision, err := AuthorizeDeploy(request); err == nil {
		t.Fatalf("deploy without a release tag authorized as tier %q", decision.Tier)
	}
}

func TestEnablementReportsWhatIsOn(t *testing.T) {
	if got := (Enablement{}).String(); got != "no tier enabled" {
		t.Fatalf("zero enablement = %q", got)
	}
	// Owner-named needs no enablement; it is the conditions that decide it.
	if !(Enablement{}).Enables(TierOwnerNamed, "Bostonvex/machinist") {
		t.Fatal("owner-named needs enabling")
	}
	if (Enablement{}).Enables(TierGreen, "Bostonvex/machinist") {
		t.Fatal("green is on by default")
	}
	if (Enablement{}).Enables("invented-tier", "Bostonvex/machinist") {
		t.Fatal("an unrecognized tier was enabled")
	}
	// An empty repository name matches nothing, not everything.
	if reviewedEnabled().Enables(TierReviewed, "") {
		t.Fatal("reviewed matched an unnamed repository")
	}
}

func TestTierAndModeAndRiskRejectWhatTheyDoNotDefine(t *testing.T) {
	if Tier("invented").Valid() {
		t.Fatal("an invented tier is valid")
	}
	if FileMode("100644 ").Valid() || FileMode("").Valid() {
		t.Fatal("an unread mode is valid")
	}
	if Risk("risk-unknown").Valid() || Risk("").Valid() {
		t.Fatal("an unassessed risk is valid")
	}
}
