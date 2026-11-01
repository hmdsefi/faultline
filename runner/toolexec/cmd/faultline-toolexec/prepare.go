// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// stateDir returns the directory of the prepared state: FAULTLINE_TOOLEXEC_STATE, which must be
// absolute, or faultline-toolexec in the user's cache directory.
func stateDir() (string, error) {
	if d := os.Getenv("FAULTLINE_TOOLEXEC_STATE"); d != "" {
		if !filepath.IsAbs(d) {
			return "", fmt.Errorf("FAULTLINE_TOOLEXEC_STATE=%q: want an absolute path", d)
		}
		return d, nil
	}
	c, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("no state directory: %v; set FAULTLINE_TOOLEXEC_STATE", err)
	}
	return filepath.Join(c, "faultline-toolexec"), nil
}

// stateBase returns the path of the state files of goVersion and the target, without extension.
func stateBase(goVersion string) string {
	dir, err := stateDir()
	if err != nil {
		fail("%v", err)
	}
	goos, goarch := target()
	return filepath.Join(dir, goVersion+"-"+goos+"-"+goarch)
}

func target() (goos, goarch string) {
	goos, goarch = os.Getenv("GOOS"), os.Getenv("GOARCH")
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

// prepare writes the prepared state of the current toolchain: sched's export data without and
// with -race, and GOROOT and GOMODCACHE.
func prepare() error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	out, err := exec.Command("go", "env", "GOVERSION", "GOOS", "GOARCH", "GOROOT", "GOMODCACHE").Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			err = fmt.Errorf("%v: %s", err, bytes.TrimSpace(ee.Stderr))
		}
		return fmt.Errorf("prepare: go env: %v", err)
	}
	env := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(env) != 5 {
		return fmt.Errorf("prepare: go env: unexpected output %q", out)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // the state directory serves every build of the user
		return err
	}
	base := filepath.Join(dir, env[0]+"-"+env[1]+"-"+env[2])
	plain, _, err := listExport(false)
	if err != nil {
		return err
	}
	if err := writeAtomic(base+".importcfg", plain); err != nil {
		return err
	}
	if race, stderr, err := listExport(true); err == nil {
		if err := writeAtomic(base+"-race.importcfg", race); err != nil {
			return err
		}
	} else {
		if err := os.Remove(base + "-race.importcfg"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		first, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n")
		fmt.Fprintf(os.Stderr, "faultline-toolexec: prepare: -race state not written: %s\n", first)
	}
	return writeAtomic(base+".env", []byte("GOROOT="+env[3]+"\nGOMODCACHE="+env[4]+"\n"))
}

// listExport runs go list -export for sched and its dependencies, with -race when race is set,
// under the caller's GOFLAGS without -race and -toolexec. A failing plain run is an error; a
// failing -race run returns its stderr.
func listExport(race bool) (out []byte, stderr string, err error) {
	args := []string{"list", "-deps", "-export", "-f", "{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}"}
	if race {
		args = append(args, "-race")
	}
	cmd := exec.Command("go", append(args, schedPath)...) //nolint:gosec // go list with the wrapper's own arguments
	cmd.Env = append(os.Environ(), "GOFLAGS="+prepareFlags(os.Getenv("GOFLAGS")))
	var eb bytes.Buffer
	cmd.Stderr = &eb
	out, err = cmd.Output()
	if err != nil && !race {
		return nil, "", fmt.Errorf("prepare: go list %s: %v: %s", schedPath, err, strings.TrimSpace(eb.String()))
	}
	return out, eb.String(), err
}

// prepareFlags drops -race and -toolexec from GOFLAGS: prepare writes both variants itself, and
// sched compiles without the wrapper.
func prepareFlags(goflags string) string {
	var keep []string
	for _, f := range strings.Fields(goflags) {
		name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "=")
		if name != "race" && name != "toolexec" {
			keep = append(keep, f)
		}
	}
	return strings.Join(keep, " ")
}

// prepared returns the prepared importcfg lines of the variant (-race when race is set) and
// GOROOT/GOMODCACHE for goVersion. A missing or malformed file fails the call.
func prepared(goVersion string, race bool) (lines []string, roots map[string]string) {
	base := stateBase(goVersion)
	goos, goarch := target()
	again := fmt.Sprintf(`run "faultline-toolexec prepare" for %s %s/%s`, goVersion, goos, goarch)
	cfg := base + ".importcfg"
	if race {
		cfg = base + "-race.importcfg"
	}
	b, err := os.ReadFile(cfg) //nolint:gosec // prepare's file in the state directory
	if err != nil {
		if race {
			fail("%s with -race support", again)
		}
		fail("%s", again)
	}
	e, err := os.ReadFile(base + ".env") //nolint:gosec // prepare's file in the state directory
	if err != nil {
		fail("%s", again)
	}
	if lines, err = preparedLines(string(b)); err != nil {
		fail("%s: %v; %s", cfg, err, again)
	}
	if roots, err = preparedRoots(string(e)); err != nil {
		fail("%s: %v; %s", base+".env", err, again)
	}
	return lines, roots
}

