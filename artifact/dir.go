// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package artifact

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/hmdsefi/faultline/kernel/fault"
)

// Artifact is the content of one artifact directory.
type Artifact struct {
	Report   Report
	Text     string            // report.txt content, written verbatim
	Trace    *Trace            // required by Write; nil from Read when trace.jsonl is absent
	Schedule *fault.Schedule   // nil: no schedule.json
	History  []byte            // history.jsonl content, as history.Recorder.WriteJSONL writes it; empty: no file
	Extra    map[string][]byte // extra files by name, written verbatim; nil from Read
}

// Dir returns filepath.Join(root, SanitizePackage(pkg), SanitizeTest(test), fmt.Sprintf("%016x", seed)).
func Dir(root, pkg, test string, seed uint64) string {
	return filepath.Join(root, SanitizePackage(pkg), SanitizeTest(test), fmt.Sprintf("%016x", seed))
}

// AltDir returns the folder for an artifact whose Dir holds another test's artifact, which
// happens when two names sanitize to the same path. It is Dir(root, pkg, test, seed) + "-" + the
// FNV-1a 64 hash of pkg + "\x00" + test as 16 hex digits.
func AltDir(root, pkg, test string, seed uint64) string {
	return Dir(root, pkg, test, seed) + fmt.Sprintf("-%016x", fnv1a64(pkg+"\x00"+test))
}

// SanitizePackage maps an import path to one path element ('/' becomes '_').
func SanitizePackage(pkg string) string { return sanitize(pkg, "_") }

// SanitizeTest maps a test name to one path element ('/' becomes "__").
func SanitizeTest(name string) string { return sanitize(name, "__") }

// sanitize implements ART-002.
func sanitize(s, rep string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', strings.IndexByte(".-_+=@,~#", c) >= 0:
			b.WriteByte(c)
		case c == '/':
			b.WriteString(rep)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		out = "_" + out
	}
	if len(out) > 100 {
		out = out[:83] + "-" + fmt.Sprintf("%016x", fnv1a64(s))
	}
	return out
}

// fnv1a64 is FNV-1a 64 over the bytes of s.
func fnv1a64(s string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 0x100000001b3
	}
	return h
}

// extraName is the pattern of extra file names (ART-010).
var extraName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// standardFiles are the names extra files may not use (ART-010).
var standardFiles = []string{FileReport, FileReportText, FileTrace, FileSchedule, FileHistory, FileTimelineText, FileHB, FileTimelineHTML, FileMinimized}

// validate implements ART-010.
func validate(a *Artifact) error {
	if a.Trace == nil {
		return errors.New("artifact: trace is nil")
	}
	for i, r := range a.Trace.Records {
		if r.Seq == 0 {
			return errors.New("artifact: record with Seq 0")
		}
		if i > 0 && r.Seq <= a.Trace.Records[i-1].Seq {
			return fmt.Errorf("artifact: records not in ascending Seq order at index %d", i)
		}
	}
	switch a.Report.Status {
	case "fail":
		if a.Report.Failure == nil {
			return errors.New("artifact: status fail without failure")
		}
	case "pass":
		if a.Report.Failure != nil {
			return errors.New("artifact: status pass with failure")
		}
	default:
		return fmt.Errorf("artifact: invalid status %q", a.Report.Status)
	}
	for _, name := range slices.Sorted(maps.Keys(a.Extra)) {
		if !extraName.MatchString(name) {
			return fmt.Errorf("artifact: invalid extra file name %q", name)
		}
		if slices.Contains(standardFiles, name) {
			return fmt.Errorf("artifact: extra file %q collides with a standard file", name)
		}
	}
	return nil
}

// fileIndex returns report.files (ART-014).
func fileIndex(a *Artifact, extras []string) []File {
	files := []File{{Name: FileReportText, Type: "report_text"}, {Name: FileTrace, Type: "trace", Version: TraceVersion}}
	if a.Schedule != nil {
		v := a.Schedule.Version
		if v == 0 {
			v = 1
		}
		files = append(files, File{Name: FileSchedule, Type: "schedule", Version: v})
	}
	if len(a.History) > 0 {
		files = append(files, File{Name: FileHistory, Type: "history"})
	}
	for _, name := range extras {
		files = append(files, File{Name: name, Type: "extra"})
	}
	return append(files,
		File{Name: FileTimelineText, Type: "timeline_text", Version: TimelineVersion},
		File{Name: FileHB, Type: "hb", Version: HBVersion},
		File{Name: FileTimelineHTML, Type: "timeline_html", Version: TimelineVersion},
	)
}

