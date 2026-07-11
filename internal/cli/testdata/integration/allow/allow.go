package allow

func Allow(a, b bool) bool {
	if a && b {
		return true
	}
	return false
}

func Any(a, b bool) bool {
	if a || b {
		return true
	}
	return false
}

func Nested(a, b, c bool) bool {
	if a && (b || c) {
		return true
	}
	return false
}

func Recursive(n int) bool {
	if n <= 0 {
		return true
	}
	if Recursive(n-1) && n > 0 {
		return true
	}
	return false
}

func MayPanic(a bool, predicate func() bool) bool {
	if a && predicate() {
		return true
	}
	return false
}
