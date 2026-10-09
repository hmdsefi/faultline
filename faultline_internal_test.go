package faultline

import "testing"

// API-010: Run reads each variable once, although resultsPath and resolve both look up
// FAULTLINE_RESULTS.
func TestOnceLookup(t *testing.T) {
	// FAULTLINE_RESULTS set, and unset (the usual case): an unset variable is read once too.
	for _, vars := range []map[string]string{{"FAULTLINE_RESULTS": "r.jsonl"}, nil} {
		calls := map[string]int{}
		env := fakeEnv(vars)
		lookup := onceLookup(func(name string) (string, bool) {
			calls[name]++
			return env(name)
		})
		results := resultsPath(lookup)
		p, err := resolve(resolveInput{testName: "TestX", lookup: lookup, exploreBase: func() uint64 { return 0 }})
		if err != nil {
			t.Fatal(err)
		}
		if (results == "") != (vars == nil) || p.env.results != results {
			t.Errorf("%v: FAULTLINE_RESULTS %q, then %q", vars, results, p.env.results)
		}
		for _, name := range envOrder {
			if calls[name] != 1 {
				t.Errorf("%v: %s looked up %d times, want 1", vars, name, calls[name])
			}
		}
	}
}
