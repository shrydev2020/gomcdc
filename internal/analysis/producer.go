package analysis

import (
	"sort"

	"github.com/shrydev2020/gomcdc/v2/internal/evidence"
)

type ProducerName = evidence.ProducerName
type ProducerIntegrity = evidence.ProducerIntegrity
type ProducerCompleteness = evidence.ProducerCompleteness
type ProducerMapping = evidence.ProducerMapping
type ProducerUsability = evidence.ProducerUsability
type ProducerOutcome = evidence.ProducerOutcome

const (
	ProducerGoCover                  = evidence.ProducerGoCover
	ProducerASTRuntime               = evidence.ProducerASTRuntime
	ProducerCompilerSelection        = evidence.ProducerCompilerSelection
	ProducerIntegrityValid           = evidence.ProducerIntegrityValid
	ProducerIntegrityValidPrefix     = evidence.ProducerIntegrityValidPrefix
	ProducerIntegrityInvalid         = evidence.ProducerIntegrityInvalid
	ProducerIntegrityUnavailable     = evidence.ProducerIntegrityUnavailable
	ProducerCompletenessComplete     = evidence.ProducerCompletenessComplete
	ProducerCompletenessPartial      = evidence.ProducerCompletenessPartial
	ProducerCompletenessUnavailable  = evidence.ProducerCompletenessUnavailable
	ProducerMappingComplete          = evidence.ProducerMappingComplete
	ProducerMappingInvalid           = evidence.ProducerMappingInvalid
	ProducerMappingUnavailable       = evidence.ProducerMappingUnavailable
	ProducerUsabilityAccepted        = evidence.ProducerUsabilityAccepted
	ProducerUsabilityAcceptedPartial = evidence.ProducerUsabilityAcceptedPartial
	ProducerUsabilityRejected        = evidence.ProducerUsabilityRejected
)

func cloneProducerOutcomes(values []ProducerOutcome) []ProducerOutcome {
	cloned := append(make([]ProducerOutcome, 0, len(values)), values...)
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Producer < cloned[j].Producer })
	return cloned
}

func producerRejectsEvidence(values []ProducerOutcome, producer ProducerName) bool {
	for _, outcome := range values {
		if outcome.Producer == producer {
			return !outcome.AllowsCoverage()
		}
	}
	// Absence means the producer was not requested. The caller owns metric
	// selection, so report construction does not invent a failure here.
	return false
}
