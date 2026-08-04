package api

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
}

func (s *Server) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), gin.Logger())
	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })
	r.GET("/readyz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ready"}) })
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
	api.GET("/incidents/:id/events", s.events)
	api.GET("/incidents/:id/stream", s.stream)
	api.POST("/incidents/:id/approval", s.approval)
	api.POST("/incidents/:id/retry", s.retry)
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
	api.GET("/knowledge/causal-patterns", s.causalPatterns)
	api.GET("/knowledge/causal-patterns/:id", s.causalPattern)
	api.POST("/knowledge/causal-patterns/:id/status", s.causalPatternStatus)
	api.POST("/benchmarks/runs", s.benchmarkStart)
	api.GET("/benchmarks/runs", s.benchmarkList)
	api.GET("/benchmarks/runs/:id", s.benchmarkGet)
	api.GET("/benchmarks/runs/:id/stream", s.benchmarkStream)
	api.POST("/benchmarks/runs/:id/cancel", s.benchmarkCancel)
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

func (s *Server) causalPatterns(c *gin.Context) {
	if s.Knowledge == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "knowledge store unavailable"})
		return
	}
	status := c.Query("status")
	if status != "" && status != "active" && status != "disabled" && status != "candidate" {
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
	if in.Status != "active" && in.Status != "disabled" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status must be active or disabled"})
		return
	}
	operator := strings.TrimSpace(c.GetHeader("X-Operator"))
	if operator == "" {
		operator = "api-user"
	}
	item, err := s.Knowledge.SetCausalPatternStatus(c, c.Param("id"), in.Status, operator)
	respond(c, item, err)
}
func (s *Server) retry(c *gin.Context) {
	v, err := s.Manager.Retry(c, c.Param("id"))
	respond(c, v, err)
}
func (s *Server) hypotheses(c *gin.Context) {
	incident, err := s.Manager.Get(c, c.Param("id"))
	if err != nil {
		respond(c, nil, err)
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
	c.JSON(http.StatusOK, gin.H{"workflow": "eino-constrained-react", "skill_snapshot_hash": incident.SkillSnapshotHash, "ranking_policy_hash": incident.RankingPolicyHash, "reranker_config_hash": incident.RerankerConfigHash, "budget": incident.AgentBudget, "decisions": decisions, "safety_feedback": feedback})
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
		Profile     string `json:"profile"`
		AutoApprove bool   `json:"auto_approve"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	run, err := s.Benchmarks.Start(in.Profile, in.AutoApprove)
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
