package report

import "sort"

// ProducerName identifies one independent source of measurement evidence.
// A producer is not a test run: several producers may observe the same run.
type ProducerName string

const (
	ProducerGoCover           ProducerName = "go-cover"
	ProducerASTRuntime        ProducerName = "ast-runtime"
	ProducerCompilerSelection ProducerName = "compiler-selection"
)

// ProducerIntegrity states whether the producer transport is trustworthy.
// ValidPrefix means a damaged tail was discarded while the accepted prefix
// retained intact provenance and record boundaries.
type ProducerIntegrity string

const (
	ProducerIntegrityValid       ProducerIntegrity = "valid"
	ProducerIntegrityValidPrefix ProducerIntegrity = "valid-prefix"
	ProducerIntegrityInvalid     ProducerIntegrity = "invalid"
	ProducerIntegrityUnavailable ProducerIntegrity = "unavailable"
)

// ProducerCompleteness states how much of the requested execution could have
// produced evidence. It does not decide whether that evidence may be used.
type ProducerCompleteness string

const (
	ProducerCompletenessComplete    ProducerCompleteness = "complete"
	ProducerCompletenessPartial     ProducerCompleteness = "partial"
	ProducerCompletenessUnavailable ProducerCompleteness = "unavailable"
)

// ProducerMapping states whether every accepted producer identity maps to a
// requested original-source obligation without guessing.
type ProducerMapping string

const (
	ProducerMappingComplete    ProducerMapping = "complete"
	ProducerMappingInvalid     ProducerMapping = "invalid"
	ProducerMappingUnavailable ProducerMapping = "unavailable"
)

// ProducerUsability is the execution layer's final decision about whether the
// evidence can reach coverage projection. Report rendering must not infer a
// different decision from the other axes.
type ProducerUsability string

const (
	ProducerUsabilityAccepted        ProducerUsability = "accepted"
	ProducerUsabilityAcceptedPartial ProducerUsability = "accepted-partial"
	ProducerUsabilityRejected        ProducerUsability = "rejected"
)

// ProducerOutcome keeps transport integrity, execution completeness, source
// mapping, and the final projection decision orthogonal.
type ProducerOutcome struct {
	Producer     ProducerName         `json:"producer"`
	Integrity    ProducerIntegrity    `json:"integrity"`
	Completeness ProducerCompleteness `json:"completeness"`
	Mapping      ProducerMapping      `json:"mapping"`
	Usability    ProducerUsability    `json:"usability"`
}

// AllowsCoverage reports the already-decided usability axis. It deliberately
// does not derive usability from the other fields.
func (outcome ProducerOutcome) AllowsCoverage() bool {
	return outcome.Usability == ProducerUsabilityAccepted || outcome.Usability == ProducerUsabilityAcceptedPartial
}

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
