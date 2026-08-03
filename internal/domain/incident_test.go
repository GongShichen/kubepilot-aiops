package domain

import "testing"

func TestStateTransitions(t *testing.T) {
	if !CanTransition(StatusReceived, StatusCorrelating) {
		t.Fatal("expected transition")
	}
	if CanTransition(StatusReceived, StatusResolved) {
		t.Fatal("unexpected transition")
	}
}
