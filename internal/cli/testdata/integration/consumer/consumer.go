package consumer

import "example.test/gomcdc-fixture/shared"

func Read(value bool) bool {
	return shared.Enabled(value)
}
