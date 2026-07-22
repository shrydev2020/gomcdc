// Package transformmap records byte provenance between immutable source
// revisions. It describes coordinate history only: consumers remain
// responsible for deciding whether mapped data is valid coverage evidence.
package transformmap

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
)

// CoordinateUnit identifies the coordinate system used by every Range.
type CoordinateUnit string

const (
	// CoordinateBytes is a zero-based byte offset in an immutable revision.
	CoordinateBytes CoordinateUnit = "byte-offset"
)

// Revision identifies immutable bytes within one named transformation stage.
// Its private fields keep callers from manufacturing an identity that does
// not match the bytes hashed by NewRevision.
type Revision struct {
	identity string
	size     uint64
	digest   [sha256.Size]byte
}

// NewRevision hashes contents under an explicit stage-local identity.
func NewRevision(identity string, contents []byte) (Revision, error) {
	if identity == "" {
		return Revision{}, errors.New("transform revision identity is empty")
	}
	return Revision{identity: identity, size: uint64(len(contents)), digest: sha256.Sum256(contents)}, nil
}

// Identity returns the caller-supplied revision identity.
func (revision Revision) Identity() string { return revision.identity }

// Size returns the revision length in bytes.
func (revision Revision) Size() uint64 { return revision.size }

// Unit returns the only coordinate unit accepted by Map.
func (revision Revision) Unit() CoordinateUnit { return CoordinateBytes }

// Digest returns the immutable content digest.
func (revision Revision) Digest() [sha256.Size]byte { return revision.digest }

// Valid reports whether the revision was constructed by NewRevision.
func (revision Revision) Valid() bool { return revision.identity != "" }

// Range is a half-open [Start, End) byte interval.
type Range struct {
	Start uint64
	End   uint64
}

// RelationKind states what can be known about bytes on the two sides of one
// source transformation. No kind grants coverage authority.
type RelationKind string

const (
	// Preserved means one equal-length source range is copied losslessly to one
	// target range.
	Preserved RelationKind = "preserved"
	// Synthetic means target bytes have no source range.
	Synthetic RelationKind = "synthetic"
	// Deleted means source bytes have no target range.
	Deleted RelationKind = "deleted"
	// Ambiguous means candidate source/target ranges are known but a unique
	// correspondence is not.
	Ambiguous RelationKind = "ambiguous"
	// Lossy means the participating ranges are known but exact byte-level
	// inversion is impossible. One-to-many and many-to-one transformations use
	// this kind unless they are genuinely ambiguous.
	Lossy RelationKind = "lossy"
)

// Relation connects non-overlapping source and target ranges. Multiple ranges
// within one relation explicitly represent one-to-many or many-to-one history.
type Relation struct {
	Kind    RelationKind
	Sources []Range
	Targets []Range
}

// Map is a validated, total relation between two immutable revisions. It owns
// private copies of all ranges and orders relations deterministically.
type Map struct {
	source    Revision
	target    Revision
	relations []Relation
}

// New validates and takes an immutable copy of a complete source-to-target
// relation. Every byte in each revision must occur in exactly one relation;
// gaps and overlaps fail instead of being inferred.
func New(source, target Revision, relations []Relation) (Map, error) {
	if !source.Valid() || !target.Valid() {
		return Map{}, errors.New("transform map requires valid source and target revisions")
	}
	cloned := cloneRelations(relations)
	for index := range cloned {
		if err := validateRelation(cloned[index], source.size, target.size); err != nil {
			return Map{}, fmt.Errorf("transform relation %d: %w", index, err)
		}
	}
	sortRelations(cloned)
	if err := validatePartition("source", source.size, cloned, func(relation Relation) []Range { return relation.Sources }); err != nil {
		return Map{}, err
	}
	if err := validatePartition("target", target.size, cloned, func(relation Relation) []Range { return relation.Targets }); err != nil {
		return Map{}, err
	}
	return Map{source: source, target: target, relations: cloned}, nil
}

// Source returns the immutable source revision.
func (mapping Map) Source() Revision { return mapping.source }

// Target returns the immutable target revision.
func (mapping Map) Target() Revision { return mapping.target }

// Relations returns a deep copy in deterministic source/target order.
func (mapping Map) Relations() []Relation { return cloneRelations(mapping.relations) }

// Compose constructs source→next.target history only when the intermediate
// revision identity, size, and digest are identical. Relation boundaries must
// also align exactly; the function refuses to guess through a partial range.
func (mapping Map) Compose(next Map) (Map, error) {
	if mapping.target != next.source {
		return Map{}, fmt.Errorf(
			"compose transform maps: intermediate revision mismatch: %q != %q",
			mapping.target.identity,
			next.source.identity,
		)
	}

	used := make([]bool, len(next.relations))
	composed := make([]Relation, 0, len(mapping.relations)+len(next.relations))
	for _, first := range mapping.relations {
		if len(first.Targets) == 0 {
			composed = append(composed, Relation{Kind: Deleted, Sources: cloneRanges(first.Sources)})
			continue
		}
		match := -1
		for index, second := range next.relations {
			if sameRanges(first.Targets, second.Sources) {
				if match >= 0 {
					return Map{}, fmt.Errorf("compose transform maps: intermediate ranges %v match multiple relations", first.Targets)
				}
				match = index
			}
		}
		if match < 0 {
			return Map{}, fmt.Errorf("compose transform maps: intermediate ranges %v do not align with the next map", first.Targets)
		}
		used[match] = true
		second := next.relations[match]
		if len(first.Sources) == 0 && len(second.Targets) == 0 {
			continue
		}
		composed = append(composed, Relation{
			Kind:    composedKind(first.Kind, second.Kind, first.Sources, second.Targets),
			Sources: cloneRanges(first.Sources),
			Targets: cloneRanges(second.Targets),
		})
	}
	for index, second := range next.relations {
		if used[index] {
			continue
		}
		if len(second.Sources) != 0 {
			return Map{}, fmt.Errorf("compose transform maps: next relation source ranges %v are unmatched", second.Sources)
		}
		composed = append(composed, Relation{Kind: Synthetic, Targets: cloneRanges(second.Targets)})
	}
	return New(mapping.source, next.target, composed)
}

