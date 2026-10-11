// Package evidence accepts observations against their original-source ownership.
package evidence

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	cover "github.com/shrydev2020/gomcdc/v2/internal/coverage"
	"github.com/shrydev2020/gomcdc/v2/internal/mcdc"
	"github.com/shrydev2020/gomcdc/v2/internal/runtimecov"
)

// Runtime is the only runtime evidence allowed to reach report
// construction. Transport provenance has been accepted against the requested
// run and source inventory, while ClauseObservations are the provenance-free,
// idempotent domain projection used for coverage aggregation.
type Runtime struct {
	Evaluations           []cover.DecisionEvaluation
	NotEvaluatedDecisions []cover.DecisionNotEvaluatedObservation
	ClauseObservations    []cover.ClauseObservation
	Diagnostics           []runtimecov.Diagnostic
	Files                 []runtimecov.ProcessFile
}

// Rejections preserves producer ownership for rejected runtime
// records. Shared transport failures invalidate both runtime producers, while
// AST and compiler mapping failures invalidate only the projection that owns
// the rejected record.
type Rejections struct {
	shared   []error
	ast      []error
	compiler []error
}

func (issues *Rejections) addShared(err error) {
	if err != nil {
		issues.shared = append(issues.shared, err)
	}
}

func (issues *Rejections) addAST(err error) {
	if err != nil {
		issues.ast = append(issues.ast, err)
	}
}

func (issues *Rejections) addCompiler(err error) {
	if err != nil {
		issues.compiler = append(issues.compiler, err)
	}
}

func (issues Rejections) Err() error {
	return errors.Join(errors.Join(issues.shared...), errors.Join(issues.ast...), errors.Join(issues.compiler...))
}

func (issues Rejections) ASTError() error {
	return errors.Join(errors.Join(issues.shared...), errors.Join(issues.ast...))
}

func (issues Rejections) CompilerError() error {
	return errors.Join(errors.Join(issues.shared...), errors.Join(issues.compiler...))
}

func (issues *Rejections) addClause(event cover.ClauseEventKind, err error) {
	switch event {
	case cover.ClauseBodyExecution:
		issues.addAST(err)
	case cover.ClauseDirectSelection, cover.ClauseNoMatchSelection:
		issues.addCompiler(err)
	default:
		issues.addShared(err)
	}
}

func AcceptRuntime(
	ctx context.Context,
	decisions []cover.DecisionMetadata,
	clauses []cover.ClauseMetadata,
	recorded runtimecov.RecordedEvidence,
	runID string,
	noMatches []cover.NoMatchMetadata,
) (Runtime, error) {
	accepted, issues := AcceptRuntimeByProducer(ctx, decisions, clauses, recorded, runID, noMatches)
	return accepted, issues.Err()
}

