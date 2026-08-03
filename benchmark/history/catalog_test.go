package history

import (
	"strings"
	"testing"
)

func TestLoadHeldOutCatalog(t *testing.T) {
	_, docs, _, err := Load("../history.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 63 {
		t.Fatalf("got %d documents, want 63", len(docs))
	}
	for _, doc := range docs {
		if !strings.HasPrefix(doc.Document.ID, "history-") {
			t.Fatalf("non-history identifier leaked into corpus: %s", doc.Document.ID)
		}
		if strings.Contains(doc.Text, "benchmark case") || strings.Contains(doc.Text, "injector") {
			t.Fatalf("benchmark control data leaked into %s", doc.Document.ID)
		}
	}
}
