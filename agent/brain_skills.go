package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"gopkg.in/yaml.v3"
)

const defaultBrainSkillCatalog = "internal/agent/skills/brain/catalog.yaml"

type brainSkillCatalogFile struct {
	Version    int                      `yaml:"version"`
	ToolPolicy domain.ToolCallingPolicy `yaml:"tool_policy"`
	Skills     []brainSkillSpec         `yaml:"skills"`
}

type brainSkillSpec struct {
	ID                    string                     `yaml:"id"`
	Version               string                     `yaml:"version"`
	Requires              []string                   `yaml:"requires"`
	Conflicts             []string                   `yaml:"conflicts"`
	Mandatory             bool                       `yaml:"mandatory"`
	CompatiblePhases      []domain.BrainPhase        `yaml:"compatible_phases"`
	AllowedToolCategories []domain.BrainToolCategory `yaml:"allowed_tool_categories"`
	OutputContract        string                     `yaml:"output_contract"`
	References            []string                   `yaml:"references"`
}

type brainSkillPackage struct {
	Spec        brainSkillSpec
	Description string
	Content     string
	Hash        string
	References  map[string]string
}

type SkillRequest struct {
	SkillID       string
	Reason        string
	Trigger       string
	RequestedBy   string
	RequestedTurn string
}

type ResolvedBrainSkills struct {
	Refs              []domain.SkillRef
	Prompt            string
	AllowedCategories map[domain.BrainToolCategory]bool
	Activations       []domain.SkillActivation
	active            map[string]brainSkillPackage
}

// brainSkillCatalogEntry is the bounded, model-facing description of one
// optional capability. It deliberately exposes only frozen catalog metadata:
// the Brain can choose an exact Skill ID, while the Runtime still owns phase,
// dependency, conflict, budget, and authority validation.
type brainSkillCatalogEntry struct {
	ID                    string                     `json:"id"`
	Version               string                     `json:"version"`
	Description           string                     `json:"description"`
	Requires              []string                   `json:"requires,omitempty"`
	AllowedToolCategories []domain.BrainToolCategory `json:"allowed_tool_categories"`
	OutputContract        string                     `json:"output_contract"`
	References            []string                   `json:"references,omitempty"`
}

type BrainSkillResolver struct {
	version    int
	root       string
	catalogRaw []byte
	packages   map[string]brainSkillPackage
	policy     domain.ToolCallingPolicy
	hash       string
}

func LoadBrainSkillResolver(path string) (*BrainSkillResolver, error) {
	path = resolveProjectFile(path)
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var catalog brainSkillCatalogFile
	if err = yaml.Unmarshal(raw, &catalog); err != nil {
		return nil, fmt.Errorf("decode brain skill catalog: %w", err)
	}
	if catalog.Version <= 0 || len(catalog.Skills) == 0 {
		return nil, fmt.Errorf("brain skill catalog is empty or unversioned")
	}
	resolver := &BrainSkillResolver{version: catalog.Version, root: filepath.Dir(path), catalogRaw: append([]byte(nil), raw...), packages: map[string]brainSkillPackage{}, policy: catalog.ToolPolicy}
	for _, spec := range catalog.Skills {
		if err = resolver.loadPackage(spec); err != nil {
			return nil, err
		}
	}
	if err = resolver.validateCatalog(); err != nil {
		return nil, err
	}
	resolver.hash = resolver.computeHash()
	return resolver, nil
}

func LoadDefaultBrainSkillResolver() (*BrainSkillResolver, error) {
	return LoadBrainSkillResolver(defaultBrainSkillCatalog)
}

func (r *BrainSkillResolver) ToolPolicy() domain.ToolCallingPolicy { return r.policy }

func (r *BrainSkillResolver) SnapshotHash() string {
	if r == nil {
		return ""
	}
	return r.hash
}