func AcceptRuntimeByProducer(
	ctx context.Context,
	decisions []cover.DecisionMetadata,
	clauses []cover.ClauseMetadata,
	recorded runtimecov.RecordedEvidence,
	runID string,
	noMatches []cover.NoMatchMetadata,
) (Runtime, Rejections) {
	var issues Rejections
	if err := ctx.Err(); err != nil {
		issues.addShared(err)
		return Runtime{}, issues
	}
	knownDecisions := make(map[cover.DecisionID]cover.DecisionMetadata, len(decisions))
	knownPackages := make(map[string]struct{})
	for _, decision := range decisions {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return Runtime{}, issues
		}
		knownDecisions[decision.ID] = decision
		knownPackages[decision.Package] = struct{}{}
	}
	knownClauses := make(map[cover.ClauseID]cover.ClauseMetadata, len(clauses))
	for _, clause := range clauses {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return Runtime{}, issues
		}
		knownClauses[clause.ID] = clause
		knownPackages[clause.Package] = struct{}{}
	}
	knownNoMatches := make(map[cover.SwitchID]cover.NoMatchMetadata)
	for _, noMatch := range noMatches {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return Runtime{}, issues
		}
		knownNoMatches[noMatch.SwitchID] = noMatch
		knownPackages[noMatch.Package] = struct{}{}
	}
	switchDecisions, orderErr := conditionlessSwitchDecisionOrder(ctx, clauses)
	if orderErr != nil {
		issues.addShared(orderErr)
		return Runtime{}, issues
	}
	accepted := Runtime{
		Diagnostics: append([]runtimecov.Diagnostic(nil), recorded.Diagnostics...),
		Files:       append([]runtimecov.ProcessFile(nil), recorded.Files...),
	}
	for _, file := range recorded.Files {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return accepted, issues
		}
		if file.RunID == "" && file.PackagePath == "" && file.ProcessID == 0 {
			continue
		}
		_, knownPackage := knownPackages[file.PackagePath]
		if file.RunID != runID || !knownPackage || file.ProcessID <= 0 {
			issues.addShared(fmt.Errorf(
				"process event file %q has invalid provenance run=%q package=%q process=%d",
				filepath.Base(file.Path), file.RunID, file.PackagePath, file.ProcessID,
			))
		}
	}
	validEvaluations := make(map[cover.EvaluationIdentity]cover.DecisionEvaluation)
	validEvaluationCandidates := make([]cover.DecisionEvaluation, 0, len(recorded.Evaluations))
	type skipCauseKey struct {
		Identity   cover.EvaluationIdentity
		DecisionID cover.DecisionID
	}
	observedSkips := make(map[skipCauseKey]map[cover.DecisionID]struct{})
	candidateSkips := make(map[skipCauseKey][]cover.DecisionNotEvaluatedObservation)
	for _, evaluation := range recorded.Evaluations {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return accepted, issues
		}
		metadata, exists := knownDecisions[evaluation.DecisionID]
		if !exists {
			issues.addAST(fmt.Errorf("event contains unknown decision ID 0x%016x", uint64(evaluation.DecisionID)))
			continue
		}
		if evaluation.EvaluationID == 0 {
			issues.addAST(fmt.Errorf("decision 0x%016x has reserved evaluation ID zero", uint64(evaluation.DecisionID)))
			continue
		}
		if evaluation.ProcessID <= 0 {
			issues.addAST(fmt.Errorf("decision 0x%016x has invalid process provenance %d", uint64(evaluation.DecisionID), evaluation.ProcessID))
			continue
		}
		if evaluation.RunID != runID {
			issues.addAST(fmt.Errorf("decision 0x%016x belongs to unexpected run %q", uint64(evaluation.DecisionID), evaluation.RunID))
			continue
		}
		if evaluation.PackagePath != metadata.Package {
			issues.addAST(fmt.Errorf(
				"decision 0x%016x belongs to package %q, want %q",
				uint64(evaluation.DecisionID), evaluation.PackagePath, metadata.Package,
			))
			continue
		}
		if len(evaluation.Conditions) != len(metadata.Conditions) {
			issues.addAST(fmt.Errorf(
				"decision 0x%016x has %d condition states, want %d",
				uint64(evaluation.DecisionID), len(evaluation.Conditions), len(metadata.Conditions),
			))
			continue
		}
		if semanticErr := mcdc.ValidateCompletedEvaluation(metadata, evaluation); semanticErr != nil {
			issues.addAST(fmt.Errorf(
				"decision 0x%016x contains impossible completed evidence: %w",
				uint64(evaluation.DecisionID), semanticErr,
			))
			continue
		}
		validEvaluationCandidates = append(validEvaluationCandidates, evaluation)
		validEvaluations[evaluation.Identity()] = evaluation
	}
	for _, observation := range recorded.NotEvaluatedDecisions {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return accepted, issues
		}
		target, targetExists := knownDecisions[observation.DecisionID]
		cause, causeExists := knownDecisions[observation.CauseDecisionID]
		identity := cover.EvaluationIdentity{
			RunID:        observation.RunID,
			PackagePath:  observation.PackagePath,
			ProcessID:    observation.ProcessID,
			EvaluationID: observation.CauseEvaluationID,
		}
		causeEvaluation, evidenceExists := validEvaluations[identity]
		switch {
		case !targetExists:
			issues.addAST(fmt.Errorf("event contains unknown skipped decision ID 0x%016x", uint64(observation.DecisionID)))
			continue
		case !causeExists:
			issues.addAST(fmt.Errorf("event contains unknown skip-cause decision ID 0x%016x", uint64(observation.CauseDecisionID)))
			continue
		case observation.RunID != runID || observation.PackagePath != target.Package || cause.Package != target.Package || observation.ProcessID <= 0:
			issues.addAST(fmt.Errorf("skipped decision 0x%016x has inconsistent run or package provenance", uint64(observation.DecisionID)))
			continue
		case target.Kind != cover.DecisionSwitchCase || cause.Kind != cover.DecisionSwitchCase:
			issues.addAST(fmt.Errorf("skipped decision 0x%016x is not a conditionless-switch decision", uint64(observation.DecisionID)))
			continue
		case !evidenceExists || causeEvaluation.DecisionID != observation.CauseDecisionID || causeEvaluation.Status != cover.EvaluationCompleted || !causeEvaluation.Result:
			issues.addAST(fmt.Errorf("skipped decision 0x%016x has no completed true cause evaluation", uint64(observation.DecisionID)))
			continue
		}
		causeOrder, causeOrdered := switchDecisions[observation.CauseDecisionID]
		targetOrder, targetOrdered := switchDecisions[observation.DecisionID]
		if !causeOrdered || !targetOrdered || causeOrder.groupID != targetOrder.groupID || targetOrder.position <= causeOrder.position {
			issues.addAST(fmt.Errorf("skipped decision 0x%016x is not later in the same conditionless switch", uint64(observation.DecisionID)))
			continue
		}
		key := skipCauseKey{Identity: identity, DecisionID: observation.CauseDecisionID}
		if observedSkips[key] == nil {
			observedSkips[key] = make(map[cover.DecisionID]struct{})
		}
		observedSkips[key][observation.DecisionID] = struct{}{}
		candidateSkips[key] = append(candidateSkips[key], observation)
	}
	for _, evaluation := range validEvaluationCandidates {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return accepted, issues
		}
		identity := evaluation.Identity()
		order, exists := switchDecisions[evaluation.DecisionID]
		if !exists || evaluation.Status != cover.EvaluationCompleted || !evaluation.Result || len(order.suffix) == 0 {
			continue
		}
		observed := observedSkips[skipCauseKey{Identity: identity, DecisionID: evaluation.DecisionID}]
		key := skipCauseKey{Identity: identity, DecisionID: evaluation.DecisionID}
		if len(candidateSkips[key]) != len(observed) {
			issues.addAST(fmt.Errorf("conditionless-switch decision 0x%016x has duplicate skipped-decision evidence", uint64(evaluation.DecisionID)))
			continue
		}
		if len(observed) != len(order.suffix) {
			issues.addAST(fmt.Errorf(
				"conditionless-switch decision 0x%016x skipped %d decisions, want complete suffix of %d",
				uint64(evaluation.DecisionID), len(observed), len(order.suffix),
			))
			continue
		}
		complete := true
		for _, expected := range order.suffix {
			if err := ctx.Err(); err != nil {
				issues.addShared(err)
				return accepted, issues
			}
			if _, found := observed[expected]; !found {
				complete = false
				issues.addAST(fmt.Errorf(
					"conditionless-switch decision 0x%016x omitted skipped decision 0x%016x",
					uint64(evaluation.DecisionID), uint64(expected),
				))
			}
		}
		if complete {
			accepted.NotEvaluatedDecisions = append(accepted.NotEvaluatedDecisions, candidateSkips[key]...)
		}
	}
	var deduplicateErr error
	accepted.Evaluations, deduplicateErr = DeduplicateEvaluations(ctx, validEvaluationCandidates)
	if deduplicateErr != nil {
		issues.addAST(deduplicateErr)
		return accepted, issues
	}
	verifiedClauses := make(map[cover.ClauseObservation]struct{})
	for _, event := range recorded.ClauseEvents {
		if err := ctx.Err(); err != nil {
			issues.addShared(err)
			return accepted, issues
		}
		observation := cover.ClauseObservation{
			SwitchID:         event.SwitchID,
			ClauseID:         event.ClauseID,
			Event:            event.Event,
			AlternativeIndex: event.AlternativeIndex,
			AlternativeKnown: event.AlternativeKnown,
		}
		if event.RunID != runID || event.PackagePath == "" || event.ProcessID <= 0 {
			issues.addClause(observation.Event, fmt.Errorf(
				"clause event 0x%016x has invalid provenance run=%q package=%q process=%d",
				uint64(observation.ClauseID), event.RunID, event.PackagePath, event.ProcessID,
			))
			continue
		}
		if observation.Event == cover.ClauseNoMatchSelection {
			metadata, exists := knownNoMatches[observation.SwitchID]
			if !exists || observation.AlternativeKnown {
				issues.addCompiler(fmt.Errorf("event contains unknown no-match switch ID 0x%016x", uint64(observation.SwitchID)))
				continue
			}
			if event.PackagePath != metadata.Package {
				issues.addCompiler(fmt.Errorf(
					"no-match switch 0x%016x belongs to package %q, want %q",
					uint64(observation.SwitchID), event.PackagePath, metadata.Package,
				))
				continue
			}
			if _, duplicate := verifiedClauses[observation]; !duplicate {
				verifiedClauses[observation] = struct{}{}
				accepted.ClauseObservations = append(accepted.ClauseObservations, observation)
			}
			continue
		}
		metadata, exists := knownClauses[observation.ClauseID]
		if !exists {
			issues.addClause(observation.Event, fmt.Errorf("event contains unknown clause ID 0x%016x", uint64(observation.ClauseID)))
			continue
		}
		if event.PackagePath != metadata.Package {
			issues.addClause(observation.Event, fmt.Errorf(
				"clause 0x%016x belongs to package %q, want %q",
				uint64(observation.ClauseID), event.PackagePath, metadata.Package,
			))
			continue
		}
		if observation.SwitchID != 0 && observation.SwitchID != metadata.SwitchID {
			issues.addClause(observation.Event, fmt.Errorf("clause 0x%016x has inconsistent switch ID", uint64(observation.ClauseID)))
			continue
		}
		switch observation.Event {
		case cover.ClauseBodyExecution:
			if observation.AlternativeKnown {
				issues.addAST(fmt.Errorf("clause 0x%016x body event carries a case alternative", uint64(observation.ClauseID)))
				continue
			}
		case cover.ClauseDirectSelection:
			if metadata.Kind != cover.ClauseExpressionSwitch && metadata.Kind != cover.ClauseTypeSwitch {
				issues.addCompiler(fmt.Errorf("clause 0x%016x kind %q cannot carry direct-selection evidence", uint64(observation.ClauseID), metadata.Kind))
				continue
			}
			if observation.SwitchID == 0 || observation.SwitchID != metadata.SwitchID {
				issues.addCompiler(fmt.Errorf("clause 0x%016x direct selection has inconsistent switch ID", uint64(observation.ClauseID)))
				continue
			}
			alternatives := len(metadata.Expressions)
			if metadata.Kind == cover.ClauseTypeSwitch {
				alternatives = len(metadata.Types)
			}
			if metadata.Role == cover.ClauseDefault {
				if observation.AlternativeKnown {
					issues.addCompiler(fmt.Errorf("default clause 0x%016x carries a case alternative", uint64(observation.ClauseID)))
					continue
				}
			} else if !observation.AlternativeKnown || int(observation.AlternativeIndex) >= alternatives {
				issues.addCompiler(fmt.Errorf("clause 0x%016x carries an invalid case alternative", uint64(observation.ClauseID)))
				continue
			}
		default:
			issues.addShared(fmt.Errorf("clause 0x%016x has unsupported event %q", uint64(observation.ClauseID), observation.Event))
			continue
		}
		if _, duplicate := verifiedClauses[observation]; !duplicate {
			verifiedClauses[observation] = struct{}{}
			accepted.ClauseObservations = append(accepted.ClauseObservations, observation)
		}
	}
	return accepted, issues
}

