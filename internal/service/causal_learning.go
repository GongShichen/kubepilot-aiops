package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	causaldiscovery "github.com/kubepilot-aiops/kubepilot/internal/causal/discovery"
	causalextractor "github.com/kubepilot-aiops/kubepilot/internal/causal/extractor"
	causalknowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	causalvalidator "github.com/kubepilot-aiops/kubepilot/internal/causal/validator"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/store"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
	topologyextractor "github.com/kubepilot-aiops/kubepilot/internal/topology/extractor"
	topologyknowledge "github.com/kubepilot-aiops/kubepilot/internal/topology/knowledge"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
)

type CausalLearner struct {
	Store               store.KnowledgeStore
	ConfidenceThreshold float64
	Namespaces          []string
	EmbeddingVersion    string
	Embedder            retrieval.EmbeddingClient
	Vectors             retrieval.VectorStore
	TopologyPatterns    topologyknowledge.PatternStore
	CausalPatterns      causalknowledge.PatternStore
	Discovery           *causaldiscovery.Engine
	IncidentHistory     ResolvedIncidentReader
}

// ResolvedIncidentReader is intentionally separate from the Agent capability
// interfaces. Only the server-side learning path can enumerate historical
// incidents for discovery; Diagnosis receives a read-only candidate Reader.
type ResolvedIncidentReader interface {
	ListResolvedIncidents(context.Context, []string, int) ([]*domain.Incident, error)
}

func (l CausalLearner) WriteVerifiedIncident(ctx context.Context, input domain.IncidentLearningInput) error {
	return l.Learn(ctx, input.Incident)
}