// OptionalCatalog returns exact phase-compatible Skill IDs without activating
// anything or expanding authority. The returned metadata is sufficient for the
// LLM to make an informed request_skills call; Resolve remains the sole
// admission boundary.
func (r *BrainSkillResolver) OptionalCatalog(phase domain.BrainPhase) []brainSkillCatalogEntry {
	if r == nil {
		return nil
	}
	entries := make([]brainSkillCatalogEntry, 0, len(r.packages))
	for _, pkg := range r.packages {
		if pkg.Spec.Mandatory || !supportsPhase(pkg.Spec, phase) {
			continue
		}
		entries = append(entries, brainSkillCatalogEntry{
			ID:                    pkg.Spec.ID,
			Version:               pkg.Spec.Version,
			Description:           pkg.Description,
			Requires:              append([]string(nil), pkg.Spec.Requires...),
			AllowedToolCategories: append([]domain.BrainToolCategory(nil), pkg.Spec.AllowedToolCategories...),
			OutputContract:        pkg.Spec.OutputContract,
			References:            append([]string(nil), pkg.Spec.References...),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return entries
}

func (r *BrainSkillResolver) ReadActiveReference(active []domain.SkillRef, skillID, name string) (string, error) {
	pkg, ok := r.packages[skillID]
	if !ok {
		return "", fmt.Errorf("unknown skill %s", skillID)
	}
	activeHash := ""
	for _, ref := range active {
		if ref.ID == skillID {
			activeHash = ref.ContentHash
			break
		}
	}
	if activeHash == "" || activeHash != pkg.Hash {
		return "", fmt.Errorf("skill %s is not active in the frozen bundle", skillID)
	}
	content, ok := pkg.References[name]
	if !ok {
		return "", fmt.Errorf("reference %s is not declared by active skill %s", name, skillID)
	}
	return content, nil
}

func (r *BrainSkillResolver) loadPackage(spec brainSkillSpec) error {
	if spec.ID == "" || spec.Version == "" || spec.OutputContract == "" || len(spec.CompatiblePhases) == 0 || len(spec.AllowedToolCategories) == 0 {
		return fmt.Errorf("brain skill has incomplete catalog metadata: %q", spec.ID)
	}
	if _, exists := r.packages[spec.ID]; exists {
		return fmt.Errorf("duplicate brain skill %q", spec.ID)
	}
	dir := filepath.Join(r.root, spec.ID)
	raw, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return fmt.Errorf("load brain skill %s: %w", spec.ID, err)
	}
	name, description, content, err := parseBrainSkill(raw)
	if err != nil {
		return fmt.Errorf("load brain skill %s: %w", spec.ID, err)
	}
	if name != spec.ID {
		return fmt.Errorf("brain skill folder %s declares name %s", spec.ID, name)
	}
	references := map[string]string{}
	for _, ref := range spec.References {
		if filepath.Base(ref) != ref || strings.Contains(ref, "..") {
			return fmt.Errorf("brain skill %s has unsafe reference %q", spec.ID, ref)
		}
		refRaw, readErr := os.ReadFile(filepath.Join(dir, "references", ref))
		if readErr != nil {
			return fmt.Errorf("load brain skill %s reference %s: %w", spec.ID, ref, readErr)
		}
		references[ref] = strings.TrimSpace(string(refRaw))
	}
	h := sha256.New()
	_, _ = h.Write(raw)
	names := sortedStringKeys(references)
	for _, name := range names {
		_, _ = h.Write([]byte("\nreference:" + name + "\n" + references[name]))
	}
	r.packages[spec.ID] = brainSkillPackage{Spec: spec, Description: description, Content: content, Hash: hex.EncodeToString(h.Sum(nil)), References: references}
	return nil
}

func parseBrainSkill(raw []byte) (string, string, string, error) {
	text := string(raw)
	if !strings.HasPrefix(text, "---\n") {
		return "", "", "", fmt.Errorf("missing YAML front matter")
	}
	parts := strings.SplitN(strings.TrimPrefix(text, "---\n"), "\n---\n", 2)
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("invalid YAML front matter")
	}
	var front map[string]any
	if err := yaml.Unmarshal([]byte(parts[0]), &front); err != nil {
		return "", "", "", err
	}
	if len(front) != 2 {
		return "", "", "", fmt.Errorf("front matter must contain only name and description")
	}
	name, _ := front["name"].(string)
	description, _ := front["description"].(string)
	if name == "" || description == "" {
		return "", "", "", fmt.Errorf("name and description are required")
	}
	content := strings.TrimSpace(parts[1])
	for _, heading := range []string{"## Preconditions", "## Server-Owned Inputs", "## Procedure", "## Allowed Tools", "## Required IDs", "## Output Contract", "## Output Example", "## Stop & Failure Conditions", "## Handoff"} {
		if !strings.Contains(content, heading) {
			return "", "", "", fmt.Errorf("missing %s", heading)
		}
	}
	lower := strings.ToLower(content)
	for _, forbidden := range []string{"scenario_id", "case_id", "ground_truth", "benchmark/", "benchmark\\"} {
		if strings.Contains(lower, forbidden) {
			return "", "", "", fmt.Errorf("contains forbidden evaluation content %q", forbidden)
		}
	}
	return name, description, content, nil
}

func (r *BrainSkillResolver) validateCatalog() error {
	if r.policy.MaxSameToolRepeat <= 0 || r.policy.MaxNoInformationStreak <= 0 {
		return fmt.Errorf("brain tool policy has invalid repeat limits")
	}
	if _, ok := r.packages["brain-kernel"]; !ok {
		return fmt.Errorf("brain-kernel is required")
	}
	for id, pkg := range r.packages {
		for _, dependency := range pkg.Spec.Requires {
			if _, ok := r.packages[dependency]; !ok {
				return fmt.Errorf("brain skill %s requires unknown skill %s", id, dependency)
			}
		}
		for _, conflict := range pkg.Spec.Conflicts {
			if _, ok := r.packages[conflict]; !ok {
				return fmt.Errorf("brain skill %s conflicts with unknown skill %s", id, conflict)
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("brain skill dependency cycle at %s", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, dependency := range r.packages[id].Spec.Requires {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	for id := range r.packages {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func (r *BrainSkillResolver) computeHash() string {
	h := sha256.New()
	_, _ = h.Write(r.catalogRaw)
	names := sortedSkillKeys(r.packages)
	for _, name := range names {
		_, _ = h.Write([]byte("\n" + name + ":" + r.packages[name].Hash))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r *BrainSkillResolver) Resolve(phase domain.BrainPhase, requests []SkillRequest, maxOptional int, activationTurn string) (ResolvedBrainSkills, error) {
	if r == nil {
		return ResolvedBrainSkills{}, fmt.Errorf("brain skill resolver is required")
	}
	if strings.TrimSpace(activationTurn) == "" {
		return ResolvedBrainSkills{}, fmt.Errorf("brain skill activation turn is required")
	}
	now := time.Now().UTC()
	selected := map[string]SkillRequest{}
	for id, pkg := range r.packages {
		if pkg.Spec.Mandatory && supportsPhase(pkg.Spec, phase) {
			selected[id] = SkillRequest{SkillID: id, Reason: "mandatory phase procedure", Trigger: "PHASE_ENTRY", RequestedBy: "RUNTIME", RequestedTurn: activationTurn}
		}
	}
	var rejected []domain.SkillActivation
	optional := 0
	for _, requested := range requests {
		request := requested
		if strings.TrimSpace(request.RequestedTurn) == "" {
			request.RequestedTurn = activationTurn
		}
		pkg, ok := r.packages[request.SkillID]
		if !ok {
			rejected = append(rejected, rejectedActivationFor(r, request, phase, "unknown_skill", now))
			continue
		}
		activation := activationFor(pkg, request, phase, now)
		if request.Reason == "" || request.Trigger == "" || request.RequestedBy != "BRAIN" {
			activation.Status, activation.RejectedReason = "REJECTED", "invalid_selection_reason"
			rejected = append(rejected, activation)
			continue
		}
		if !supportsPhase(pkg.Spec, phase) {
			activation.Status, activation.RejectedReason = "REJECTED", "incompatible_phase"
			rejected = append(rejected, activation)
			continue
		}
		needed := r.dependencyClosure(request.SkillID)
		newOptional := 0
		for _, id := range needed {
			if _, exists := selected[id]; !exists && !r.packages[id].Spec.Mandatory {
				newOptional++
			}
		}
		if maxOptional >= 0 && optional+newOptional > maxOptional {
			activation.Status, activation.RejectedReason = "REJECTED", "optional_skill_budget_exhausted"
			rejected = append(rejected, activation)
			continue
		}
		for _, id := range needed {
			if _, exists := selected[id]; exists {
				continue
			}
			dependencyRequest := request
			dependencyRequest.SkillID = id
			if id != request.SkillID {
				dependencyRequest.Reason = "dependency of " + request.SkillID
				dependencyRequest.RequestedBy = "RUNTIME"
			}
			selected[id] = dependencyRequest
		}
		optional += newOptional
	}
	if len(selected) == 0 {
		return ResolvedBrainSkills{}, fmt.Errorf("no mandatory skill is available for phase %s", phase)
	}
	for id := range selected {
		for _, conflict := range r.packages[id].Spec.Conflicts {
			if _, ok := selected[conflict]; ok {
				return ResolvedBrainSkills{}, fmt.Errorf("active brain skills conflict: %s and %s", id, conflict)
			}
		}
	}
	ordered, err := r.topologicalOrder(selected)
	if err != nil {
		return ResolvedBrainSkills{}, err
	}
	out := ResolvedBrainSkills{AllowedCategories: map[domain.BrainToolCategory]bool{}, active: map[string]brainSkillPackage{}}
	var prompt strings.Builder
	for _, id := range ordered {
		pkg, request := r.packages[id], selected[id]
		out.active[id] = pkg
		out.Refs = append(out.Refs, domain.SkillRef{ID: id, Version: pkg.Spec.Version, ContentHash: pkg.Hash})
		out.Activations = append(out.Activations, activationFor(pkg, request, phase, now))
		if id != "brain-kernel" {
			for _, category := range pkg.Spec.AllowedToolCategories {
				out.AllowedCategories[category] = true
			}
		}
		fmt.Fprintf(&prompt, "\n\n<skill id=%q version=%q hash=%q>\n%s\n</skill>", id, pkg.Spec.Version, pkg.Hash, pkg.Content)
	}
	if len(out.AllowedCategories) == 0 {
		for _, category := range r.packages["brain-kernel"].Spec.AllowedToolCategories {
			out.AllowedCategories[category] = true
		}
	}
	out.Prompt = strings.TrimSpace(prompt.String())
	out.Activations = append(out.Activations, rejected...)
	return out, nil
}

func (r ResolvedBrainSkills) ReadReference(skillID, name string) (string, error) {
	pkg, ok := r.active[skillID]
	if !ok {
		return "", fmt.Errorf("skill %s is not active", skillID)
	}
	content, ok := pkg.References[name]
	if !ok {
		return "", fmt.Errorf("reference %s is not declared by active skill %s", name, skillID)
	}
	return content, nil
}

// unambiguousRequestedCategory returns a category only when the accepted
// requested Skills themselves (not mandatory phase Skills or their dependency
// closure) narrow execution to exactly one non-control category. It therefore
// cannot expand authority: contextBuilder still intersects the selected
// category with the resolved Skill bundle before exposing a ToolsNode.
func (r *BrainSkillResolver) unambiguousRequestedCategory(requests []SkillRequest) domain.BrainToolCategory {
	categories := map[domain.BrainToolCategory]bool{}
	for _, request := range requests {
		pkg, ok := r.packages[request.SkillID]
		if !ok {
			continue
		}
		for _, category := range pkg.Spec.AllowedToolCategories {
			if category == domain.BrainToolControl {
				continue
			}
			categories[category] = true
		}
	}
	if len(categories) != 1 {
		return ""
	}
	for category := range categories {
		return category
	}
	return ""
}

func activationFor(pkg brainSkillPackage, request SkillRequest, phase domain.BrainPhase, now time.Time) domain.SkillActivation {
	return domain.SkillActivation{SkillID: pkg.Spec.ID, Version: pkg.Spec.Version, ContentHash: pkg.Hash, Phase: phase, Reason: request.Reason, Trigger: request.Trigger, RequestedBy: request.RequestedBy, RequestedTurn: request.RequestedTurn, Status: "ACTIVATED", ActivatedAt: now}
}

// rejectedActivationFor preserves the immutable catalog identity of a known
// Skill even when a later routing or policy decision rejects its activation.
// Rejection is an execution decision about that exact Skill package, so its
// audit record must remain replayable against the frozen catalog snapshot.
func rejectedActivationFor(resolver *BrainSkillResolver, request SkillRequest, phase domain.BrainPhase, reason string, now time.Time) domain.SkillActivation {
	activation := domain.SkillActivation{
		SkillID:       request.SkillID,
		Phase:         phase,
		Reason:        request.Reason,
		Trigger:       request.Trigger,
		RequestedBy:   request.RequestedBy,
		RequestedTurn: request.RequestedTurn,
		ActivatedAt:   now,
	}
	if resolver != nil {
		if pkg, ok := resolver.packages[request.SkillID]; ok {
			activation = activationFor(pkg, request, phase, now)
		} else {
			// No package content exists for an unknown ID. Bind the rejection to
			// the exact catalog version and snapshot that proved its absence so
			// the decision remains complete and replayable without inventing a
			// Skill package identity.
			activation.Version = fmt.Sprintf("catalog-v%d-unresolved", resolver.version)
			activation.ContentHash = resolver.SnapshotHash()
		}
	}
	activation.Status = "REJECTED"
	activation.RejectedReason = reason
	return activation
}

func supportsPhase(spec brainSkillSpec, phase domain.BrainPhase) bool {
	for _, candidate := range spec.CompatiblePhases {
		if candidate == phase {
			return true
		}
	}
	return false
}

func (r *BrainSkillResolver) dependencyClosure(id string) []string {
	seen := map[string]bool{}
	var visit func(string)
	visit = func(current string) {
		if seen[current] {
			return
		}
		seen[current] = true
		for _, dependency := range r.packages[current].Spec.Requires {
			visit(dependency)
		}
	}
	visit(id)
	selected := map[string]SkillRequest{}
	for item := range seen {
		selected[item] = SkillRequest{}
	}
	order, _ := r.topologicalOrder(selected)
	return order
}

func (r *BrainSkillResolver) topologicalOrder(selected map[string]SkillRequest) ([]string, error) {
	seen, visiting := map[string]bool{}, map[string]bool{}
	var out []string
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("brain skill dependency cycle at %s", id)
		}
		if seen[id] {
			return nil
		}
		visiting[id] = true
		dependencies := append([]string(nil), r.packages[id].Spec.Requires...)
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			if _, ok := selected[dependency]; ok {
				if err := visit(dependency); err != nil {
					return err
				}
			}
		}
		delete(visiting, id)
		seen[id] = true
		out = append(out, id)
		return nil
	}
	names := make([]string, 0, len(selected))
	for id := range selected {
		names = append(names, id)
	}
	sort.Strings(names)
	for _, id := range names {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sortedSkillKeys(values map[string]brainSkillPackage) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedStringKeys(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