type acceptedEvaluationKey struct {
	RunID       string
	PackagePath string
	DecisionID  cover.DecisionID
	Conditions  string
	Result      bool
	Status      cover.EvaluationStatus
	TestID      string
	ProcessID   int
}

func DeduplicateEvaluations(ctx context.Context, evaluations []cover.DecisionEvaluation) ([]cover.DecisionEvaluation, error) {
	result := make([]cover.DecisionEvaluation, 0, len(evaluations))
	seen := make(map[acceptedEvaluationKey]struct{}, len(evaluations))
	for _, evaluation := range evaluations {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		encodedConditions := make([]byte, len(evaluation.Conditions))
		for index, condition := range evaluation.Conditions {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			encodedConditions[index] = byte(condition)
		}
		key := acceptedEvaluationKey{
			RunID: evaluation.RunID, PackagePath: evaluation.PackagePath,
			DecisionID: evaluation.DecisionID, Conditions: string(encodedConditions),
			Result: evaluation.Result, Status: evaluation.Status,
			TestID: evaluation.TestID, ProcessID: evaluation.ProcessID,
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, evaluation)
	}
	return result, nil
}

type switchDecisionOrder struct {
	groupID  cover.ClauseGroupID
	position int
	suffix   []cover.DecisionID
}

func conditionlessSwitchDecisionOrder(ctx context.Context, clauses []cover.ClauseMetadata) (map[cover.DecisionID]switchDecisionOrder, error) {
	groups := make(map[cover.ClauseGroupID][]cover.ClauseMetadata)
	for _, clause := range clauses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if clause.Kind == cover.ClauseConditionlessSwitch && clause.GroupID != 0 {
			groups[clause.GroupID] = append(groups[clause.GroupID], clause)
		}
	}
	result := make(map[cover.DecisionID]switchDecisionOrder)
	for groupID, members := range groups {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sort.Slice(members, func(i, j int) bool { return members[i].Index < members[j].Index })
		var sequence []cover.DecisionID
		for _, clause := range members {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			sequence = append(sequence, clause.DecisionIDs...)
		}
		for position, decisionID := range sequence {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			result[decisionID] = switchDecisionOrder{
				groupID:  groupID,
				position: position,
				suffix:   append([]cover.DecisionID(nil), sequence[position+1:]...),
			}
		}
	}
	return result, nil
}