func composedKind(first, second RelationKind, sources, targets []Range) RelationKind {
	switch {
	case len(sources) == 0:
		return Synthetic
	case len(targets) == 0:
		return Deleted
	case first == Ambiguous || second == Ambiguous:
		return Ambiguous
	case first == Lossy || second == Lossy:
		return Lossy
	case first == Preserved && second == Preserved:
		return Preserved
	default:
		return Lossy
	}
}

func validateRelation(relation Relation, sourceSize, targetSize uint64) error {
	if !validKind(relation.Kind) {
		return fmt.Errorf("invalid relation kind %q", relation.Kind)
	}
	if err := validateRanges("source", relation.Sources, sourceSize); err != nil {
		return err
	}
	if err := validateRanges("target", relation.Targets, targetSize); err != nil {
		return err
	}
	switch relation.Kind {
	case Preserved:
		if len(relation.Sources) != 1 || len(relation.Targets) != 1 || rangeLength(relation.Sources[0]) != rangeLength(relation.Targets[0]) {
			return errors.New("preserved relation requires one equal-length source and target range")
		}
	case Synthetic:
		if len(relation.Sources) != 0 || len(relation.Targets) == 0 {
			return errors.New("synthetic relation requires targets and no sources")
		}
	case Deleted:
		if len(relation.Sources) == 0 || len(relation.Targets) != 0 {
			return errors.New("deleted relation requires sources and no targets")
		}
	case Ambiguous, Lossy:
		if len(relation.Sources) == 0 || len(relation.Targets) == 0 {
			return fmt.Errorf("%s relation requires both source and target ranges", relation.Kind)
		}
	}
	return nil
}

func rangeLength(source Range) uint64 { return source.End - source.Start }

func validKind(kind RelationKind) bool {
	switch kind {
	case Preserved, Synthetic, Deleted, Ambiguous, Lossy:
		return true
	default:
		return false
	}
}

func validateRanges(name string, ranges []Range, size uint64) error {
	ordered := cloneRanges(ranges)
	sort.Slice(ordered, func(i, j int) bool { return lessRange(ordered[i], ordered[j]) })
	for index, current := range ordered {
		if current.Start >= current.End || current.End > size {
			return fmt.Errorf("invalid %s range %v for revision size %d", name, current, size)
		}
		if index > 0 && ordered[index-1].End > current.Start {
			return fmt.Errorf("overlapping %s ranges %v and %v", name, ordered[index-1], current)
		}
	}
	return nil
}

func validatePartition(name string, size uint64, relations []Relation, selectRanges func(Relation) []Range) error {
	var ranges []Range
	for _, relation := range relations {
		ranges = append(ranges, selectRanges(relation)...)
	}
	sort.Slice(ranges, func(i, j int) bool { return lessRange(ranges[i], ranges[j]) })
	position := uint64(0)
	for _, current := range ranges {
		if current.Start != position {
			return fmt.Errorf("transform map %s partition has gap or overlap at byte %d", name, position)
		}
		position = current.End
	}
	if position != size {
		return fmt.Errorf("transform map %s partition ends at byte %d, want %d", name, position, size)
	}
	return nil
}

func sortRelations(relations []Relation) {
	for index := range relations {
		sort.Slice(relations[index].Sources, func(i, j int) bool { return lessRange(relations[index].Sources[i], relations[index].Sources[j]) })
		sort.Slice(relations[index].Targets, func(i, j int) bool { return lessRange(relations[index].Targets[i], relations[index].Targets[j]) })
	}
	sort.Slice(relations, func(i, j int) bool {
		leftSource, leftHasSource := firstRange(relations[i].Sources)
		rightSource, rightHasSource := firstRange(relations[j].Sources)
		if leftHasSource != rightHasSource {
			return leftHasSource
		}
		if leftHasSource && leftSource != rightSource {
			return lessRange(leftSource, rightSource)
		}
		leftTarget, _ := firstRange(relations[i].Targets)
		rightTarget, _ := firstRange(relations[j].Targets)
		if leftTarget != rightTarget {
			return lessRange(leftTarget, rightTarget)
		}
		return relations[i].Kind < relations[j].Kind
	})
}

func firstRange(ranges []Range) (Range, bool) {
	if len(ranges) == 0 {
		return Range{}, false
	}
	return ranges[0], true
}

func lessRange(left, right Range) bool {
	if left.Start != right.Start {
		return left.Start < right.Start
	}
	return left.End < right.End
}

func sameRanges(left, right []Range) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneRelations(relations []Relation) []Relation {
	cloned := make([]Relation, len(relations))
	for index, relation := range relations {
		cloned[index] = Relation{Kind: relation.Kind, Sources: cloneRanges(relation.Sources), Targets: cloneRanges(relation.Targets)}
	}
	return cloned
}

func cloneRanges(ranges []Range) []Range { return append([]Range(nil), ranges...) }
