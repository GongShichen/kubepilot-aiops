package retrieval

import (
	"testing"
	"time"
)

func TestProtocolExample(t *testing.T) {
	p := NewWSParser("ws://example.invalid", "token")
	b, err := p.MarshalProtocolExample()
	if err != nil || len(b) == 0 {
		t.Fatal(err)
	}
	_ = time.Second
}

func TestParserStatsStartAtZero(t *testing.T) {
	p := NewWSParser("ws://example.invalid", "token")
	if stats := p.Stats(); stats != (ParserStats{}) {
		t.Fatalf("stats=%#v", stats)
	}
}
