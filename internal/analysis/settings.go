package analysis

import "github.com/shrydev2020/gomcdc/v2/internal/mcdc"

// Settings owns the effective inputs to coverage analysis. Report fields are
// descriptive outputs and must not be used to recover these inputs.
type Settings struct {
	MaskingAnalysisLimits MaskingAnalysisLimits `json:"maskingAnalysisLimits"`
}

func NewSettings(budget mcdc.AnalysisBudget) Settings {
	effective := mcdc.EffectiveMaskingAnalysisBudget(budget)
	return Settings{MaskingAnalysisLimits: MaskingAnalysisLimits{
		MaxEvaluationPairs: effective.MaxEvaluationPairs,
		MaxSearchStates:    effective.MaxSearchStates,
		MaxSolverBytes:     effective.MaxSolverBytes,
	}}
}

func (s Settings) MaskingBudget() mcdc.AnalysisBudget {
	return mcdc.AnalysisBudget{
		MaxEvaluationPairs: s.MaskingAnalysisLimits.MaxEvaluationPairs,
		MaxSearchStates:    s.MaskingAnalysisLimits.MaxSearchStates,
		MaxSolverBytes:     s.MaskingAnalysisLimits.MaxSolverBytes,
	}
}
