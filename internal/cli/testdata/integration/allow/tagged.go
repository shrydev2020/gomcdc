//go:build !plan9

package allow

// TaggedMultiline exercises active build-tag selection, multiline decisions,
// multiple statement blocks, and switch clauses in the combined cover plan.
func TaggedMultiline(left, right bool) int {
	if left &&
		right {
		value := 1
		value++
		return value
	}
	switch left {
	case true:
		return 1
	default:
		return 0
	}
}

//line virtual_allow.go:200
func LineMapped(value bool) int {
	if value {
		return 1
	}
	return 0
}
