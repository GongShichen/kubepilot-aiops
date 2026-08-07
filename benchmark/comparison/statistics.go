package comparison

import (
	"fmt"
	"math"
	"sort"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

const bootstrapSamples = 2000

type Interval struct {
	Estimate float64 `json:"estimate"`
	Lower    float64 `json:"lower"`
	Upper    float64 `json:"upper"`
}

type SystemResult struct {
	Strategy          string           `json:"strategy"`
	Summary           reporter.Summary `json:"summary"`
	DiagnosisAccuracy Interval         `json:"diagnosis_accuracy"`
	RecoverySuccess   Interval         `json:"recovery_success"`
	MeanCost          Interval         `json:"mean_cost"`
	MeanLatency       Interval         `json:"mean_latency"`
	P95Latency        Interval         `json:"p95_latency"`
	Breakdowns        []SliceResult    `json:"breakdowns"`
}

// SliceResult makes category, root-cause variant, service, and resource
// results first-class report data instead of requiring consumers to infer
// them from a flat case file.
type SliceResult struct {
	Dimension         string   `json:"dimension"`
	Value             string   `json:"value"`
	Cases             int      `json:"cases"`
	DiagnosisAccuracy Interval `json:"diagnosis_accuracy"`
	RecoverySuccess   Interval `json:"recovery_success"`
	MeanCost          Interval `json:"mean_cost"`
	P95Latency        Interval `json:"p95_latency"`
}

type PairwiseResult struct {
	Baseline            string   `json:"baseline"`
	Target              string   `json:"target"`
	Metric              string   `json:"metric"`
	Pairs               int      `json:"pairs"`
	Difference          Interval `json:"difference"`
	RelativeImprovement float64  `json:"relative_improvement"`
	Test                string   `json:"test"`
	PValue              float64  `json:"p_value"`
	HolmAdjustedPValue  float64  `json:"holm_adjusted_p_value"`
	EffectSize          float64  `json:"effect_size"`
}

type Report struct {
	RunID       string           `json:"run_id"`
	Profile     string           `json:"profile"`
	Valid       bool             `json:"valid"`
	Systems     []SystemResult   `json:"systems"`
	Comparisons []PairwiseResult `json:"comparisons"`
}

func Build(runID, profile string, summaries map[string]reporter.Summary, cases map[string][]reporter.CaseResult) (Report, error) {
	strategies := []string{domain.DiagnosisMethodRuleOnly, domain.DiagnosisMethodEvidence, domain.DiagnosisMethodCognitive, domain.DiagnosisMethodActive, domain.DiagnosisMethodReAct}
	report := Report{RunID: runID, Profile: profile, Valid: true}
	for _, strategy := range strategies {
		items, ok := cases[strategy]
		if !ok {
			continue
		}
		summary, ok := summaries[strategy]
		if !ok {
			return Report{}, fmt.Errorf("missing summary for strategy %s", strategy)
		}
		if err := validateStrategyFootprint(strategy, items); err != nil {
			return Report{}, err
		}
		report.Valid = report.Valid && summary.Valid
		modelItems := withoutInfrastructureFailures(items)
		report.Systems = append(report.Systems, SystemResult{
			Strategy: strategy, Summary: summary,
			DiagnosisAccuracy: stratifiedInterval(modelItems, func(item reporter.CaseResult) float64 { return boolValue(item.Score.StrictRootCause) }),
			RecoverySuccess:   stratifiedInterval(modelItems, func(item reporter.CaseResult) float64 { return boolValue(item.VerificationOK) }),
			MeanCost:          stratifiedInterval(modelItems, func(item reporter.CaseResult) float64 { return item.EstimatedModelCost }),
			MeanLatency:       stratifiedInterval(modelItems, func(item reporter.CaseResult) float64 { return item.Duration.Seconds() }),
			P95Latency:        stratifiedP95Interval(modelItems),
			Breakdowns:        buildBreakdowns(modelItems),
		})
	}
	target, hasTarget := cases[domain.DiagnosisMethodActive]
	if !hasTarget {
		return report, nil
	}
	for _, baseline := range strategies {
		if baseline == domain.DiagnosisMethodActive {
			continue
		}
		if _, ok := cases[baseline]; !ok {
			continue
		}
		paired, err := pairCases(cases[baseline], target)
		if err != nil {
			return Report{}, err
		}
		report.Comparisons = append(report.Comparisons,
			binaryComparison(baseline, domain.DiagnosisMethodActive, "strict_diagnosis_accuracy", paired, func(item reporter.CaseResult) bool { return item.Score.StrictRootCause }),
			binaryComparison(baseline, domain.DiagnosisMethodActive, "recovery_success", paired, func(item reporter.CaseResult) bool { return item.VerificationOK }),
			continuousComparison(baseline, domain.DiagnosisMethodActive, "model_cost", paired, func(item reporter.CaseResult) float64 { return item.EstimatedModelCost }),
			continuousComparison(baseline, domain.DiagnosisMethodActive, "latency", paired, func(item reporter.CaseResult) float64 { return item.Duration.Seconds() }),
		)
	}
	applyHolmCorrection(report.Comparisons)
	return report, nil
}

func buildBreakdowns(items []reporter.CaseResult) []SliceResult {
	dimensions := []struct {
		name  string
		value func(reporter.CaseResult) string
	}{
		{name: "category", value: func(item reporter.CaseResult) string { return item.Category }},
		{name: "root_cause_variant", value: func(item reporter.CaseResult) string { return item.RootCauseVariant }},
		{name: "service", value: func(item reporter.CaseResult) string { return item.Service }},
		{name: "resource", value: func(item reporter.CaseResult) string { return item.Resource }},
	}
	var out []SliceResult
	for _, dimension := range dimensions {
		groups := map[string][]reporter.CaseResult{}
		for _, item := range items {
			value := dimension.value(item)
			if value == "" {
				value = "unspecified"
			}
			groups[value] = append(groups[value], item)
		}
		values := make([]string, 0, len(groups))
		for value := range groups {
			values = append(values, value)
		}
		sort.Strings(values)
		for _, value := range values {
			group := groups[value]
			out = append(out, SliceResult{
				Dimension:         dimension.name,
				Value:             value,
				Cases:             len(group),
				DiagnosisAccuracy: stratifiedInterval(group, func(item reporter.CaseResult) float64 { return boolValue(item.Score.StrictRootCause) }),
				RecoverySuccess:   stratifiedInterval(group, func(item reporter.CaseResult) float64 { return boolValue(item.VerificationOK) }),
				MeanCost:          stratifiedInterval(group, func(item reporter.CaseResult) float64 { return item.EstimatedModelCost }),
				P95Latency:        stratifiedP95Interval(group),
			})
		}
	}
	return out
}

func percentile95(items []reporter.CaseResult) float64 {
	if len(items) == 0 {
		return 0
	}
	values := make([]float64, 0, len(items))
	for _, item := range items {
		values = append(values, item.Duration.Seconds())
	}
	return percentile95Values(values)
}

func percentile95Values(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	index := int(math.Ceil(.95*float64(len(values)))) - 1
	if index < 0 {
		index = 0
	}
	return values[index]
}

func stratifiedP95Interval(items []reporter.CaseResult) Interval {
	if len(items) == 0 {
		return Interval{}
	}
	groups := map[string][]reporter.CaseResult{}
	keys := []string{}
	for _, item := range items {
		if _, exists := groups[item.Category]; !exists {
			keys = append(keys, item.Category)
		}
		groups[item.Category] = append(groups[item.Category], item)
	}
	sort.Strings(keys)
	state := uint64(0x4b75626550696c6f)
	distribution := make([]float64, 0, bootstrapSamples)
	for range bootstrapSamples {
		values := make([]float64, 0, len(items))
		for _, key := range keys {
			group := groups[key]
			for range len(group) {
				state = state*6364136223846793005 + 1442695040888963407
				index := int((state >> 33) % uint64(len(group)))
				values = append(values, group[index].Duration.Seconds())
			}
		}
		distribution = append(distribution, percentile95Values(values))
	}
	return intervalFromDistribution(percentile95(items), distribution)
}

func validateStrategyFootprint(strategy string, items []reporter.CaseResult) error {
	for _, item := range items {
		if item.InfrastructureFailure || item.IncidentID == "" {
			continue
		}
		switch strategy {
		case domain.DiagnosisMethodDirect:
			if item.Architecture != "single-pass" || item.PlannerTasks != 0 || item.WorkerFindings != 0 || item.DebateRounds != 0 || item.MemoryReads != 0 {
				return fmt.Errorf("direct strategy produced an invalid execution footprint for %s", caseKey(item))
			}
		case domain.DiagnosisMethodRAG:
			if item.Architecture != "single-pass-episodic" || item.MemoryReads != 1 || item.PlannerTasks != 0 || item.WorkerFindings != 0 || item.DebateRounds != 0 {
				return fmt.Errorf("rag strategy produced an invalid execution footprint for %s", caseKey(item))
			}
		case domain.DiagnosisMethodReAct:
			if item.Architecture != "single-react" || item.PlannerTasks != 0 || item.WorkerFindings != 0 || item.DebateRounds != 0 || item.MemoryReads != 0 {
				return fmt.Errorf("react strategy produced an invalid execution footprint for %s", caseKey(item))
			}
		case domain.DiagnosisMethodRuleOnly:
			if item.Architecture != "eino-rule-diagnosis-runtime" || item.PlannerTasks == 0 || item.WorkerFindings == 0 || item.DebateRounds != 0 || item.MemoryReads != 0 {
				return fmt.Errorf("rule-only strategy produced an invalid execution footprint for %s", caseKey(item))
			}
		case domain.DiagnosisMethodEvidence:
			if item.Architecture != "eino-evidence-diagnosis-runtime" || item.PlannerTasks == 0 || item.WorkerFindings == 0 || item.DebateRounds != 0 || item.MemoryReads != 0 {
				return fmt.Errorf("evidence-only strategy produced an invalid execution footprint for %s", caseKey(item))
			}
		case domain.DiagnosisMethodCognitive:
			if item.Architecture != domain.WorkflowRuntimeName || item.PlannerTasks == 0 || item.WorkerFindings == 0 || item.DebateRounds != 0 || item.MemoryReads != 0 {
				return fmt.Errorf("cognitive strategy produced an invalid execution footprint for %s", caseKey(item))
			}
		case domain.DiagnosisMethodActive, domain.DiagnosisMethodKubePilot:
			if item.Architecture != domain.WorkflowRuntimeName || item.PlannerTasks == 0 || item.WorkerFindings == 0 || item.DebateRounds != 0 || item.MemoryReads != 0 {
				return fmt.Errorf("active-diagnosis strategy produced an invalid execution footprint for %s", caseKey(item))
			}
		}
	}
	return nil
}

type pair struct {
	category       string
	baseline, goal reporter.CaseResult
}

func pairCases(baseline, target []reporter.CaseResult) ([]pair, error) {
	byKey := map[string]reporter.CaseResult{}
	for _, item := range baseline {
		if item.InfrastructureFailure {
			continue
		}
		byKey[caseKey(item)] = item
	}
	var pairs []pair
	for _, item := range target {
		if item.InfrastructureFailure {
			continue
		}
		base, ok := byKey[caseKey(item)]
		if !ok {
			continue
		}
		pairs = append(pairs, pair{category: item.Category, baseline: base, goal: item})
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("comparison has no complete paired cases")
	}
	return pairs, nil
}

func binaryComparison(baseline, target, metric string, pairs []pair, value func(reporter.CaseResult) bool) PairwiseResult {
	var baselineMean, targetMean float64
	var baselineOnly, targetOnly int
	for _, item := range pairs {
		left, right := value(item.baseline), value(item.goal)
		baselineMean += boolValue(left)
		targetMean += boolValue(right)
		if left && !right {
			baselineOnly++
		} else if !left && right {
			targetOnly++
		}
	}
	baselineMean /= float64(len(pairs))
	targetMean /= float64(len(pairs))
	difference := pairedInterval(pairs, func(item pair) float64 { return boolValue(value(item.goal)) - boolValue(value(item.baseline)) })
	return PairwiseResult{Baseline: baseline, Target: target, Metric: metric, Pairs: len(pairs), Difference: difference, RelativeImprovement: relativeChange(baselineMean, targetMean), Test: "paired McNemar", PValue: mcnemarExact(baselineOnly, targetOnly), EffectSize: difference.Estimate}
}

func continuousComparison(baseline, target, metric string, pairs []pair, value func(reporter.CaseResult) float64) PairwiseResult {
	differences := make([]float64, 0, len(pairs))
	var baselineMean, targetMean float64
	for _, item := range pairs {
		left, right := value(item.baseline), value(item.goal)
		baselineMean += left
		targetMean += right
		differences = append(differences, right-left)
	}
	baselineMean /= float64(len(pairs))
	targetMean /= float64(len(pairs))
	interval := pairedInterval(pairs, func(item pair) float64 { return value(item.goal) - value(item.baseline) })
	pValue, effect := wilcoxonSignedRank(differences)
	return PairwiseResult{Baseline: baseline, Target: target, Metric: metric, Pairs: len(pairs), Difference: interval, RelativeImprovement: relativeChange(baselineMean, targetMean), Test: "paired Wilcoxon", PValue: pValue, EffectSize: effect}
}

func stratifiedInterval(items []reporter.CaseResult, value func(reporter.CaseResult) float64) Interval {
	if len(items) == 0 {
		return Interval{}
	}
	groups := map[string][]reporter.CaseResult{}
	var total float64
	for _, item := range items {
		groups[item.Category] = append(groups[item.Category], item)
		total += value(item)
	}
	distribution := bootstrap(groups, func(item reporter.CaseResult) float64 { return value(item) })
	return intervalFromDistribution(total/float64(len(items)), distribution)
}

func pairedInterval(items []pair, value func(pair) float64) Interval {
	groups := map[string][]pair{}
	var total float64
	for _, item := range items {
		groups[item.category] = append(groups[item.category], item)
		total += value(item)
	}
	distribution := bootstrap(groups, value)
	return intervalFromDistribution(total/float64(len(items)), distribution)
}

func bootstrap[T any](groups map[string][]T, value func(T) float64) []float64 {
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	state := uint64(0x4b75626550696c6f)
	distribution := make([]float64, 0, bootstrapSamples)
	for range bootstrapSamples {
		var total float64
		count := 0
		for _, key := range keys {
			group := groups[key]
			for range len(group) {
				state = state*6364136223846793005 + 1442695040888963407
				index := int((state >> 33) % uint64(len(group)))
				total += value(group[index])
				count++
			}
		}
		distribution = append(distribution, total/float64(count))
	}
	return distribution
}

func intervalFromDistribution(estimate float64, distribution []float64) Interval {
	sort.Float64s(distribution)
	return Interval{Estimate: estimate, Lower: distribution[int(.025*float64(len(distribution)))], Upper: distribution[int(.975*float64(len(distribution))-1)]}
}

func mcnemarExact(baselineOnly, targetOnly int) float64 {
	n := baselineOnly + targetOnly
	if n == 0 {
		return 1
	}
	k := min(baselineOnly, targetOnly)
	probability := math.Pow(.5, float64(n))
	sum := probability
	term := probability
	for index := 1; index <= k; index++ {
		term *= float64(n-index+1) / float64(index)
		sum += term
	}
	return min(1, 2*sum)
}

func wilcoxonSignedRank(values []float64) (float64, float64) {
	type ranked struct{ absolute, signed float64 }
	var samples []ranked
	for _, value := range values {
		if math.Abs(value) > 1e-12 {
			samples = append(samples, ranked{absolute: math.Abs(value), signed: value})
		}
	}
	if len(samples) == 0 {
		return 1, 0
	}
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].absolute < samples[j].absolute })
	var positive, negative float64
	for start := 0; start < len(samples); {
		end := start + 1
		for end < len(samples) && math.Abs(samples[end].absolute-samples[start].absolute) < 1e-12 {
			end++
		}
		rank := float64(start+1+end) / 2
		for index := start; index < end; index++ {
			if samples[index].signed > 0 {
				positive += rank
			} else {
				negative += rank
			}
		}
		start = end
	}
	n := float64(len(samples))
	mean := n * (n + 1) / 4
	variance := n * (n + 1) * (2*n + 1) / 24
	z := (positive - mean) / math.Sqrt(variance)
	p := math.Erfc(math.Abs(z) / math.Sqrt2)
	effect := (positive - negative) / (positive + negative)
	return p, effect
}

func applyHolmCorrection(items []PairwiseResult) {
	indices := make([]int, len(items))
	for index := range indices {
		indices[index] = index
	}
	sort.Slice(indices, func(i, j int) bool { return items[indices[i]].PValue < items[indices[j]].PValue })
	previous := 0.0
	for rank, index := range indices {
		adjusted := math.Min(1, float64(len(items)-rank)*items[index].PValue)
		if adjusted < previous {
			adjusted = previous
		}
		items[index].HolmAdjustedPValue = adjusted
		previous = adjusted
	}
}

func caseKey(item reporter.CaseResult) string {
	return fmt.Sprintf("%s|%d|%d", item.CaseID, item.Seed, item.Repetition)
}

func withoutInfrastructureFailures(items []reporter.CaseResult) []reporter.CaseResult {
	out := make([]reporter.CaseResult, 0, len(items))
	for _, item := range items {
		if !item.InfrastructureFailure {
			out = append(out, item)
		}
	}
	return out
}

func boolValue(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func relativeChange(baseline, target float64) float64 {
	if math.Abs(baseline) < 1e-12 {
		return 0
	}
	return (target - baseline) / math.Abs(baseline)
}
