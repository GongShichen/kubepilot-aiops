package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/service"
	"github.com/kubepilot-aiops/kubepilot/internal/store"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

//go:embed web/*
var webFS embed.FS

type Server struct {
	Manager                *service.IncidentManager
	Benchmarks             *service.BenchmarkManager
	APIToken, WebhookToken string
	ModelHealth            func() map[string]any
	ModelProbe             func(*gin.Context) error
	RerankerHealth         func() map[string]any
	RerankerProbe          func(*gin.Context) error
	Knowledge              store.KnowledgeStore
	Readiness              func(context.Context) map[string]string
	BenchmarkReadiness     func(context.Context) map[string]string
}

func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), gin.Logger())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/readyz", s.ready)
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))
	r.POST("/api/v1/alerts/alertmanager", s.webhookAuth(), s.alertmanager)
	api := r.Group("/api/v1", s.auth())
	api.POST("/incidents", s.create)
	api.GET("/incidents", s.list)
	api.GET("/incidents/:id", s.get)
	api.GET("/incidents/:id/alerts", s.alerts)
	api.GET("/incidents/:id/evidence", s.evidence)
	api.GET("/incidents/:id/hypotheses", s.hypotheses)
	api.GET("/incidents/:id/agent-runs", s.agentRuns)
	api.GET("/incidents/:id/investigation", s.investigation)
	api.GET("/incidents/:id/events", s.events)
	api.GET("/incidents/:id/stream", s.stream)
	api.POST("/incidents/:id/approval", s.approval)
	api.POST("/incidents/:id/retry", s.retry)
	api.POST("/incidents/:id/workflow-attempts/migrate", s.migrateWorkflowAttempt)
	api.GET("/model/health", func(c *gin.Context) { c.JSON(200, s.ModelHealth()) })
	api.POST("/model/probe", func(c *gin.Context) {
		if err := s.ModelProbe(c); err != nil {
			c.JSON(502, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"status": "ok"})
	})
	api.GET("/reranker/health", func(c *gin.Context) {
		if s.RerankerHealth == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "disabled"})
			return
		}
		c.JSON(http.StatusOK, s.RerankerHealth())
	})
	api.POST("/reranker/probe", func(c *gin.Context) {
		if s.RerankerProbe == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "reranker is disabled"})
			return
		}
		if s.RerankerHealth != nil {
			if configured, ok := s.RerankerHealth()["configured"].(bool); ok && !configured {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "reranker is disabled"})
				return
			}
		}
		if err := s.RerankerProbe(c); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	api.GET("/runtime/readiness", s.runtimeReadiness)
	api.GET("/knowledge/causal-patterns", s.causalPatterns)
	api.GET("/knowledge/causal-patterns/:id", s.causalPattern)
	api.POST("/knowledge/causal-patterns/:id/status", s.causalPatternStatus)
	api.POST("/knowledge/causal-patterns/:id/rollback", s.causalPatternRollback)
	api.POST("/benchmarks/runs", s.benchmarkStart)
	api.GET("/benchmarks/runs", s.benchmarkList)
	api.GET("/benchmarks/runs/:id", s.benchmarkGet)
	api.GET("/benchmarks/runs/:id/stream", s.benchmarkStream)
	api.POST("/benchmarks/runs/:id/cancel", s.benchmarkCancel)
	api.POST("/benchmarks/runs/:id/resume", s.benchmarkResume)
	api.GET("/benchmarks/runs/:id/results", s.benchmarkResults)
	api.GET("/benchmarks/runs/:id/artifacts", s.benchmarkArtifacts)
	r.GET("/", func(c *gin.Context) {
		index, err := webFS.ReadFile("web/index.html")
		if err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	})
	return r
}

func (s *Server) ready(c *gin.Context) {
	components := map[string]string{"api": "ready"}
	if s.Readiness != nil {
		components = s.Readiness(c)
	}
	ready := true
	for _, status := range components {
		if status != "ready" {
			ready = false
			break
		}
	}
	code := http.StatusOK
	status := "ready"
	if !ready {
		code = http.StatusServiceUnavailable
		status = "not_ready"
	}
	c.JSON(code, gin.H{"status": status, "components": components})
}

