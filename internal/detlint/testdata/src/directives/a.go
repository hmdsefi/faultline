package directives

import "time"

func F(m map[string]int) {
	//faultline:maporder keys sorted below
	for k := range m {
		_ = k
	}
	for k := range m { //faultline:maporder ok
		_ = k
	}
	/* want DL000 */   //faultline:maporder
	for k := range m { // want DL006
		_ = k
	}
	// faultline:maporder spaced
	for k := range m { // want DL006
		_ = k
	}
	/* want DL000 */ //faultline:sorted x
	_ = len(m)
	/* want DL000 */ //faultline:maporder nothing here
	_ = len(m)
}

func W() {
	/* want(core) DL000 */ //faultline:wallclock budget
	_ = time.Now()         // want(core) DL001
}
