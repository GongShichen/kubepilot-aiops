package evaluation

import (
	"strings"
	"testing"
)

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

func TestRootCauseJudgePromptAllowsOnlySpecificRefinement(t *testing.T) {
	prompt := rootCauseJudgePrompt()
	for _, clause := range []string{"strictly more-specific operational subtype", "do not accept a broader diagnosis", "same service and resource"} {
		if !strings.Contains(prompt, clause) {
			t.Fatalf("semantic judge prompt lost required calibration rule %q: %s", clause, prompt)
		}
	}
}
