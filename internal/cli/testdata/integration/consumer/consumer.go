package consumer

import "example.test/gocoverage-fixture/shared"

func Read(value bool) bool {
	return shared.Enabled(value)
}
