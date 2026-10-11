// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simdisk_test

import (
	"fmt"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
)

// A node appends two records to its log and syncs only the first before it crashes. After the
// restart the log holds the synced record only. CrashLoseUnsynced fixes the outcome; the default
// model, CrashAny, draws one of four crash models per file and crash.
func Example() {
	s := kernel.New(kernel.Config{Seed: 1})
	disks := simdisk.New(s, simdisk.Config{Crash: simdisk.CrashLoseUnsynced})
	n := s.AddNode("n1", func(n *kernel.Node) {
		vol := disks.Volume(n)
		data, _ := vol.ReadFile("/log")
		fmt.Printf("boot %d: %q\n", n.Incarnation(), data)
		if n.Incarnation() > 1 {
			return
		}
		f, err := vol.Open("/log")
		if err == nil {
			_, err = f.Append([]byte("put k1=v1\n"))
		}
		if err == nil {
			err = f.Sync()
		}
		if err == nil {
			_, err = f.Append([]byte("put k2=v2\n")) // never synced
		}
		if err != nil {
			fmt.Println(err)
		}
	})
	// The file exists, durably and empty, before the first boot.
	if err := disks.Volume(n).WriteFileDurable("/log", nil); err != nil {
		fmt.Println(err)
	}
	s.RunFor(time.Second)
	data, _ := disks.Volume(n).ReadFile("/log")
	fmt.Printf("before the crash: %q\n", data)
	n.Crash()
	n.Restart()
	s.RunFor(time.Second)
	// Output:
	// boot 1: ""
	// before the crash: "put k1=v1\nput k2=v2\n"
	// boot 2: "put k1=v1\n"
}
