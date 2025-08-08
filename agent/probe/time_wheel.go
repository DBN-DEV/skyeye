package probe

type timer struct {
	cancelled bool
	callback  func()
}

type timerWheel struct {
}