func (s *Server) runtimeReadiness(c *gin.Context) {
	components := map[string]string{"api": "ready"}
	if s.Readiness != nil {
		components = s.Readiness(c)
	}
	if s.ModelHealth != nil {
		health := s.ModelHealth()
		if configured, ok := health["configured"].(bool); ok && configured {
			components["model"] = "ready"
		} else {
			components["model"] = "not_ready"
		}
	}
	if s.BenchmarkReadiness != nil {
		for component, status := range s.BenchmarkReadiness(c) {
			components[component] = status
		}
	}
	code := http.StatusOK
	for _, status := range components {
		if status != "ready" && status != "disabled" {
			code = http.StatusServiceUnavailable
			break
		}
	}
	c.JSON(code, gin.H{"components": components})
}

func (s *Server) causalPatterns(c *gin.Context) {
	if s.Knowledge == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "knowledge store unavailable"})
		return
	}
	status := c.Query("status")
	if status != "" && status != "active" && status != "disabled" && status != "candidate" && status != "validating" && status != "rejected" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status filter"})
		return
	}
	items, err := s.Knowledge.ListCausalPatterns(c, status)
	respond(c, items, err)
}
func (s *Server) causalPattern(c *gin.Context) {
	if s.Knowledge == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "knowledge store unavailable"})
		return
	}
	item, err := s.Knowledge.GetCausalPattern(c, c.Param("id"))
	respond(c, item, err)
}
func (s *Server) causalPatternStatus(c *gin.Context) {
	if s.Knowledge == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "knowledge store unavailable"})
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if in.Status != "active" && in.Status != "disabled" && in.Status != "validating" && in.Status != "rejected" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be active, validating, rejected, or disabled"})
		return
	}
	operator := strings.TrimSpace(c.GetHeader("X-Operator"))
	if operator == "" {
		operator = "api-user"
	}
	item, err := s.Knowledge.SetCausalPatternStatus(c, c.Param("id"), in.Status, operator)
	respond(c, item, err)
}
func (s *Server) causalPatternRollback(c *gin.Context) {
	if s.Knowledge == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "knowledge store unavailable"})
		return
	}
	var in struct {
		Revision int `json:"revision"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Revision <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a positive revision is required"})
		return
	}
	operator := strings.TrimSpace(c.GetHeader("X-Operator"))
	if operator == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "X-Operator is required"})
		return
	}
	item, err := s.Knowledge.RollbackCausalPattern(c, c.Param("id"), in.Revision, operator)
	respond(c, item, err)
}
func (s *Server) retry(c *gin.Context) {
	v, err := s.Manager.Retry(c, c.Param("id"))
	respond(c, v, err)
}
func (s *Server) migrateWorkflowAttempt(c *gin.Context) {
	v, err := s.Manager.MigrateWorkflowAttempt(c, c.Param("id"))
	respond(c, v, err)
}
func (s *Server) hypotheses(c *gin.Context) {
	incident, err := s.Manager.Get(c, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	if incident.Investigation != nil && incident.Investigation.Architecture == "eino-native-self-reflective-brain" {
		c.JSON(http.StatusOK, gin.H{
			"hypotheses":         incident.Investigation.AgentHypotheses,
			"admissions":         incident.Investigation.HypothesisAdmissions,
			"groundings":         incident.Investigation.HypothesisGroundings,
			"grounding_deltas":   incident.Investigation.GroundingDeltas,
			"belief_deltas":      incident.Investigation.BeliefDeltas,
			"reflections":        incident.Investigation.Reflections,
			"diagnosis":          incident.Investigation.AgentDiagnosis,
			"execution_snapshot": incident.Investigation.ExecutionSnapshot,
			"workflow_attempt":   incident.Investigation.WorkflowAttempt,
		})
		return
	}
	if incident.DiagnosisLedger == nil {
		c.JSON(http.StatusOK, []domain.VerifiedHypothesis{})
		return
	}
	c.JSON(http.StatusOK, gin.H{"drafts": incident.DiagnosisLedger.Drafts, "verified": incident.DiagnosisLedger.Verified, "transitions": incident.DiagnosisLedger.HypothesisTransitions, "confidence_history": confidenceHistory(incident.DiagnosisLedger.Verified)})
}
func (s *Server) agentRuns(c *gin.Context) {
	incident, err := s.Manager.Get(c, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	var decisions []domain.AgentDecisionEvent
	var feedback []domain.SafetyFeedback
	if incident.DiagnosisLedger != nil {
		decisions = incident.DiagnosisLedger.AgentDecisions
		feedback = incident.DiagnosisLedger.SafetyFeedback
	}
	architecture := "constrained-react"
	var modelUsage []domain.ModelUsageEvent
	var memoryReads []domain.MemoryAccessEvent
	if incident.Investigation != nil {
		architecture = incident.Investigation.Architecture
		modelUsage = incident.Investigation.ModelUsage
		memoryReads = incident.Investigation.MemoryReads
	}
	type agentRunSummary struct {
		Agent              string  `json:"agent"`
		ParentAgent        string  `json:"parent_agent,omitempty"`
		ModelCalls         int     `json:"model_calls"`
		InputTokens        int     `json:"input_tokens"`
		OutputTokens       int     `json:"output_tokens"`
		ReasoningTokens    int     `json:"reasoning_tokens"`
		DurationMS         float64 `json:"duration_ms"`
		EstimatedCost      float64 `json:"estimated_cost"`
		Iterations         int     `json:"iterations"`
		ToolUses           int     `json:"tool_uses"`
		ToolComplexityCost int     `json:"tool_complexity_cost"`
		Corrections        int     `json:"corrections"`
	}
	byAgent := map[string]*agentRunSummary{}
	for _, usage := range modelUsage {
		item := byAgent[usage.Agent]
		if item == nil {
			item = &agentRunSummary{Agent: usage.Agent, ParentAgent: usage.ParentAgent}
			byAgent[usage.Agent] = item
		}
		item.ModelCalls++
		item.InputTokens += usage.InputTokens
		item.OutputTokens += usage.OutputTokens
		item.ReasoningTokens += usage.ReasoningTokens
		item.DurationMS += usage.DurationMS
		item.EstimatedCost += usage.EstimatedCost
	}
	if incident.AgentBudget != nil {
		for agentName, usage := range incident.AgentBudget.Usage {
			item := byAgent[agentName]
			if item == nil {
				item = &agentRunSummary{Agent: agentName}
				byAgent[agentName] = item
			}
			item.Iterations = usage.Iterations
			item.ToolUses = usage.ToolUses
			item.ToolComplexityCost = usage.ToolCost
			item.Corrections = usage.Corrections
		}
	}
	agents := make([]agentRunSummary, 0, len(byAgent))
	for _, item := range byAgent {
		agents = append(agents, *item)
	}
	sort.SliceStable(agents, func(i, j int) bool { return agents[i].Agent < agents[j].Agent })
	c.JSON(http.StatusOK, gin.H{"workflow": domain.WorkflowRuntimeName, "strategy": incident.DiagnosisMethod, "architecture": architecture, "skill_snapshot_hash": incident.SkillSnapshotHash, "ranking_policy_hash": incident.RankingPolicyHash, "reranker_config_hash": incident.RerankerConfigHash, "agents": agents, "budget": incident.AgentBudget, "model_usage": modelUsage, "memory_reads": memoryReads, "decisions": decisions, "safety_feedback": feedback})
}

func (s *Server) investigation(c *gin.Context) {
	incident, err := s.Manager.Get(c, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	if incident.Investigation == nil {
		c.JSON(http.StatusOK, gin.H{"strategy": incident.DiagnosisMethod, "status": "not_available"})
		return
	}
	c.JSON(http.StatusOK, incident.Investigation)
}
func confidenceHistory(items []domain.VerifiedHypothesis) []domain.HypothesisConfidenceRecord {
	var out []domain.HypothesisConfidenceRecord
	for _, item := range items {
		out = append(out, item.ConfidenceHistory...)
	}
	return out
}
func (s *Server) auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+s.APIToken {
			c.AbortWithStatusJSON(401, gin.H{"error": "unauthorized"})
			return
		}
	}
}

func (s *Server) benchmarkStart(c *gin.Context) {
	if s.Benchmarks == nil {
		c.JSON(503, gin.H{"error": "benchmark manager unavailable"})
		return
	}
	var in struct {
		Profile      string   `json:"profile"`
		Strategies   []string `json:"strategies"`
		DatasetSplit string   `json:"dataset_split"`
		Seeds        []int64  `json:"seeds"`
		Repetitions  int      `json:"repetitions"`
		ModelProfile string   `json:"model_profile"`
		AutoApprove  bool     `json:"auto_approve"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if len(in.Strategies) == 0 && (in.Profile == "smoke" || in.Profile == "ci" || in.Profile == "standard" || in.Profile == "robustness" || in.Profile == "full") {
		in.Strategies = []string{
			domain.DiagnosisMethodDirect,
			domain.DiagnosisMethodRAG,
			domain.DiagnosisMethodReAct,
			domain.DiagnosisMethodRuleOnly,
			domain.DiagnosisMethodEvidence,
			domain.DiagnosisMethodCognitive,
			domain.DiagnosisMethodActive,
			domain.DiagnosisMethodKubePilot,
			domain.DiagnosisMethodKubePilotNoReflection,
			domain.DiagnosisMethodKubePilotNoOptionalSkills,
		}
	}
	run, err := s.Benchmarks.StartRequest(service.BenchmarkRequest{Profile: in.Profile, Strategies: in.Strategies, DatasetSplit: in.DatasetSplit, Seeds: in.Seeds, Repetitions: in.Repetitions, ModelProfile: in.ModelProfile, AutoApprove: in.AutoApprove})
	respond(c, run, err)
}
func (s *Server) benchmarkList(c *gin.Context) {
	if s.Benchmarks == nil {
		c.JSON(503, gin.H{"error": "benchmark manager unavailable"})
		return
	}
	c.JSON(200, s.Benchmarks.List())
}
func (s *Server) benchmarkGet(c *gin.Context) {
	if s.Benchmarks == nil {
		c.JSON(503, gin.H{"error": "benchmark manager unavailable"})
		return
	}
	v, err := s.Benchmarks.Get(c.Param("id"))
	respond(c, v, err)
}
func (s *Server) benchmarkCancel(c *gin.Context) {
	if s.Benchmarks == nil {
		c.JSON(503, gin.H{"error": "benchmark manager unavailable"})
		return
	}
	err := s.Benchmarks.Cancel(c.Param("id"))
	respond(c, gin.H{"status": "cancelling"}, err)
}
func (s *Server) benchmarkResume(c *gin.Context) {
	if s.Benchmarks == nil {
		c.JSON(503, gin.H{"error": "benchmark manager unavailable"})
		return
	}
	run, err := s.Benchmarks.Resume(c.Param("id"))
	respond(c, run, err)
}
func (s *Server) benchmarkResults(c *gin.Context) {
	v, err := s.Benchmarks.Results(c.Param("id"))
	respond(c, v, err)
}
func (s *Server) benchmarkArtifacts(c *gin.Context) {
	v, err := s.Benchmarks.Artifacts(c.Param("id"))
	respond(c, v, err)
}
func (s *Server) benchmarkStream(c *gin.Context) {
	if s.Benchmarks == nil {
		c.JSON(503, gin.H{"error": "benchmark manager unavailable"})
		return
	}
	ch, done := s.Manager.Hub.Subscribe("benchmark:" + c.Param("id"))
	defer done()
	c.Header("Content-Type", "text/event-stream")
	c.Stream(func(w io.Writer) bool {
		select {
		case b, ok := <-ch:
			if !ok {
				return false
			}
			c.SSEvent("benchmark", string(b))
			return true
		case <-time.After(20 * time.Second):
			c.SSEvent("ping", gin.H{"time": time.Now().UTC()})
			return true
		case <-c.Request.Context().Done():
			return false
		}
	})
}
func (s *Server) webhookAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+s.WebhookToken {
			c.AbortWithStatusJSON(401, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}
func (s *Server) create(c *gin.Context) {
	var in service.ManualIncident
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	v, err := s.Manager.Create(c, in)
	respond(c, v, err)
}
func (s *Server) list(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	v, err := s.Manager.List(c, limit, offset)
	respond(c, v, err)
}
func (s *Server) get(c *gin.Context) { v, err := s.Manager.Get(c, c.Param("id")); respond(c, v, err) }
func (s *Server) alerts(c *gin.Context) {
	v, err := s.Manager.Get(c, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	c.JSON(200, v.Alerts)
}
func (s *Server) evidence(c *gin.Context) {
	v, err := s.Manager.Get(c, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	c.JSON(200, v.Evidence)
}
func (s *Server) events(c *gin.Context) {
	v, err := s.Manager.Events(c, c.Param("id"))
	respond(c, v, err)
}
func (s *Server) approval(c *gin.Context) {
	var in struct {
		ProposalID string `json:"proposal_id"`
		Decision   string `json:"decision"`
		Comment    string `json:"comment"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(c.GetHeader("Idempotency-Key")) == "" {
		c.JSON(400, gin.H{"error": "Idempotency-Key is required"})
		return
	}
	v, err := s.Manager.Approve(c, c.Param("id"), in.ProposalID, in.Decision, in.Comment, c.GetHeader("Idempotency-Key"))
	respond(c, v, err)
}
func (s *Server) stream(c *gin.Context) {
	ch, done := s.Manager.Hub.Subscribe(c.Param("id"))
	defer done()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Stream(func(w io.Writer) bool {
		select {
		case b, ok := <-ch:
			if !ok {
				return false
			}
			c.SSEvent("incident", json.RawMessage(b))
			return true
		case <-time.After(20 * time.Second):
			c.SSEvent("ping", gin.H{"time": time.Now().UTC()})
			return true
		case <-c.Request.Context().Done():
			return false
		}
	})
}
func (s *Server) alertmanager(c *gin.Context) {
	var body struct {
		Status string `json:"status"`
		Alerts []struct {
			Status      string            `json:"status"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			StartsAt    time.Time         `json:"startsAt"`
			EndsAt      time.Time         `json:"endsAt"`
			Fingerprint string            `json:"fingerprint"`
		} `json:"alerts"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	ids := []string{}
	for _, raw := range body.Alerts {
		a := domain.Alert{Fingerprint: raw.Fingerprint, Name: raw.Labels["alertname"], Status: raw.Status, Labels: raw.Labels, Annotations: raw.Annotations, StartsAt: raw.StartsAt, EndsAt: raw.EndsAt}
		in, err := s.Manager.IngestAlert(c, a, first(raw.Labels, "service", "app", "workload"), first(raw.Labels, "namespace"), first(raw.Labels, "severity"), first(raw.Labels, "deployment", "pod"), first(raw.Annotations, "summary", "description"))
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		ids = append(ids, in.ID)
	}
	c.JSON(202, gin.H{"incident_ids": ids})
}
func respond(c *gin.Context, v any, err error) {
	if err == nil {
		c.JSON(200, v)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(404, gin.H{"error": "not found"})
		return
	}
	c.JSON(409, gin.H{"error": fmt.Sprint(err)})
}
func first(m map[string]string, keys ...string) string {
	for _, k := range keys {
		if m[k] != "" {
			return m[k]
		}
	}
	return ""
}
