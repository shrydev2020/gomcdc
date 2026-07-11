package routing

func ExpressionSwitch(value int) int {
	switch value {
	case 1:
		fallthrough
	case 2:
		return 2
	default:
		return 0
	}
}

func NoDefault(value int) int {
	switch value {
	case 1, 2:
		return value
	}
	return 0
}

func TypeSwitch(value any) string {
	switch value.(type) {
	case int:
		return "int"
	case string, []byte:
		return "text"
	default:
		return "other"
	}
}

func Conditional(a, b, c bool) int {
	switch {
	case a:
		return 1
	case b && c:
		return 2
	default:
		return 0
	}
}

func MultiConditional(a, b, c bool) int {
	switch {
	case a && b, c:
		return 1
	case !a:
		return 2
	default:
		return 0
	}
}

func SelectValue(channel <-chan int) int {
	select {
	case value := <-channel:
		return value
	default:
		return 0
	}
}
