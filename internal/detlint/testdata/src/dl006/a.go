package dl006

type Named map[string]int

type MapConstraint interface{ map[string]int }

func F(m map[string]int, n Named, s []int) {
	for k := range m { // want DL006
		_ = k
	}
	for k := range n { // want DL006
		_ = k
	}
	for i := range s {
		_ = i
	}
}

func G[M ~map[string]int](m M) {
	for k := range m { // want DL006
		_ = k
	}
}

func H[M MapConstraint](m M) {
	for k := range m { // want DL006
		_ = k
	}
}