// ErrOtherTest is wrapped by Write's error when dir holds the artifact of another package, test or
// seed. The caller then writes to AltDir.
var ErrOtherTest = errors.New("it holds the artifact of another test")

// Write writes a to dir atomically. It writes every file into a temporary directory next to dir,
// then renames it to dir, so dir never mixes old and new files. It refuses to replace a symbolic
// link or a directory without report.json. It does not replace the artifact of another package,
// test or seed either: the error then wraps ErrOtherTest. Once a and dir pass its checks,
// and before it writes a file, Write changes a: a.Report becomes the copy it writes, with every
// string valid UTF-8, nil Nodes, Planners and Files empty, and Version, Dir and Files set, and
// a.Trace.Header the header line it writes, with Version, Records and Dropped set. An error after
// that point, such as a file system error, leaves these changes in a.
func Write(dir string, a *Artifact) error {
	if a == nil {
		return errors.New("artifact: artifact is nil")
	}
	if err := validate(a); err != nil {
		return err
	}
	// The last ART-010 check: a pointer cycle fails here, before a or the file system changes.
	rep, err := normReport(&a.Report)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	var sched []byte
	if a.Schedule != nil {
		var b bytes.Buffer
		if err := a.Schedule.Write(&b); err != nil {
			return fmt.Errorf("artifact: write %s: %w", FileSchedule, err)
		}
		sched = b.Bytes()
	}
	// dir itself is checked before anything is written (ART-012). It is never followed: a
	// symbolic link is refused, whatever it points to. So is the artifact of another package,
	// test or seed, since names can map to one folder (ART-001).
	seen, err := checkTarget(abs, &rep)
	if err != nil {
		return err
	}
	extras := slices.Sorted(maps.Keys(a.Extra))
	a.Trace.Header = headerFor(a.Trace)
	a.Report = rep
	a.Report.Dir = abs
	a.Report.Files = fileIndex(a, extras)
	root := uint64(0)
	if a.Report.Failure != nil {
		root = a.Report.Failure.RecordSeq
	}
	s := CausalSlice(a.Trace.Records, root, DefaultSliceCap)

	parent, base := filepath.Dir(abs), filepath.Base(abs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	// A killed Write of this dir leaves its temporary or set-aside copy (two writers of one dir
	// are not supported, so none is in use).
	if entries, err := os.ReadDir(parent); err == nil {
		for _, e := range entries {
			if leftover(e.Name(), base) {
				_ = os.RemoveAll(filepath.Join(parent, e.Name()))
			}
		}
	}
	tmp, err := os.MkdirTemp(parent, "."+base+".tmp-")
	if err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(tmp)
		}
	}()
	if err := os.Chmod(tmp, 0o755); err != nil {
		return fmt.Errorf("artifact: %w", err)
	}
	write := func(name string, fn func(f *os.File) error) (err error) {
		f, err := os.OpenFile(filepath.Join(tmp, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("artifact: write %s: %w", name, err)
		}
		defer func() {
			if cerr := f.Close(); err == nil && cerr != nil {
				err = fmt.Errorf("artifact: write %s: %w", name, cerr)
			}
		}()
		// The umask narrows OpenFile's mode; ART-012 wants exactly 0o644, as Render writes.
		if err := f.Chmod(0o644); err != nil {
			return fmt.Errorf("artifact: write %s: %w", name, err)
		}
		if err := fn(f); err != nil {
			return fmt.Errorf("artifact: write %s: %w", name, err)
		}
		return nil
	}
	bytesOf := func(b []byte) func(f *os.File) error {
		return func(f *os.File) error { _, err := f.Write(b); return err }
	}
	if err := write(FileTrace, func(f *os.File) error { return WriteTrace(f, a.Trace) }); err != nil {
		return err
	}
	if a.Schedule != nil {
		if err := write(FileSchedule, bytesOf(sched)); err != nil {
			return err
		}
	}
	if len(a.History) > 0 {
		if err := write(FileHistory, bytesOf(a.History)); err != nil {
			return err
		}
	}
	for _, name := range extras {
		if err := write(name, bytesOf(a.Extra[name])); err != nil {
			return err
		}
	}
	if err := write(FileTimelineText, func(f *os.File) error { return WriteTimelineText(f, &a.Report, a.Trace, s) }); err != nil {
		return err
	}
	if err := write(FileHB, func(f *os.File) error { return WriteHB(f, a.Trace, root, DefaultSliceCap) }); err != nil {
		return err
	}
	if err := write(FileTimelineHTML, func(f *os.File) error {
		return WriteTimelineHTML(f, &a.Report, a.Trace, sched, s, DefaultTimelineRecords)
	}); err != nil {
		return err
	}
	if err := write(FileReportText, bytesOf([]byte(a.Text))); err != nil {
		return err
	}
	if err := write(FileReport, func(f *os.File) error { return WriteReport(f, &a.Report) }); err != nil {
		return err
	}
	// The old directory is set aside, not deleted, until the new one is in place: a kill or an
	// error never leaves dir half deleted.
	old := ""
	// Checked again: the files took a while, and dir may have changed meanwhile.
	seen, err = recheckTarget(abs, &rep, seen)
	if err != nil {
		return err
	}
	if seen.dir != nil {
		old = tmp + ".old"
		if err := os.Rename(abs, old); err != nil {
			return fmt.Errorf("artifact: %w", err)
		}
	}
	if err := os.Rename(tmp, abs); err != nil {
		if old != "" {
			_ = os.Rename(old, abs)
		}
		return fmt.Errorf("artifact: %w", err)
	}
	ok = true
	if old != "" {
		_ = os.RemoveAll(old)
	}
	return nil
}

