package measurement

import cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"

// MeasurementRun records the physical test execution independently from the
// evidence producers which observed it.
type MeasurementRun struct {
	Name     string            `json:"name"`
	Run      TestRun           `json:"run"`
	Packages map[string]string `json:"packages"`
}

// ResultStatus is one independent D28 outcome axis. Not-requested is distinct
// from not-run so callers never have to infer whether a policy was enabled.
type ResultStatus string

const (
	ResultPassed       ResultStatus = "passed"
	ResultFailed       ResultStatus = "failed"
	ResultTimeout      ResultStatus = "timeout"
	ResultNotRun       ResultStatus = "not-run"
	ResultNotRequested ResultStatus = "not-requested"
)

// RunResults preserves the five independent outcomes required by D28 even
// when the process exit code collapses them according to precedence.
type RunResults struct {
	Test        ResultStatus `json:"test"`
	Measurement ResultStatus `json:"measurement"`
	Integrity   ResultStatus `json:"integrity"`
	Strict      ResultStatus `json:"strict"`
	Threshold   ResultStatus `json:"threshold"`
}

// TestRun describes one go test execution independently from coverage and
// from the overall policy results.
type TestRun struct {
	Status      cover.RunStatus      `json:"status"`
	FailureKind cover.RunFailureKind `json:"failureKind"`
	Complete    bool                 `json:"complete"`
}

// Run describes the aggregate test execution and independent D28 outcomes.
type ExecutionSummary struct {
	Status      cover.RunStatus      `json:"status"`
	FailureKind cover.RunFailureKind `json:"failureKind"`
	Complete    bool                 `json:"complete"`
	Results     RunResults           `json:"results"`
}
