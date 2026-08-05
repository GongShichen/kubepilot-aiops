package suite

import "context"

// SectionRunner is the only execution dependency accepted by the suite.
// A live runner can be backed by the public Agent API; an offline evaluator
// can be used for deterministic component tests. Neither path gets access to
// evaluator-only truth through this contract.
type SectionRunner interface {
	Run(context.Context) (Section, error)
}

type Runner struct {
	Sections []SectionRunner
}

func (r Runner) Run(ctx context.Context) (Report, error) {
	report := Report{Name: Name, Version: "autonomous-sre"}
	for _, section := range r.Sections {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		result, err := section.Run(ctx)
		if err != nil {
			return report, err
		}
		report.Sections = append(report.Sections, result)
	}
	return report, nil
}