func (l CausalLearner) Learn(ctx context.Context, in *domain.Incident) error {
	if l.Store == nil || in == nil {
		return nil
	}
	if !l.namespaceAllowed(in.Namespace) || in.Namespace == "kubepilot-benchmark" || evaluationIncident(in) {
		return nil
	}
	threshold := l.ConfidenceThreshold
	if threshold <= 0 {
		threshold = .90
	}
	if in.Status != domain.StatusResolved || in.Confidence < threshold || in.DiagnosisError != "" {
		return nil
	}
	if in.ExecutionContext == nil || in.ExecutionContext.ApprovalID == "" || in.Proposal == nil || in.Verification == nil || !in.Verification.Success {
		return nil
	}
	passed := 0
	for _, ok := range in.Verification.Checks {
		if ok {
			passed++
		}
	}
	if passed < 3 {
		return nil
	}
	sources := map[string]bool{}
	ids := map[string]bool{}
	for _, id := range in.RootCauseEvidenceIDs {
		ids[id] = true
	}
	for _, e := range in.Evidence {
		if ids[e.ID] {
			sources[e.Source] = true
		}
	}
	if len(sources) < 2 {
		return nil
	}
	ledger := in.DiagnosisLedger
	if ledger == nil || len(ledger.InfrastructureErrors) > 0 {
		return nil
	}
	var selected *domain.VerifiedHypothesis
	for i := range ledger.Verified {
		if ledger.Verified[i].Draft.ID == ledger.SelectedHypothesisID {
			selected = &ledger.Verified[i]
			break
		}
	}
	if selected == nil || selected.ContradictionScore > .10 {
		return nil
	}
	// Knowledge evolution is deliberately outside the Agent runtime. Agents
	// can read patterns and submit proposals, but only this resolved-Incident
	// service path can extract, validate and merge them.
	if l.TopologyPatterns != nil {
		graph := topology.Build(in, in.Evidence)
		if in.DiagnosisLedger != nil && len(in.DiagnosisLedger.Candidates) > 0 {
			observed := in.DiagnosisLedger.Candidates[0].Features.TopologyGraph
			if len(observed.Nodes) > 0 || len(observed.Edges) > 0 {
				graph = topology.Merge(graph, topology.FromDependencyGraph(in.ID, observed))
			}
		}
		if _, err := topologyextractor.Merge(ctx, in, graph, l.TopologyPatterns); err != nil {
			return fmt.Errorf("evolve topology knowledge: %w", err)
		}
	}
	managedCausalKnowledge := l.CausalPatterns != nil
	if l.CausalPatterns != nil {
		proposal, ok := causalextractor.Propose(in)
		if ok {
			validator := causalvalidator.New(l.CausalPatterns)
			validation, err := validator.Validate(ctx, in, proposal)
			if err != nil {
				return fmt.Errorf("validate causal knowledge proposal: %w", err)
			}
			if validation.Valid {
				proposal.Pattern.Confidence = validation.Confidence
				proposal.Pattern.Status = "candidate"
				merged, mergeErr := l.CausalPatterns.Merge(ctx, proposal.Pattern)
				if mergeErr != nil {
					return fmt.Errorf("evolve causal knowledge: %w", mergeErr)
				}
				if err := l.Store.RecordCausalPatternEvent(ctx, merged.ID, in.ID, "incident_support", "resolved incident met causal knowledge gates", map[string]any{"confidence": merged.Confidence, "support_count": merged.SupportCount, "status": merged.Status}); err != nil {
					return err
				}
			}
		}
	}
	if l.Discovery != nil && l.IncidentHistory != nil {
		resolved, err := l.IncidentHistory.ListResolvedIncidents(ctx, l.Namespaces, 500)
		if err != nil {
			return fmt.Errorf("load resolved incidents for causal discovery: %w", err)
		}
		if _, err := l.Discovery.Discover(ctx, resolved); err != nil {
			return fmt.Errorf("discover causal patterns: %w", err)
		}
	}
	features := featuresFromLedger(in)
	if err := l.Store.UpsertIncidentKnowledge(ctx, in, features, l.EmbeddingVersion); err != nil {
		return err
	}
	if l.Embedder != nil && l.Vectors != nil {
		text := strings.Join(append([]string{in.Summary, in.RootCause, in.RootCauseCategory, in.RootCauseVariant, in.Service, in.Resource}, features.Terms...), " ")
		vectors, embedErr := l.Embedder.Embed(ctx, []string{text})
		if embedErr != nil {
			return fmt.Errorf("index learned incident embedding: %w", embedErr)
		}
		if len(vectors) != 1 {
			return fmt.Errorf("index learned incident embedding: expected one vector, got %d", len(vectors))
		}
		recovery := ""
		if in.Proposal != nil {
			recovery = string(in.Proposal.Action)
		}
		if vectorErr := l.Vectors.Upsert(ctx, []retrieval.Document{{ID: in.ID, Cluster: in.Cluster, Namespace: in.Namespace, Service: in.Service, Category: in.RootCauseCategory, Template: in.Summary, RootCause: in.RootCause, Recovery: recovery, Vector: vectors[0]}}); vectorErr != nil {
			return fmt.Errorf("index learned incident vector: %w", vectorErr)
		}
	}
	if managedCausalKnowledge {
		return nil
	}
	pattern := selectLearnedPattern(in, ledger, selected)
	pattern.Status = "candidate"
	if err := l.Store.SeedCausalPatterns(ctx, []domain.CausalPattern{pattern}); err != nil {
		return err
	}
	if err := l.Store.RecordCausalPatternEvent(ctx, pattern.ID, in.ID, "incident_support", "resolved incident met high-confidence causal learning gates", map[string]any{"confidence": in.Confidence, "evidence_sources": len(sources), "contradiction_score": selected.ContradictionScore}); err != nil {
		return err
	}
	count, err := l.Store.CountCausalPatternSupport(ctx, pattern.ID)
	if err != nil {
		return err
	}
	current, err := l.Store.GetCausalPattern(ctx, pattern.ID)
	if err != nil {
		return err
	}
	if count < 3 {
		if current.Status == "candidate" {
			if _, err = l.Store.SetCausalPatternStatus(ctx, pattern.ID, "validating", "causal-pattern-learner"); err != nil {
				return err
			}
			return l.Store.RecordCausalPatternEvent(ctx, pattern.ID, in.ID, "validation_started", "first qualified production incident moved the candidate into validation", map[string]any{"support_count": count})
		}
		return nil
	}
	if current.Status != "active" {
		_, err = l.Store.SetCausalPatternStatus(ctx, pattern.ID, "active", "causal-auto-learner")
		if err != nil {
			return err
		}
		return l.Store.RecordCausalPatternEvent(ctx, pattern.ID, in.ID, "auto_activated", fmt.Sprintf("%d independent resolved incidents support the normalized pattern", count), map[string]any{"support_count": count})
	}
	return nil
}

