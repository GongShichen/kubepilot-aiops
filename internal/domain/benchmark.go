package domain

import "time"

type BenchmarkRun struct {
	ID             string    `json:"id"`
	Profile        string    `json:"profile"`
	Status         string    `json:"status"`
	Strategies     []string  `json:"strategies,omitempty"`
	DatasetSplit   string    `json:"dataset_split,omitempty"`
	Seeds          []int64   `json:"seeds,omitempty"`
	Repetitions    int       `json:"repetitions,omitempty"`
	ModelProfile   string    `json:"model_profile,omitempty"`
	AutoApprove    bool      `json:"auto_approve"`
	Output         []string  `json:"output,omitempty"`
	Error          string    `json:"error,omitempty"`
	ArtifactRoot   string    `json:"artifact_root"`
	ResultArtifact string    `json:"result_artifact,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type BenchmarkCaseResult struct {
	RunID      string `json:"run_id"`
	StrategyID string `json:"strategy_id"`
	CaseID     string `json:"case_id"`
	Seed       int64  `json:"seed"`
	Repetition int    `json:"repetition"`
	Status     string `json:"status"`
	Result     []byte `json:"-"`
}
