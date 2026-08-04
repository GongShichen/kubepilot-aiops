package reasoning

import "testing"

func TestEngineImplementsCandidateRerankerContract(t *testing.T) {
	var reranker CandidateReranker = New(DefaultConfig())
	if reranker == nil {
		t.Fatal("deterministic candidate reranker is not registered")
	}
}
