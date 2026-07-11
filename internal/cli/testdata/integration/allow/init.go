package allow

var firstInit = true
var secondInit = true

func init() {
	if firstInit {
		firstInit = false
	}
}

func init() {
	if secondInit {
		secondInit = false
	}
}
