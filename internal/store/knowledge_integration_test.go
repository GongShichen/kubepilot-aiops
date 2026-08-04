package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/oklog/ulid/v2"
)

func TestPostgresLexicalAndCrossServiceTopologyRetrieval(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	database, err := NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	namespace := "knowledge-test-" + ulid.Make().String()
	incident := &domain.Incident{ID: ulid.Make().String(), Status: domain.StatusResolved, Severity: "warning", Namespace: namespace, Service: "payment-service", Resource: "deployment/payment-service", Summary: "MySQL connection refused", RootCauseCategory: "database", RootCause: "shared MySQL endpoint unavailable", CreatedAt: time.Now().Add(-time.Minute), UpdatedAt: time.Now()}
	if err = database.Create(ctx, incident); err != nil {
		t.Fatal(err)
	}
	defer database.pool.Exec(context.Background(), `DELETE FROM incidents WHERE id=$1`, incident.ID) //nolint:errcheck
	historicalFeatures := domain.IncidentFeatures{IncidentID: incident.ID, Namespace: namespace, Service: incident.Service, Resource: incident.Resource, Terms: []string{"mysql", "connection", "refused"}, TopologyServices: []string{"payment-service", "mysql"}, TopologyGraph: domain.IncidentDependencyGraph{RootService: "payment-service", Nodes: []domain.DependencyNode{{ID: "payment-service", Role: "root"}, {ID: "mysql", Role: "critical_dependency"}}, Edges: []domain.DependencyEdge{{From: "payment-service", To: "mysql", Kind: "observed_call"}}, SuspectedFailureNodes: []string{"mysql"}, ErrorPropagationPaths: [][]string{{"payment-service", "mysql"}}}}
	if err = database.UpsertIncidentKnowledge(ctx, incident, historicalFeatures, "integration-test"); err != nil {
		t.Fatal(err)
	}
	lexicalQuery := domain.IncidentFeatures{Namespace: namespace, Service: "order-service", Resource: "deployment/order-service", Terms: []string{"mysql", "connection", "refused"}, TopologyServices: []string{"order-service", "mysql"}}
	topologyQuery := domain.IncidentFeatures{Namespace: "different-namespace", Service: "order-service", Resource: "deployment/order-service", Terms: []string{"mysql", "connection", "refused"}, TopologyServices: []string{"order-service", "mysql"}, TopologyGraph: domain.IncidentDependencyGraph{RootService: "order-service", Nodes: []domain.DependencyNode{{ID: "order-service", Role: "root"}, {ID: "mysql", Role: "critical_dependency"}}, Edges: []domain.DependencyEdge{{From: "order-service", To: "mysql", Kind: "observed_call"}}, SuspectedFailureNodes: []string{"mysql"}, ErrorPropagationPaths: [][]string{{"order-service", "mysql"}}}}
	lexical, err := database.SearchLexicalIncidents(ctx, lexicalQuery, 50)
	if err != nil || len(lexical) != 1 || lexical[0].IncidentID != incident.ID {
		t.Fatalf("lexical=%#v err=%v", lexical, err)
	}
	topology, err := database.SearchTopologyIncidents(ctx, topologyQuery, 50)
	if err != nil || len(topology) != 1 || topology[0].IncidentID != incident.ID || topology[0].Service == topologyQuery.Service || topology[0].SourceScores["topology"] <= 0 {
		t.Fatalf("cross-service topology=%#v err=%v", topology, err)
	}
}
