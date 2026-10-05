package dl004

func f() {}

func F() {
	go f() // want DL004
}
