package retrieval

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMilvusDropUsesExplicitCollection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/vectordb/collections/drop" {
			t.Fatalf("path=%s", request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["collectionName"] != "isolated-evaluation" {
			t.Fatalf("unexpected drop body: %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()

	store := NewMilvusStore(server.URL, "isolated-evaluation", 2)
	if err := store.Drop(t.Context()); err != nil {
		t.Fatal(err)
	}
}