// versionLine prints tool -V=full's line followed by the wrapper's cache key: the version, the
// hash of the prepared state, and the two lists (XTE-021).
func versionLine(tool string) {
	out, err := exec.Command(tool, "-V=full").Output() //nolint:gosec // runs the tool the go command names, as -toolexec does
	if err != nil {
		fail("%s -V=full: %v", tool, err)
	}
	line := strings.TrimSpace(string(out))
	f := strings.Fields(line)
	if len(f) < 3 {
		fail("%s -V=full: unexpected output %q", tool, out)
	}
	lines, _ := prepared(f[2], false)
	base := stateBase(f[2])
	plain, _ := os.ReadFile(base + ".importcfg")     //nolint:gosec // prepare's file, which prepared just read
	race, _ := os.ReadFile(base + "-race.importcfg") //nolint:gosec // prepare's file; absent without -race support
	std, deps := os.Getenv("FAULTLINE_TOOLEXEC_STD"), os.Getenv("FAULTLINE_TOOLEXEC_DEPS")
	if err := checkStd(std, lines); err != nil {
		fail("%v", err)
	}
	if err := checkDeps(deps); err != nil {
		fail("%v", err)
	}
	sum := sha256.Sum256(append(plain, race...))
	id := "faultline-toolexec=" + version + ",sched=" + hex.EncodeToString(sum[:])[:12]
	if std != "" {
		id += ",std=" + std
	}
	if deps != "" {
		id += ",deps=" + deps
	}
	fmt.Printf("%s %s\n", line, id)
}

// writeAtomic writes b to a temporary file next to name and renames it to name, so that a
// compile reading name sees the old file or the new one, never a part of it.
func writeAtomic(name string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), name)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// preparedLines checks that s is an importcfg as prepare writes it: whole packagefile lines with
// an absolute file that exists, sched's among them. go list prints sched last, so a file cut short
// at a line end lacks it.
func preparedLines(s string) ([]string, error) {
	if !strings.HasSuffix(s, "\n") {
		return nil, errors.New("the file does not end with a newline")
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	sched := false
	for _, l := range lines {
		rest, ok := strings.CutPrefix(l, "packagefile ")
		p, f, ok2 := strings.Cut(rest, "=")
		if !ok || !ok2 || p == "" || !filepath.IsAbs(f) {
			return nil, fmt.Errorf("line %q is not a packagefile line", l)
		}
		// The export files live in the go build cache, which deletes an entry after five days
		// without a lookup; builds through the wrapper never look them up.
		if _, err := os.Stat(f); err != nil {
			return nil, fmt.Errorf("line %q: the export file is missing (the go build cache was cleaned or trimmed)", l)
		}
		sched = sched || p == schedPath
	}
	if !sched {
		return nil, fmt.Errorf("no export data for %s", schedPath)
	}
	return lines, nil
}

// preparedRoots reads the GOROOT and GOMODCACHE lines that prepare writes, both absolute paths.
func preparedRoots(s string) (map[string]string, error) {
	lines := strings.Split(s, "\n")
	if len(lines) != 3 || lines[2] != "" {
		return nil, errors.New("want a GOROOT line and a GOMODCACHE line")
	}
	roots := map[string]string{}
	for i, k := range []string{"GOROOT", "GOMODCACHE"} {
		v, ok := strings.CutPrefix(lines[i], k+"=")
		if !ok || !filepath.IsAbs(v) {
			return nil, fmt.Errorf("line %q: want %s=<absolute path>", lines[i], k)
		}
		roots[k] = v
	}
	return roots, nil
}

// toolGoVersion returns the "go1.x.y" field of tool -V=full.
func toolGoVersion(tool string) string {
	out, err := exec.Command(tool, "-V=full").Output() //nolint:gosec // runs the tool the go command names, as -toolexec does
	if err != nil {
		fail("%s -V=full: %v", tool, err)
	}
	f := strings.Fields(string(out))
	if len(f) < 3 {
		fail("%s -V=full: unexpected output %q", tool, out)
	}
	return f[2]
}