// leftover reports whether name is a temporary or set-aside sibling of base that a killed Write
// left: "."+base+".tmp-" then MkdirTemp's digits, and ".old" for a set-aside copy.
func leftover(name, base string) bool {
	rest, ok := strings.CutPrefix(name, "."+base+".tmp-")
	if !ok {
		return false
	}
	rest = strings.TrimSuffix(rest, ".old")
	return rest != "" && strings.Trim(rest, "0123456789") == ""
}

// target is what checkTarget found at dir: the os.Lstat results of dir and of dir/report.json,
// nil where nothing was found.
type target struct {
	dir, report fs.FileInfo
}

// checkTarget returns what it found at dir, and an error unless Write may replace dir with the
// artifact of rep (ART-012): dir is not followed, so a symbolic link is refused, and so is the
// artifact of another package, test or seed.
func checkTarget(abs string, rep *Report) (target, error) {
	fi, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return target{}, nil
	case err != nil:
		return target{}, fmt.Errorf("artifact: %w", err)
	case fi.Mode()&fs.ModeSymlink != 0:
		return target{}, fmt.Errorf("artifact: refusing to replace %s: it is a symbolic link", abs)
	case !replaceable(abs):
		return target{}, fmt.Errorf("artifact: refusing to replace %s: not a faultline artifact directory (no report.json)", abs)
	}
	t := target{dir: fi, report: lstatOrNil(filepath.Join(abs, FileReport))}
	if old := oldReport(abs, t.report); old != nil && (old.Package != rep.Package || old.Test != rep.Test || old.Seed != rep.Seed) {
		return target{}, fmt.Errorf("artifact: refusing to replace %s: %w (package %q, test %q, seed %q)", abs, ErrOtherTest, old.Package, old.Test, old.Seed)
	}
	return t, nil
}

