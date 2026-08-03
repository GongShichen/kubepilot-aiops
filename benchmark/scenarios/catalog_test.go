package scenarios

import "testing"

func TestCatalog(t *testing.T) {
	_, items, _, err := Load("../incidents.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 100 {
		t.Fatalf("got %d", len(items))
	}
}
