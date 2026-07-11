package good

import "testing"

func TestPositive(t *testing.T) {
	if !Positive(1) || Positive(0) {
		t.Fatal("unexpected result")
	}
}