func (l CausalLearner) namespaceAllowed(namespace string) bool {
	for _, allowed := range l.Namespaces {
		if namespace == allowed {
			return true
		}
	}
	return false
}
func evaluationIncident(in *domain.Incident) bool {
	for _, alert := range in.Alerts {
		for _, key := range []string{"evaluation", "benchmark", "kubepilot.io/evaluation"} {
			if strings.EqualFold(alert.Labels[key], "true") || alert.Labels[key] == "1" {
				return true
			}
		}
	}
	return false
}
func featuresFromLedger(in *domain.Incident) domain.IncidentFeatures {
	features := domain.IncidentFeatures{IncidentID: in.ID, Cluster: in.Cluster, Namespace: in.Namespace, Service: in.Service, Resource: in.Resource}
	if in.DiagnosisLedger != nil && len(in.DiagnosisLedger.Candidates) > 0 {
		features.TopologyServices = append(features.TopologyServices, in.DiagnosisLedger.Candidates[0].Features.TopologyServices...)
	}
	for _, e := range in.Evidence {
		features.EvidenceTypes = append(features.EvidenceTypes, e.Type)
		if e.Service != "" {
			features.TopologyServices = append(features.TopologyServices, e.Service)
		}
		features.Terms = append(features.Terms, strings.Fields(strings.ToLower(e.Summary))...)
		features.CausalNodeIDs = append(features.CausalNodeIDs, e.CausalNodeIDs...)
	}
	return features
}
func selectLearnedPattern(in *domain.Incident, ledger *domain.DiagnosisLedger, selected *domain.VerifiedHypothesis) domain.CausalPattern {
	for _, pattern := range ledger.CausalPatterns {
		if pattern.Category == selected.Draft.Category {
			pattern.Cluster = in.Cluster
			pattern.Namespace = in.Namespace
			return pattern
		}
	}
	normalized := strings.ToLower(in.Cluster + "|" + in.Namespace + "|" + selected.Draft.Category + "|" + strings.Join(selected.Draft.ExpectedCausalPath, "->"))
	sum := sha256.Sum256([]byte(normalized))
	nodes := make([]domain.CausalNode, 0, len(selected.Draft.ExpectedCausalPath))
	edges := make([]domain.CausalEdge, 0, len(selected.Draft.ExpectedCausalPath)-1)
	for i, node := range selected.Draft.ExpectedCausalPath {
		id := fmt.Sprintf("node_%d", i)
		typeName := "mechanism"
		if i == 0 {
			typeName = "cause"
		} else if i == len(selected.Draft.ExpectedCausalPath)-1 {
			typeName = "symptom"
		}
		nodes = append(nodes, domain.CausalNode{ID: id, Type: typeName, Name: node, Match: []string{node}, Confidence: in.Confidence, SourceEvidenceIDs: append([]string(nil), selected.VerifiedEvidenceIDs...)})
		if i > 0 {
			relation := "causes"
			if i == len(selected.Draft.ExpectedCausalPath)-1 {
				relation = "manifests_as"
			}
			edges = append(edges, domain.CausalEdge{From: fmt.Sprintf("node_%d", i-1), To: id, Relation: relation, Confidence: in.Confidence})
		}
	}
	return domain.CausalPattern{ID: "learned-" + hex.EncodeToString(sum[:6]), Category: selected.Draft.Category, Cause: selected.Draft.Cause, Nodes: nodes, Edges: edges, Cluster: in.Cluster, Namespace: in.Namespace, Source: "learned", Confidence: in.Confidence, Status: "candidate", Version: 1}
}
