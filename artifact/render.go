package artifact

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hmdsefi/faultline/kernel/fault"
)

// RenderOptions configure Render. Zero fields take the defaults.
type RenderOptions struct {
	SliceCap        int // 0: DefaultSliceCap
	TimelineRecords int // 0: DefaultTimelineRecords; otherwise >= MinTimelineRecords
}

// Render regenerates timeline.txt, hb.mmd and timeline.html in dir from report.json and
// trace.jsonl, and returns the absolute paths it wrote, in that order (ART-090).
func Render(dir string, opts RenderOptions) ([]string, error) {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("artifact: render %s: "+format, append([]any{dir}, args...)...)
	}
	sliceCap := opts.SliceCap
	switch {
	case sliceCap == 0:
		sliceCap = DefaultSliceCap
	case sliceCap < 0:
		return nil, fail("slice cap %d is negative", sliceCap)
	}
	maxRecords := opts.TimelineRecords
	switch {
	case maxRecords == 0:
		maxRecords = DefaultTimelineRecords
	case maxRecords < MinTimelineRecords:
		return nil, fail("timeline records %d is below %d", maxRecords, MinTimelineRecords)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fail("%w", err)
	}
	rf, err := os.Open(filepath.Join(abs, FileReport))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fail("not a faultline artifact directory (no report.json); pass the directory of one seed, which holds report.json and trace.jsonl")
	}
	if err != nil {
		return nil, fail("open %s: %w", FileReport, pathErr(err))
	}
	rep, err := ReadReport(rf)
	rf.Close()
	if err != nil {
		return nil, fail("%s", strings.TrimPrefix(err.Error(), "artifact: "))
	}
	tf, err := os.Open(filepath.Join(abs, FileTrace))
	if err != nil {
		return nil, fail("open %s: %w", FileTrace, pathErr(err))
	}
	tr, err := ReadTrace(tf)
	tf.Close()
	if err != nil {
		return nil, fail("%s", strings.TrimPrefix(err.Error(), "artifact: "))
	}
	// schedule.json is parsed and written again, as Write writes it, so only a schedule reaches
	// timeline.html, never the content of some other file it may link to.
	var sched []byte
	if b, err := os.ReadFile(filepath.Join(abs, FileSchedule)); err == nil {
		sc, err := fault.ReadSchedule(bytes.NewReader(b))
		if err != nil {
			return nil, fail("%s: %w", FileSchedule, err)
		}
		var buf bytes.Buffer
		if err := sc.Write(&buf); err != nil {
			return nil, fail("%s: %w", FileSchedule, err)
		}
		sched = buf.Bytes()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fail("read %s: %w", FileSchedule, pathErr(err))
	}
	root := uint64(0)
	if rep.Failure != nil {
		root = rep.Failure.RecordSeq
	}
	s := CausalSlice(tr.Records, root, sliceCap)
	outputs := []struct {
		name  string
		write func(w io.Writer) error
	}{
		{FileTimelineText, func(w io.Writer) error { return WriteTimelineText(w, rep, tr, s) }},
		{FileHB, func(w io.Writer) error { return WriteHB(w, tr, root, sliceCap) }},
		{FileTimelineHTML, func(w io.Writer) error { return WriteTimelineHTML(w, rep, tr, sched, s, maxRecords) }},
	}
	// The inputs are read, so dir is an artifact: remove the temporary files an interrupted Render
	// left. Their names are os.CreateTemp's for Render's own prefixes: the prefix, then digits.
	if entries, err := os.ReadDir(abs); err == nil {
		for _, e := range entries {
			for _, o := range outputs {
				rest, ok := strings.CutPrefix(e.Name(), tempPrefix(o.name))
				if ok && rest != "" && strings.Trim(rest, "0123456789") == "" {
					_ = os.Remove(filepath.Join(abs, e.Name()))
				}
			}
		}
	}
	// All three are written before any is renamed, so a failed write, such as the slice error of
	// timeline.html, changes no file.
	tmps := make([]string, 0, len(outputs))
	defer func() {
		for _, tmp := range tmps {
			if tmp != "" {
				_ = os.Remove(tmp)
			}
		}
	}()
	for _, o := range outputs {
		tmp, err := writeTemp(abs, o.name, o.write)
		if err != nil {
			return nil, fail("write %s: %w", o.name, err)
		}
		tmps = append(tmps, tmp)
	}
	// A directory in a target's place would fail its rename after earlier ones succeeded, so it
	// fails Render before the first rename.
	for _, o := range outputs {
		if fi, err := os.Lstat(filepath.Join(abs, o.name)); err == nil && fi.IsDir() {
			return nil, fail("%s is a directory", o.name)
		}
	}
	paths := make([]string, 0, len(outputs))
	for i, o := range outputs {
		target := filepath.Join(abs, o.name)
		if err := os.Rename(tmps[i], target); err != nil {
			return nil, fail("write %s: %w", o.name, err)
		}
		tmps[i] = ""
		paths = append(paths, target)
	}
	return paths, nil
}

// tempPrefix is the name prefix of the temporary files Render writes for name.
func tempPrefix(name string) string { return "." + name + ".tmp-" }

// pathErr returns the inner error of a *fs.PathError, so that the message names the file once,
// as Read's does.
func pathErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// writeTemp writes a temporary file for name in dir, with mode 0o644, and returns its path. On an
// error it removes the file.
func writeTemp(dir, name string, write func(w io.Writer) error) (string, error) {
	f, err := os.CreateTemp(dir, tempPrefix(name))
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	err = write(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}
