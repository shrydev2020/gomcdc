package routing_test

import (
	"testing"

	"example.test/gomcdc-fixture/routing"
)

func externalHelper(value bool) bool {
	if value {
		return true
	}
	return false
}

func TestExternalPackage(t *testing.T) {
	if !externalHelper(routing.ExpressionSwitch(1) == 2) {
		t.Fatal("unexpected external package result")
	}
}
