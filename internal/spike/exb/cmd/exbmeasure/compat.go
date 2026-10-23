// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
)

// compat implements EXB-065: every C1 and C6 test once in the backend's normal mode (each test's
// own seeds; wasm runs one instance per seed), written to compat-<backend>.csv as
// test,result,seconds. The requirement is that every gated test passes; the gRPC test is
// reported, not gated.
func compat(c *config) error {
	if !slices.Contains(backends, c.backend) {
		return fmt.Errorf("unknown backend %q", c.backend)
	}
	dir := filepath.Join(c.work, c.backend, "compat")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	recs := [][]string{{"test", "result", "seconds"}}
	failed, gated := 0, 0
	for _, corpus := range []string{"C1", "C6"} {
		for _, tc := range corpora[corpus] {
			res, log := filepath.Join(dir, tc.Name+".jsonl"), filepath.Join(dir, tc.Name+".log")
			os.Remove(res)
			os.Remove(log)
			rr, err := c.exec(tc, []string{"FAULTLINE_RESULTS=" + res, "FAULTLINE_ARTIFACTS=off"})
			if err != nil {
				return err
			}
			lines, err := readResults(res)
			if err != nil { // judged without results lines, as the parent test would fail to read them
				fmt.Fprintf(os.Stderr, "exbmeasure: compat %s %s: %v\n", c.backend, tc.Name, err)
				lines = nil
			}
			result, why := compatResult(tc, rr, lines)
			if tc.Gated {
				gated++
			}
			if why != nil {
				if tc.Gated {
					failed++
				}
				if err := os.WriteFile(log, []byte(rr.head()+"\n"+rr.out), 0o600); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "exbmeasure: compat %s %s: %s: %v; output in %s\n", c.backend, tc.Name, result, why, log)
			}
			recs = append(recs, []string{tc.Name, result, strconv.FormatFloat(rr.wall.Seconds(), 'f', 3, 64)})
		}
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	if err := w.WriteAll(recs); err != nil {
		return err
	}
	if err := os.WriteFile(c.out, b.Bytes(), 0o600); err != nil {
		return err
	}
	fmt.Printf("compat %s: %d of %d gated tests failed\n", c.backend, failed, gated)
	if failed > 0 {
		return errRequirement
	}
	return nil
}

// compatResult judges one compat run and returns its result column and, for a failure, the
// reason. A scenario entry point passes when its parent test's assertion (Check) accepts the run;
// any other test when it exits 0. A gated test that exits non-zero although every results line
// passed is "fail (all seeds passed)": the failing assertion ran outside the seeds, and under
// wasm that is a test that checks several seeds after Run, which counts as failed.
func compatResult(tc testCase, rr runResult, lines []map[string]any) (string, error) {
	var err error
	switch {
	case tc.Check != nil:
		err = tc.Check(rr.code, rr.out, lines)
	case rr.code != 0:
		err = errors.New(rr.head())
	}
	switch {
	case err == nil:
		return "pass", nil
	case !tc.Gated:
		return "fail (not gated)", err
	case tc.Check == nil && rr.code > 0 && allPassed(lines):
		return "fail (all seeds passed)", fmt.Errorf("exit %d, though all %d results lines passed", rr.code, len(lines))
	default:
		return "fail", err
	}
}

// allPassed reports whether lines is not empty and every line has status pass.
func allPassed(lines []map[string]any) bool {
	for _, l := range lines {
		if str(l, "status") != "pass" {
			return false
		}
	}
	return len(lines) > 0
}
