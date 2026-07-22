package transformmap

import (
	"reflect"
	"strings"
	"testing"
)

func TestMapDistinguishesAllRelationKindsAndOwnsCopies(t *testing.T) {
	t.Parallel()
	source := revision(t, "original:p.go", "abcdef")
	target := revision(t, "instrumented:p.go", "ABCDEFghi")
	relations := []Relation{
		{Kind: Preserved, Sources: []Range{{Start: 0, End: 2}}, Targets: []Range{{Start: 0, End: 2}}},
		{Kind: Deleted, Sources: []Range{{Start: 2, End: 3}}},
		{Kind: Ambiguous, Sources: []Range{{Start: 3, End: 4}}, Targets: []Range{{Start: 2, End: 3}}},
		{Kind: Lossy, Sources: []Range{{Start: 4, End: 6}}, Targets: []Range{{Start: 3, End: 6}}},
		{Kind: Synthetic, Targets: []Range{{Start: 6, End: 9}}},
	}
	mapping, err := New(source, target, relations)
	if err != nil {
		t.Fatal(err)
	}
	relations[0].Sources[0].Start = 99
	got := mapping.Relations()
	got[0].Sources[0].Start = 88
	if mapping.Relations()[0].Sources[0].Start != 0 {
		t.Fatal("map retained caller mutation authority")
	}
	kinds := make(map[RelationKind]bool)
	for _, relation := range mapping.Relations() {
		kinds[relation.Kind] = true
	}
	for _, kind := range []RelationKind{Preserved, Synthetic, Deleted, Ambiguous, Lossy} {
		if !kinds[kind] {
			t.Errorf("map omits %q relation", kind)
		}
	}
}

