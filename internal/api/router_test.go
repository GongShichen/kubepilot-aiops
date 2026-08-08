package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/service"
	"github.com/kubepilot-aiops/kubepilot/internal/store"
)

type knowledgeFixture struct {
	rollbackRevision int
	rollbackOperator string
}

func (*knowledgeFixture) UpsertIncidentKnowledge(context.Context, *domain.Incident, domain.IncidentFeatures, string) error {
	return nil
}
func (*knowledgeFixture) SearchLexicalIncidents(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error) {
	return nil, nil
}
func (*knowledgeFixture) SearchTopologyIncidents(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error) {
	return nil, nil
}
func (*knowledgeFixture) SeedCausalPatterns(context.Context, []domain.CausalPattern) error {
	return nil
}
func (*knowledgeFixture) ListCausalPatterns(context.Context, string) ([]domain.CausalPattern, error) {
	return nil, nil
}
func (*knowledgeFixture) GetCausalPattern(_ context.Context, id string) (*domain.CausalPattern, error) {
	return &domain.CausalPattern{ID: id}, nil
}
func (*knowledgeFixture) SetCausalPatternStatus(_ context.Context, id, status, _ string) (*domain.CausalPattern, error) {
	return &domain.CausalPattern{ID: id, Status: status}, nil
}
func (fixture *knowledgeFixture) RollbackCausalPattern(_ context.Context, id string, revision int, operator string) (*domain.CausalPattern, error) {
	fixture.rollbackRevision, fixture.rollbackOperator = revision, operator
	return &domain.CausalPattern{ID: id, Version: revision}, nil
}
func (*knowledgeFixture) RecordCausalPatternEvent(context.Context, string, string, string, string, map[string]any) error {
	return nil
}
func (*knowledgeFixture) CountCausalPatternSupport(context.Context, string) (int, error) {
	return 0, nil
}

func testRouter() (*gin.Engine, *store.MemoryStore, *knowledgeFixture) {
	gin.SetMode(gin.TestMode)
	memoryStore := store.NewMemoryStore()
	knowledge := &knowledgeFixture{}
	manager := &service.IncidentManager{Store: memoryStore, Hub: service.NewHub(), AllowedNamespaces: []string{"kubepilot-demo"}}
	server := &Server{
		Manager: manager, APIToken: "token", WebhookToken: "webhook", Knowledge: knowledge,
		ModelHealth: func() map[string]any { return map[string]any{"configured": true} },
		ModelProbe:  func(*gin.Context) error { return nil },
	}
	return server.Router(), memoryStore, knowledge
}

func perform(router http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer token")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func TestIncidentAPIExposesCanonicalInvestigationLedger(t *testing.T) {
	router, memoryStore, _ := testRouter()
	response := perform(router, http.MethodPost, "/api/v1/incidents", `{"service":"payment","namespace":"kubepilot-demo","summary":"latency","diagnosis_method":"kubepilot"}`, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var incident domain.Incident
	if err := json.Unmarshal(response.Body.Bytes(), &incident); err != nil {
		t.Fatal(err)
	}
	if incident.DiagnosisMethod != domain.DiagnosisMethodKubePilot || incident.CausalMode != domain.CausalModeFull {
		t.Fatalf("canonical strategy was not persisted: %+v", incident)
	}
	incident.Investigation = &domain.Investigation{
		Architecture:    "eino-native-self-reflective-brain",
		Plan:            domain.InvestigationPlan{Objective: "diagnose"},
		AgentHypotheses: []domain.AgentHypothesis{{ID: "h1", Statement: "dependency unavailable"}},
		ModelUsage:      []domain.ModelUsageEvent{{Agent: "brain_model_reasoning", InputTokens: 10, OutputTokens: 5, EstimatedCost: .01}},
	}
	if err := memoryStore.Update(context.Background(), &incident); err != nil {
		t.Fatal(err)
	}
	response = perform(router, http.MethodGet, "/api/v1/incidents/"+incident.ID+"/investigation", "", nil)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"agent_hypotheses"`)) || bytes.Contains(response.Body.Bytes(), []byte("chain_of_thought")) {
		t.Fatalf("investigation response=%d %s", response.Code, response.Body.String())
	}
	response = perform(router, http.MethodGet, "/api/v1/incidents/"+incident.ID+"/agent-runs", "", nil)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"input_tokens":10`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"strategy":"kubepilot"`)) || !bytes.Contains(response.Body.Bytes(), []byte(domain.BrainWorkflowRuntimeName)) {
		t.Fatalf("agent run ledger response=%d %s", response.Code, response.Body.String())
	}
}

func TestIncidentAPIRejectsRemovedLegacyDiagnosisMethods(t *testing.T) {
	router, _, _ := testRouter()
	for _, method := range []string{"direct", "rag", "react", "rule-only", "evidence-only", "cognitive", "active-diagnosis"} {
		response := perform(router, http.MethodPost, "/api/v1/incidents", fmt.Sprintf(`{"service":"payment","namespace":"kubepilot-demo","summary":"latency","diagnosis_method":%q}`, method), nil)
		if response.Code == http.StatusOK {
			t.Fatalf("removed legacy diagnosis method %q was accepted", method)
		}
	}
}

func TestCausalRollbackAPIRequiresOperatorAndDelegatesExactRevision(t *testing.T) {
	router, _, knowledge := testRouter()
	response := perform(router, http.MethodPost, "/api/v1/knowledge/causal-patterns/pattern/rollback", `{"revision":2}`, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("rollback without operator status=%d body=%s", response.Code, response.Body.String())
	}
	response = perform(router, http.MethodPost, "/api/v1/knowledge/causal-patterns/pattern/rollback", `{"revision":2}`, map[string]string{"X-Operator": "sre@example.test"})
	if response.Code != http.StatusOK || knowledge.rollbackRevision != 2 || knowledge.rollbackOperator != "sre@example.test" {
		t.Fatalf("rollback delegation status=%d revision=%d operator=%q body=%s", response.Code, knowledge.rollbackRevision, knowledge.rollbackOperator, response.Body.String())
	}
}

func TestReadinessAndAuthenticationBoundaries(t *testing.T) {
	router, _, _ := testRouter()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("default readiness status=%d body=%s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/incidents", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request status=%d", response.Code)
	}
}
