package broken

var Invalid string = 42

func Broken(value int) bool {
	if value > 0 {
		return Invalid != ""
	}
	return false
}
