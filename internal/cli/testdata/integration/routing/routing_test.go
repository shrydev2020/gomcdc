package routing

import "testing"

func TestClauses(t *testing.T) {
	// case 2 is reached only by fallthrough, so its body and direct-selection
	// obligations must remain observably distinct.
	for _, value := range []int{1, 9} {
		ExpressionSwitch(value)
	}
	for _, value := range []int{1, 2, 9} {
		NoDefault(value)
	}
	for _, value := range []any{1, "x", []byte("x"), true} {
		TypeSwitch(value)
	}
	for _, values := range [][3]bool{
		{true, false, false},
		{false, true, true},
		{false, false, false},
	} {
		Conditional(values[0], values[1], values[2])
	}
	for _, test := range []struct {
		a, b, c bool
		want    int
	}{
		{a: true, b: true, c: true, want: 1},
		{a: true, b: false, c: true, want: 1},
		{a: false, b: false, c: false, want: 2},
	} {
		if got := MultiConditional(test.a, test.b, test.c); got != test.want {
			t.Fatalf("MultiConditional(%t,%t,%t)=%d, want %d", test.a, test.b, test.c, got, test.want)
		}
	}
	ready := make(chan int, 1)
	ready <- 7
	SelectValue(ready)
	SelectValue(make(chan int))
}
