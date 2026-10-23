// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dataDir is the result-file directory (EXB-003), from EXB_DATA.
func dataDir(t *testing.T) string {
	d := os.Getenv("EXB_DATA")
	if d == "" {
		t.Skip("set EXB_DATA to .helper/docs/rfcs/0002-data to check the measurement files")
	}
	return d
}

// requiredDataFiles lists the measurement result files (EXB-061 to EXB-068, EXB-081), and the
// logs kept beside them: each exbmeasure command's combined output under logs/, and per slowdown
// measurement parta's reference.json (with the backend's failure logs) under slowdown-logs/.
func requiredDataFiles(toolchains []string) []string {
	var files []string
	for _, b := range []string{"parta", "wasm", "toolexec"} {
		for _, c := range []string{"C1", "C2", "C3", "C4", "C5"} {
			files = append(files, "exactness-"+b+"-"+c+".csv", filepath.Join("logs", "exactness-"+b+"-"+c+".log"))
		}
		files = append(files, "sensitivity-"+b+"-C4.csv", "sensitivity-"+b+"-C5.csv", "compat-"+b+".csv",
			"goroutine-order-"+b+".csv")
		for _, c := range []string{"C4", "C5", "C7"} {
			files = append(files, filepath.Join("logs", "sensitivity-"+b+"-"+c+".log"))
		}
		for _, c := range []string{"C1", "C2", "C3"} {
			files = append(files, filepath.Join("logs", "slowdown-"+b+"-"+c+".log"),
				filepath.Join("slowdown-logs", b+"-"+c, "reference.json"))
		}
		files = append(files, filepath.Join("logs", "compat-"+b+".log"))
	}
	files = append(files, "slowdown.csv", "size-wasm.txt", "size-toolexec.txt", "deps.txt", "machine.txt",
		"quantum-wasm-C1.csv", "quantum-wasm-C2.csv", "quantum-wasm-C3.csv",
		"exactness-toolexec-std-C3.csv", "gaps-toolexec.txt")
	for _, l := range []string{"size-wasm", "size-toolexec", "deps", "quantum-wasm-C1", "quantum-wasm-C2",
		"quantum-wasm-C3", "exactness-toolexec-std-C3"} {
		files = append(files, filepath.Join("logs", l+".log"))
	}
	for _, tc := range toolchains {
		for _, b := range []string{"wasm", "toolexec"} {
			for _, f := range []string{"compat-" + b, "exactness-" + b + "-C2", "exactness-" + b + "-C3",
				"sensitivity-" + b + "-C4", "sensitivity-" + b + "-C5"} {
				files = append(files, filepath.Join("robustness", tc, f+".csv"),
					filepath.Join("robustness", tc, "logs", f+".log"))
			}
		}
	}
	return files
}

// toolchains returns the robustness/<toolchain> directories (EXB-063): one go1.26 and one go1.27.
func toolchains(t *testing.T, dir string) []string {
	ents, err := os.ReadDir(filepath.Join(dir, "robustness"))
	if err != nil {
		t.Fatalf("robustness directory: %v", err)
	}
	var tcs []string
	for _, e := range ents {
		if e.IsDir() {
			tcs = append(tcs, e.Name())
		}
	}
	if len(tcs) != 2 || !strings.HasPrefix(tcs[0], "go1.26.") || !strings.HasPrefix(tcs[1], "go1.27.") {
		t.Fatalf("robustness toolchains %v, want one go1.26.x and one go1.27.y", tcs)
	}
	return tcs
}

func TestDataFilesComplete(t *testing.T) {
	dir := dataDir(t)
	for _, f := range requiredDataFiles(toolchains(t, dir)) {
		info, err := os.Stat(filepath.Join(dir, f))
		if err != nil || info.Size() == 0 {
			t.Errorf("%s: missing or empty (%v)", f, err)
		}
	}
}
