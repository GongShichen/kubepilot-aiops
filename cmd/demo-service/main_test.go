package main

import "testing"

func TestAllocateTouchedCommitsEveryPage(t *testing.T) {
	block := allocateTouched(8193)
	if len(block) != 8193 || block[0] == 0 || block[4096] == 0 || block[8192] == 0 {
		t.Fatalf("allocation pages were not touched: len=%d samples=%v", len(block), []byte{block[0], block[4096], block[8192]})
	}
}
