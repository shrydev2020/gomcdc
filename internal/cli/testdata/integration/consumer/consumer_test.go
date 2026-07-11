package consumer

import "testing"

func TestRead(t *testing.T) {
	if !Read(true) || Read(false) {
		t.Fatal("unexpected shared result")
	}
}
