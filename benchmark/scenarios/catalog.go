package scenarios

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Catalog struct {
	Version    string     `yaml:"version"`
	Namespace  string     `yaml:"namespace"`
	Defaults   Timeouts   `yaml:"defaults"`
	Categories []Category `yaml:"categories"`
}
type Category struct {
	Name        string    `yaml:"name"`
	Repetitions int       `yaml:"repetitions"`
	Variants    []Variant `yaml:"variants"`
}
type Variant struct {
	Name             string   `yaml:"name"`
	Injector         string   `yaml:"injector"`
	RootCause        string   `yaml:"root_cause"`
	RequiredEvidence []string `yaml:"required_evidence"`
	AllowedActions   []string `yaml:"allowed_actions"`
}
type Timeouts struct {
	FaultVisible time.Duration `yaml:"fault_visible"`
	Diagnosis    time.Duration `yaml:"diagnosis"`
	Recovery     time.Duration `yaml:"recovery"`
}

func (t *Timeouts) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		FaultVisible string `yaml:"fault_visible"`
		Diagnosis    string `yaml:"diagnosis"`
		Recovery     string `yaml:"recovery"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	var err error
	if t.FaultVisible, err = time.ParseDuration(raw.FaultVisible); err != nil {
		return err
	}
	if t.Diagnosis, err = time.ParseDuration(raw.Diagnosis); err != nil {
		return err
	}
	t.Recovery, err = time.ParseDuration(raw.Recovery)
	return err
}

type Scenario struct {
	ID             string         `json:"id"`
	Split          string         `json:"split"`
	Category       string         `json:"category"`
	Variant        string         `json:"variant"`
	Description    string         `json:"description"`
	Seed           int64          `json:"seed"`
	Repetition     int            `json:"repetition"`
	Namespace      string         `json:"namespace"`
	Service        string         `json:"service"`
	Target         string         `json:"target"`
	Preconditions  []string       `json:"preconditions"`
	Injector       string         `json:"injector"`
	InjectParams   map[string]any `json:"inject_params"`
	ExpectedAlerts []string       `json:"expected_alerts"`
	GroundTruth    GroundTruth    `json:"ground_truth"`
	Timeouts       Timeouts       `json:"timeouts"`
	Verification   []string       `json:"verification"`
	Cleanup        string         `json:"cleanup"`
}
type GroundTruth struct {
	RootCauseCategory      string   `json:"root_cause_category"`
	RootCauseDetail        string   `json:"root_cause_detail"`
	Service                string   `json:"service"`
	Resource               string   `json:"resource"`
	RequiredEvidence       []string `json:"required_evidence"`
	AllowedRecoveryActions []string `json:"allowed_recovery_actions"`
}

func Load(path string) (Catalog, []Scenario, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, nil, "", err
	}
	var c Catalog
	if err = yaml.Unmarshal(b, &c); err != nil {
		return Catalog{}, nil, "", err
	}
	sum := sha256.Sum256(b)
	items := Expand(c)
	if err = Validate(items); err != nil {
		return Catalog{}, nil, "", err
	}
	return c, items, hex.EncodeToString(sum[:]), nil
}
func Expand(c Catalog) []Scenario {
	services := []string{"gateway-service", "order-service", "payment-service"}
	var out []Scenario
	for _, cat := range c.Categories {
		categoryIndex := 0
		for _, v := range cat.Variants {
			for i := 1; i <= cat.Repetitions; i++ {
				service := services[(i-1)%len(services)]
				if cat.Name == "database" {
					service = "payment-service"
				} else if cat.Name == "dependency" {
					service = "order-service"
					if len(v.Name) >= len("payment_") && v.Name[:len("payment_")] == "payment_" {
						service = "payment-service"
					}
				}
				categoryIndex++
				split := "test"
				if categoryIndex <= 4 {
					split = "dev"
				} else if categoryIndex <= 8 {
					split = "validation"
				}
				id := fmt.Sprintf("%s-%s-%02d", cat.Name, v.Name, i)
				out = append(out, Scenario{ID: id, Split: split, Category: cat.Name, Variant: v.Name, Description: v.RootCause, Seed: int64(1000 + len(out)), Repetition: 1, Namespace: c.Namespace, Service: service, Target: service, Preconditions: []string{"target_exists", "baseline_healthy"}, Injector: v.Injector, InjectParams: map[string]any{"variant": v.Name, "seed": int64(1000 + len(out))}, ExpectedAlerts: []string{cat.Name + "_anomaly", "service_error_rate"}, Timeouts: c.Defaults, Verification: []string{"pod_ready", "deployment_available", "business_probe"}, Cleanup: "restore_deployment_snapshot", GroundTruth: GroundTruth{RootCauseCategory: cat.Name, RootCauseDetail: v.RootCause, Service: service, Resource: service, RequiredEvidence: append([]string(nil), v.RequiredEvidence...), AllowedRecoveryActions: append([]string(nil), v.AllowedActions...)}})
			}
		}
	}
	return out
}
func Validate(items []Scenario) error {
	if len(items) != 120 {
		return fmt.Errorf("catalog must expand to 120 scenarios, got %d", len(items))
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	allowedInjectors := map[string]bool{"service_fault": true, "resource_patch": true, "traffic": true, "dependency_scale": true, "config_patch": true, "network_policy": true, "service_patch": true, "deployment_patch": true}
	allowedActions := map[string]bool{"restart_pod": true, "scale_deployment": true, "rollback_deployment": true}
	for _, s := range items {
		if seen[s.ID] {
			return fmt.Errorf("duplicate scenario %s", s.ID)
		}
		seen[s.ID] = true
		counts[s.Category]++
		if !allowedInjectors[s.Injector] {
			return fmt.Errorf("scenario %s uses unsafe injector %q", s.ID, s.Injector)
		}
		if len(s.GroundTruth.RequiredEvidence) == 0 {
			return fmt.Errorf("scenario %s has no required evidence", s.ID)
		}
		if s.Description == "" || len(s.Preconditions) == 0 || len(s.ExpectedAlerts) == 0 || len(s.Verification) == 0 || s.Cleanup == "" {
			return fmt.Errorf("scenario %s is incomplete", s.ID)
		}
		if s.GroundTruth.RootCauseCategory == "" || s.GroundTruth.RootCauseDetail == "" || s.GroundTruth.Service == "" || s.GroundTruth.Resource == "" {
			return fmt.Errorf("scenario %s ground truth is incomplete", s.ID)
		}
		for _, a := range s.GroundTruth.AllowedRecoveryActions {
			if !allowedActions[a] {
				return fmt.Errorf("scenario %s uses unsafe action %q", s.ID, a)
			}
		}
	}
	for _, cat := range []string{"cpu", "memory", "database", "network", "deployment", "dependency"} {
		if counts[cat] != 20 {
			return fmt.Errorf("category %s has %d scenarios, want 20", cat, counts[cat])
		}
	}
	splits := map[string]int{}
	for _, item := range items {
		splits[item.Split]++
	}
	for split, expected := range map[string]int{"dev": 24, "validation": 24, "test": 72} {
		if splits[split] != expected {
			return fmt.Errorf("split %s has %d scenarios, want %d", split, splits[split], expected)
		}
	}
	return nil
}