func TestMapRepresentsOneToManyAndManyToOneLoss(t *testing.T) {
	t.Parallel()
	oneToMany, err := New(
		revision(t, "source", "ab"),
		revision(t, "target", "abab"),
		[]Relation{{Kind: Lossy, Sources: []Range{{0, 2}}, Targets: []Range{{0, 2}, {2, 4}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := oneToMany.Relations()[0]; len(got.Sources) != 1 || len(got.Targets) != 2 {
		t.Fatalf("one-to-many relation = %#v", got)
	}
	manyToOne, err := New(
		revision(t, "source", "abab"),
		revision(t, "target", "ab"),
		[]Relation{{Kind: Lossy, Sources: []Range{{0, 2}, {2, 4}}, Targets: []Range{{0, 2}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := manyToOne.Relations()[0]; len(got.Sources) != 2 || len(got.Targets) != 1 {
		t.Fatalf("many-to-one relation = %#v", got)
	}
}

func TestMapRejectsGapsOverlapAndInvalidKinds(t *testing.T) {
	t.Parallel()
	source := revision(t, "source", "abcd")
	target := revision(t, "target", "abcd")
	for _, test := range []struct {
		name      string
		relations []Relation
	}{
		{name: "source gap", relations: []Relation{{Kind: Lossy, Sources: []Range{{0, 3}}, Targets: []Range{{0, 4}}}}},
		{name: "target gap", relations: []Relation{{Kind: Lossy, Sources: []Range{{0, 4}}, Targets: []Range{{1, 4}}}}},
		{name: "overlap", relations: []Relation{
			{Kind: Lossy, Sources: []Range{{0, 3}}, Targets: []Range{{0, 2}}},
			{Kind: Lossy, Sources: []Range{{2, 4}}, Targets: []Range{{2, 4}}},
		}},
		{name: "invalid kind", relations: []Relation{{Kind: "guess", Sources: []Range{{0, 4}}, Targets: []Range{{0, 4}}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(source, target, test.relations); err == nil {
				t.Fatal("invalid transformation map was accepted")
			}
		})
	}
}

func TestComposeRequiresIdenticalIntermediateRevision(t *testing.T) {
	t.Parallel()
	first := mustMap(t, "a", "b", "ab", "ab")
	next := mustMap(t, "other-b", "c", "ab", "ab")
	if _, err := first.Compose(next); err == nil || !strings.Contains(err.Error(), "intermediate revision mismatch") {
		t.Fatalf("mismatched composition error = %v", err)
	}
	sameIdentityDifferentBytes := mustMap(t, "b", "c", "xy", "xy")
	if _, err := first.Compose(sameIdentityDifferentBytes); err == nil || !strings.Contains(err.Error(), "intermediate revision mismatch") {
		t.Fatalf("mismatched digest composition error = %v", err)
	}
}

func TestComposePreservesAlignedHistoryAndFailureKinds(t *testing.T) {
	t.Parallel()
	source := revision(t, "a", "abcd")
	middle := revision(t, "b", "abXY")
	target := revision(t, "c", "abZ")
	first, err := New(source, middle, []Relation{
		{Kind: Preserved, Sources: []Range{{0, 2}}, Targets: []Range{{0, 2}}},
		{Kind: Deleted, Sources: []Range{{2, 4}}},
		{Kind: Synthetic, Targets: []Range{{2, 4}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(middle, target, []Relation{
		{Kind: Preserved, Sources: []Range{{0, 2}}, Targets: []Range{{0, 2}}},
		{Kind: Deleted, Sources: []Range{{2, 4}}},
		{Kind: Synthetic, Targets: []Range{{2, 3}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	composed, err := first.Compose(second)
	if err != nil {
		t.Fatal(err)
	}
	want := []Relation{
		{Kind: Preserved, Sources: []Range{{0, 2}}, Targets: []Range{{0, 2}}},
		{Kind: Deleted, Sources: []Range{{2, 4}}},
		{Kind: Synthetic, Targets: []Range{{2, 3}}},
	}
	if got := composed.Relations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("composed relations = %#v, want %#v", got, want)
	}
}

func TestComposePreservesAlignedOneToManyAndManyToOneLoss(t *testing.T) {
	t.Parallel()
	source := revision(t, "a", "ab")
	middle := revision(t, "b", "abab")
	target := revision(t, "c", "AB")
	first, err := New(source, middle, []Relation{{
		Kind: Lossy, Sources: []Range{{0, 2}}, Targets: []Range{{0, 2}, {2, 4}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(middle, target, []Relation{{
		Kind: Lossy, Sources: []Range{{0, 2}, {2, 4}}, Targets: []Range{{0, 2}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	composed, err := first.Compose(second)
	if err != nil {
		t.Fatal(err)
	}
	want := []Relation{{Kind: Lossy, Sources: []Range{{0, 2}}, Targets: []Range{{0, 2}}}}
	if got := composed.Relations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("composed lossy relation = %#v, want %#v", got, want)
	}
}

func TestComposeRejectsUnalignedIntermediateBoundaries(t *testing.T) {
	t.Parallel()
	source := revision(t, "a", "abcd")
	middle := revision(t, "b", "abcd")
	target := revision(t, "c", "abcd")
	first, err := New(source, middle, []Relation{{
		Kind: Preserved, Sources: []Range{{0, 4}}, Targets: []Range{{0, 4}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(middle, target, []Relation{
		{Kind: Preserved, Sources: []Range{{0, 2}}, Targets: []Range{{0, 2}}},
		{Kind: Preserved, Sources: []Range{{2, 4}}, Targets: []Range{{2, 4}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Compose(second); err == nil || !strings.Contains(err.Error(), "do not align") {
		t.Fatalf("unaligned composition error = %v", err)
	}
}

func TestRevisionExposesByteCoordinatesAndContentIdentity(t *testing.T) {
	t.Parallel()
	left := revision(t, "original:p.go", "ab")
	right := revision(t, "original:p.go", "xy")
	if left.Unit() != CoordinateBytes || left.Size() != 2 || left.Identity() != "original:p.go" {
		t.Fatalf("revision coordinates = identity %q, unit %q, size %d", left.Identity(), left.Unit(), left.Size())
	}
	if left.Digest() == right.Digest() {
		t.Fatal("revision digest did not distinguish different immutable bytes")
	}
}

func revision(t *testing.T, identity, contents string) Revision {
	t.Helper()
	revision, err := NewRevision(identity, []byte(contents))
	if err != nil {
		t.Fatal(err)
	}
	return revision
}

func mustMap(t *testing.T, sourceID, targetID, sourceText, targetText string) Map {
	t.Helper()
	source := revision(t, sourceID, sourceText)
	target := revision(t, targetID, targetText)
	mapping, err := New(source, target, []Relation{{
		Kind: Preserved, Sources: []Range{{0, source.Size()}}, Targets: []Range{{0, target.Size()}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return mapping
}
