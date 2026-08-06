package scenarios

import "testing"

func TestCatalog(t *testing.T) {
	_, items, _, err := Load("../incidents.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 120 {
		t.Fatalf("got %d", len(items))
	}
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Split]++
	}
	if counts["dev"] != 24 || counts["validation"] != 24 || counts["test"] != 72 {
		t.Fatalf("unexpected split counts: %v", counts)
	}
}