// recheckTarget checks dir again just before Write sets it aside (ART-012). Reading report.json is
// the costly part, so seen stands only when dir is the same directory and report.json the same
// regular file as in seen; otherwise, also for a dir without report.json, checkTarget runs again.
// A dir that did not exist goes straight to checkTarget, whose one os.Lstat is all a new dir costs.
func recheckTarget(abs string, rep *Report, seen target) (target, error) {
	if seen.dir != nil && sameFile(lstatOrNil(abs), seen.dir) && sameReport(lstatOrNil(filepath.Join(abs, FileReport)), seen.report) {
		return seen, nil
	}
	return checkTarget(abs, rep)
}

// lstatOrNil returns os.Lstat(name), or nil on an error.
func lstatOrNil(name string) fs.FileInfo {
	fi, err := os.Lstat(name)
	if err != nil {
		return nil
	}
	return fi
}

// sameFile reports whether a and b are the same file; nil is no file.
func sameFile(a, b fs.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b)
}

// sameReport is sameFile for report.json, which must also be a regular file that kept its size and
// modification time.
func sameReport(a, b fs.FileInfo) bool {
	return sameFile(a, b) && a.Mode().IsRegular() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// oldReport returns the report of the artifact in dir, or nil when report.json (fi, its os.Lstat
// result) is not a regular file or ReadReport rejects it (ART-012). A link is not followed, and a
// FIFO is not opened.
func oldReport(dir string, fi fs.FileInfo) *Report {
	if fi == nil || !fi.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(filepath.Join(dir, FileReport))
	if err != nil {
		return nil
	}
	defer f.Close()
	rep, err := ReadReport(f)
	if err != nil {
		return nil
	}
	return rep
}

// replaceable reports whether an existing dir may be replaced (ART-012): it holds report.json, or
// its only entry is stall.txt (GOR-081).
func replaceable(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, FileReport)); err == nil {
		return true
	}
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 1 && entries[0].Name() == "stall.txt"
}

// Read reads an artifact directory: report.json, which is required, and report.txt, trace.jsonl,
// schedule.json and history.jsonl when present. It does not read extra files; report.json's file
// index lists them. An error names its file once and wraps its cause.
func Read(dir string) (*Artifact, error) {
	// fail returns "artifact: <dir>: <reason>" wrapping err. For a *fs.PathError of file name the
	// reason is "<op> <name>: <inner error>", so the path is not repeated; otherwise it is err's text
	// without its "artifact: " prefix.
	fail := func(name string, err error) error {
		if pe, ok := err.(*fs.PathError); ok {
			return fmt.Errorf("artifact: %s: %s %s: %w", dir, pe.Op, name, pe.Err)
		}
		return &readError{dir: dir, err: err}
	}
	// file returns the content of name, or nil when it does not exist and is not report.json.
	file := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil && (name == FileReport || !errors.Is(err, fs.ErrNotExist)) {
			return nil, fail(name, err)
		}
		return b, nil
	}
	b, err := file(FileReport)
	if err != nil {
		return nil, err
	}
	rep, err := ReadReport(bytes.NewReader(b))
	if err != nil {
		return nil, fail(FileReport, err)
	}
	a := &Artifact{Report: *rep}
	if b, err = file(FileReportText); err != nil {
		return nil, err
	}
	a.Text = string(b)
	// trace.jsonl is read as a stream: it can be large.
	if f, err := os.Open(filepath.Join(dir, FileTrace)); err == nil {
		tr, err := ReadTrace(f)
		f.Close()
		if err != nil {
			return nil, fail(FileTrace, err)
		}
		a.Trace = tr
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fail(FileTrace, err)
	}
	if b, err = file(FileSchedule); err != nil {
		return nil, err
	}
	if b != nil {
		s, err := fault.ReadSchedule(bytes.NewReader(b))
		if err != nil {
			return nil, fail(FileSchedule, fmt.Errorf("%s: %w", FileSchedule, err))
		}
		a.Schedule = &s
	}
	if a.History, err = file(FileHistory); err != nil {
		return nil, err
	}
	return a, nil
}

// readError is an error of Read: "artifact: <dir>: " and err's text without its own "artifact: "
// prefix. It wraps err.
type readError struct {
	dir string
	err error
}

func (e *readError) Error() string {
	return "artifact: " + e.dir + ": " + strings.TrimPrefix(e.err.Error(), "artifact: ")
}

func (e *readError) Unwrap() error { return e.err }
