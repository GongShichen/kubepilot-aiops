package evaluation

import "testing"

func TestValidateVerdictRejectsCrossTargetAcceptance(t *testing.T) {
	expected := RootCause{Category: "network", Variant: "egress_deny", Service: "checkout", Resource: "checkout"}
	actual := RootCause{Category: "network", Variant: "egress_policy_block", Service: "orders", Resource: "orders"}
	if _, err := validateVerdict(RootCauseVerdict{Equivalent: true, Confidence: .9}, expected, actual); err == nil {
		t.Fatal("judge accepted a different service and resource")
	}
}

func TestValidateVerdictPreservesSameTargetSemanticVerdict(t *testing.T) {
	expected := RootCause{Category: "network", Variant: "egress_deny", Service: "checkout", Resource: "checkout"}
	actual := RootCause{Category: "network", Variant: "egress_policy_block", Service: "checkout", Resource: "checkout"}
	verdict, err := validateVerdict(RootCauseVerdict{Equivalent: true, Confidence: .9, Reason: "same mechanism"}, expected, actual)
	if err != nil || !verdict.Equivalent {
		t.Fatalf("verdict=%+v err=%v", verdict, err)
	}
}

func TestIncompleteRootCauseCannotBeJudgedEquivalent(t *testing.T) {
	if completeRootCause(RootCause{Category: "network", Service: "checkout", Resource: "checkout"}) {
		t.Fatal("incomplete root cause was accepted")
	}
}
