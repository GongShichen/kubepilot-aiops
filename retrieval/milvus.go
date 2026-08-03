package retrieval

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strings"
	"time"
)

// MilvusStore uses Milvus' versioned REST API so the agent and benchmark do
// not depend on a platform-specific C library.
type MilvusStore struct {
	base, collection string
	dim              int
	http             *http.Client
}

func NewMilvusStore(base, collection string, dimensions int) *MilvusStore {
	if !strings.HasPrefix(base, "http") {
		base = "http://" + base
	}
	return &MilvusStore{base: strings.TrimRight(base, "/"), collection: collection, dim: dimensions, http: &http.Client{Timeout: 30 * time.Second}}
}

func (m *MilvusStore) Ensure(ctx context.Context) error {
	var listed struct {
		Data []string `json:"data"`
	}
	if err := m.call(ctx, "/v2/vectordb/collections/list", map[string]any{}, &listed); err != nil {
		return err
	}
	for _, name := range listed.Data {
		if name == m.collection {
			return nil
		}
	}
	return m.call(ctx, "/v2/vectordb/collections/create", map[string]any{"collectionName": m.collection, "dimension": m.dim, "metricType": "COSINE"}, nil)
}

func (m *MilvusStore) Upsert(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	data := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		if len(doc.Vector) != m.dim {
			return fmt.Errorf("document %s vector dimension %d, expected %d", doc.ID, len(doc.Vector), m.dim)
		}
		data = append(data, map[string]any{"id": numericID(doc.ID), "external_id": doc.ID, "service": doc.Service, "namespace": doc.Namespace, "category": doc.Category, "template": doc.Template, "root_cause": doc.RootCause, "recovery": doc.Recovery, "vector": doc.Vector})
	}
	return m.call(ctx, "/v2/vectordb/entities/upsert", map[string]any{"collectionName": m.collection, "data": data}, nil)
}

func (m *MilvusStore) Search(ctx context.Context, vector []float32, filters map[string]string, limit int) ([]Document, error) {
	if len(vector) != m.dim {
		return nil, fmt.Errorf("query vector dimension %d, expected %d", len(vector), m.dim)
	}
	var clauses []string
	for _, key := range []string{"service", "namespace", "category"} {
		if value := filters[key]; value != "" {
			clauses = append(clauses, fmt.Sprintf(`%s == %q`, key, value))
		}
	}
	body := map[string]any{"collectionName": m.collection, "data": [][]float32{vector}, "limit": limit, "outputFields": []string{"external_id", "service", "namespace", "category", "template", "root_cause", "recovery"}}
	if len(clauses) > 0 {
		body["filter"] = strings.Join(clauses, " and ")
	}
	var result struct {
		Data []struct {
			ExternalID string `json:"external_id"`
			Service    string `json:"service"`
			Namespace  string `json:"namespace"`
			Category   string `json:"category"`
			Template   string `json:"template"`
			RootCause  string `json:"root_cause"`
			Recovery   string `json:"recovery"`
		} `json:"data"`
	}
	if err := m.call(ctx, "/v2/vectordb/entities/search", body, &result); err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(result.Data))
	for _, item := range result.Data {
		out = append(out, Document{ID: item.ExternalID, Service: item.Service, Namespace: item.Namespace, Category: item.Category, Template: item.Template, RootCause: item.RootCause, Recovery: item.Recovery})
	}
	return out, nil
}

func (m *MilvusStore) call(ctx context.Context, path string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("milvus status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &envelope)
	if envelope.Code != 0 {
		return fmt.Errorf("milvus code %d: %s", envelope.Code, envelope.Message)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func numericID(value string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	sum := h.Sum(nil)
	return int64(binary.BigEndian.Uint64(sum) & 0x7fffffffffffffff)
}

var _ VectorStore = (*MilvusStore)(nil)
