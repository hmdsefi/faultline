// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk

import (
	"runtime"
	"testing"
	"weak"

	"github.com/hmdsefi/faultline/kernel"
)

// DSK-024: syncDir compacts the namespace log in place and clears the slots past the ops it keeps,
// so a dropped op no longer references its inodes. SyncDir("/d") drops the create and the remove
// of /d/f and keeps Mkdir("/x"): the slots past it are zero, and the removed file's inode, with
// its 1 MiB of data, is collected.
func TestSyncDirReleasesDroppedOps(t *testing.T) {
	s := kernel.New(kernel.Config{Seed: 1})
	d := New(s, DefaultConfig())
	a := s.AddNode("a", func(*kernel.Node) {})
	s.RunUntil(0)
	v := d.Volume(a)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	check(v.Mkdir("/d"))
	check(v.SyncDir("/"))
	check(v.Mkdir("/x")) // stays in the log: / is not synced again
	f, err := v.Create("/d/f")
	check(err)
	_, err = f.Append(make([]byte, 1<<20))
	check(err)
	inode := weak.Make(f.f)
	check(f.Close())
	f = nil
	check(v.Remove("/d/f"))
	check(v.SyncDir("/d"))
	if len(v.nslog) != 1 || v.nslog[0].kind != "mkdir" {
		t.Fatalf("log after SyncDir(/d): %d ops, want only mkdir /x", len(v.nslog))
	}
	for i, op := range v.nslog[len(v.nslog):cap(v.nslog)] {
		if op.kind != "" || op.muts != nil {
			t.Fatalf("log slot %d past the kept op still holds a %s op", len(v.nslog)+i, op.kind)
		}
	}
	runtime.GC()
	runtime.GC()
	if inode.Value() != nil {
		t.Fatalf("the removed file's inode is still reachable after SyncDir")
	}
	runtime.KeepAlive(v)
}
