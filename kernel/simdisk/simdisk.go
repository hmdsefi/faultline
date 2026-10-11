// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package simdisk simulates one disk volume per node, with the crash behavior of real file
// systems. Each volume has two states: the visible one that reads return, and the durable one
// that survives a crash. A write becomes durable at File.Sync, and a create, remove or rename at
// Volume.SyncDir of its directory (at once under MetadataImmediate). When a node crashes,
// Config.Crash decides which unsynced writes survive, and Config.Metadata which unsynced namespace
// operations do.
//
// A node takes its volume from Disks.Volume in its BootFunc. The same volume comes back in every
// incarnation, with the durable state the crash left:
//
//	f, err := disks.Volume(n).Open("/wal")
//	if err == nil {
//		_, err = f.Append([]byte("put k1=v1\n"))
//	}
//	if err == nil {
//		err = f.Sync() // once Sync returns nil, the record survives a crash
//	}
//
// Inside faultline.Run, World.Disk holds the volumes, created from Options.Disk.
//
// All methods must be called from the simulation goroutine. Types are not safe for concurrent use.
package simdisk

import (
	"errors"
	"io/fs"
	"strconv"
	"time"
)

const (
	// DefaultSectorSize is used when Config.SectorSize is 0.
	DefaultSectorSize = 512
	// MaxSectorSize is the largest accepted Config.SectorSize.
	MaxSectorSize = 1 << 20
	// MaxFileSize is the largest size a file may reach (1 GiB). Larger writes and truncates
	// fail with ErrTooLarge.
	MaxFileSize int64 = 1 << 30
	// MaxLatency bounds every duration in LatencyConfig.
	MaxLatency = time.Hour
)

// CrashModel decides what happens to a file's pending data ops when its node crashes.
type CrashModel uint8

const (
	CrashAny          CrashModel = iota // one of the four models below, drawn per file per crash
	CrashLoseUnsynced                   // all pending data ops are lost
	CrashKeepPrefix                     // a random prefix of the pending data ops survives
	CrashKeepSubset                     // each pending data op survives independently
	CrashTorn                           // a random prefix survives and the next op survives partially, per sector
)

// String returns "any", "lose_unsynced", "keep_prefix", "keep_subset", "torn", or
// "CrashModel(<n>)".
func (m CrashModel) String() string {
	switch m {
	case CrashAny:
		return "any"
	case CrashLoseUnsynced:
		return "lose_unsynced"
	case CrashKeepPrefix:
		return "keep_prefix"
	case CrashKeepSubset:
		return "keep_subset"
	case CrashTorn:
		return "torn"
	}
	return "CrashModel(" + strconv.Itoa(int(m)) + ")"
}

// MetadataModel decides when namespace operations become durable.
type MetadataModel uint8

const (
	MetadataStrict    MetadataModel = iota // durable after SyncDir; on crash a random prefix of the log survives
	MetadataImmediate                      // durable immediately
)

// String returns "strict", "immediate", or "MetadataModel(<n>)".
func (m MetadataModel) String() string {
	switch m {
	case MetadataStrict:
		return "strict"
	case MetadataImmediate:
		return "immediate"
	}
	return "MetadataModel(" + strconv.Itoa(int(m)) + ")"
}

// FailedSyncModel decides what a failed File.Sync does to the file's pending data ops.
type FailedSyncModel uint8

const (
	FailedSyncDropDirty FailedSyncModel = iota // pending ops are dropped: readable until a crash, never durable
	FailedSyncKeepDirty                        // pending ops stay pending; a later successful Sync persists them
)

// String returns "drop_dirty", "keep_dirty", or "FailedSyncModel(<n>)".
func (m FailedSyncModel) String() string {
	switch m {
	case FailedSyncDropDirty:
		return "drop_dirty"
	case FailedSyncKeepDirty:
		return "keep_dirty"
	}
	return "FailedSyncModel(" + strconv.Itoa(int(m)) + ")"
}

// Op classifies operations for the latency model.
type Op uint8

const (
	OpRead  Op = iota + 1 // ReadAt, ReadFile
	OpWrite               // WriteAt, Append, Truncate
	OpSync                // Sync, SyncDir
	OpMeta                // Create, Open, Remove, Rename, Mkdir, MkdirAll, ReadDir, Stat, Close
)

// String returns "read", "write", "sync", "meta", or "Op(<n>)".
func (o Op) String() string {
	switch o {
	case OpRead:
		return "read"
	case OpWrite:
		return "write"
	case OpSync:
		return "sync"
	case OpMeta:
		return "meta"
	}
	return "Op(" + strconv.Itoa(int(o)) + ")"
}

// Latency is the latency of one operation class: Base plus kernel.Uniform(r, 0, Jitter).
type Latency struct {
	Base   time.Duration
	Jitter time.Duration
}

// LatencyConfig is the latency model that Volume.SampleLatency draws from. Volume methods take no
// virtual time themselves; code that models a slow disk waits for a sampled latency, for example
// with Node.After. The zero value means no latency.
type LatencyConfig struct {
	Read  Latency
	Write Latency
	Sync  Latency
	Meta  Latency
	// SlowPPM is the probability that an operation is also slow; a slow operation adds
	// kernel.Uniform(r, 0, Slow).
	SlowPPM uint32
	Slow    time.Duration
}

// DefaultLatency returns a latency model resembling a local NVMe SSD. Reads and writes take 20µs
// plus up to 80µs, syncs 500µs plus up to 1.5ms, and metadata operations 10µs plus up to 40µs.
// One operation in a thousand is slow and takes up to 20ms more.
func DefaultLatency() LatencyConfig {
	return LatencyConfig{
		Read:    Latency{Base: 20 * time.Microsecond, Jitter: 80 * time.Microsecond},
		Write:   Latency{Base: 20 * time.Microsecond, Jitter: 80 * time.Microsecond},
		Sync:    Latency{Base: 500 * time.Microsecond, Jitter: 1500 * time.Microsecond},
		Meta:    Latency{Base: 10 * time.Microsecond, Jitter: 40 * time.Microsecond},
		SlowPPM: 1000,
		Slow:    20 * time.Millisecond,
	}
}

// Config configures every volume of a Disks.
type Config struct {
	Crash      CrashModel
	Metadata   MetadataModel
	FailedSync FailedSyncModel
	SectorSize int   // 0 means DefaultSectorSize
	Capacity   int64 // initial capacity of every volume in bytes; 0 = unlimited
	// Latency is the model Volume.SampleLatency draws from. The zero value means no latency.
	Latency LatencyConfig
}

// DefaultConfig returns Config{SectorSize: DefaultSectorSize}: CrashAny, MetadataStrict,
// FailedSyncDropDirty, unlimited capacity, no latency.
func DefaultConfig() Config { return Config{SectorSize: DefaultSectorSize} }

// Validate reports the first invalid field, or nil. The error names the field, its value and the
// allowed values, for example "invalid config: SectorSize -1 out of range [0, 1048576]".
func (c Config) Validate() error {
	switch {
	case c.Crash > CrashTorn:
		return errors.New("invalid config: Crash " + strconv.Itoa(int(c.Crash)) + " is not a CrashModel")
	case c.Metadata > MetadataImmediate:
		return errors.New("invalid config: Metadata " + strconv.Itoa(int(c.Metadata)) + " is not a MetadataModel")
	case c.FailedSync > FailedSyncKeepDirty:
		return errors.New("invalid config: FailedSync " + strconv.Itoa(int(c.FailedSync)) + " is not a FailedSyncModel")
	case c.SectorSize < 0 || c.SectorSize > MaxSectorSize:
		return errors.New("invalid config: SectorSize " + strconv.Itoa(c.SectorSize) + " out of range [0, 1048576]")
	case c.Capacity < 0:
		return errors.New("invalid config: Capacity " + strconv.FormatInt(c.Capacity, 10) + " is negative")
	}
	classes := []struct {
		name string
		l    Latency
	}{{"Read", c.Latency.Read}, {"Write", c.Latency.Write}, {"Sync", c.Latency.Sync}, {"Meta", c.Latency.Meta}}
	for _, cl := range classes {
		if err := checkLatency("Latency."+cl.name+".Base", cl.l.Base); err != nil {
			return err
		}
		if err := checkLatency("Latency."+cl.name+".Jitter", cl.l.Jitter); err != nil {
			return err
		}
	}
	if err := checkLatency("Latency.Slow", c.Latency.Slow); err != nil {
		return err
	}
	if c.Latency.SlowPPM > 1_000_000 {
		return errors.New("invalid config: Latency.SlowPPM " + strconv.FormatUint(uint64(c.Latency.SlowPPM), 10) + " exceeds 1000000")
	}
	return nil
}

func checkLatency(field string, d time.Duration) error {
	if d < 0 || d > MaxLatency {
		return errors.New("invalid config: " + field + " " + d.String() + " out of range [0s, " + MaxLatency.String() + "]")
	}
	return nil
}

// Errors returned by Volume and File methods. Every error except io.EOF from File.ReadAt is a
// *fs.PathError that wraps one of these, so errors.Is works. ErrClosed, ErrNotExist, ErrExist and
// ErrInvalid are the io/fs errors, so they also match the os errors of the same name.
var (
	ErrIO       = errors.New("simdisk: input/output error")
	ErrNoSpace  = errors.New("simdisk: no space left on device")
	ErrStale    = errors.New("simdisk: stale file handle")
	ErrClosed   = fs.ErrClosed
	ErrNotExist = fs.ErrNotExist
	ErrExist    = fs.ErrExist
	ErrInvalid  = fs.ErrInvalid
	ErrIsDir    = errors.New("simdisk: is a directory")
	ErrNotDir   = errors.New("simdisk: not a directory")
	ErrNotEmpty = errors.New("simdisk: directory not empty")
	ErrTooLarge = errors.New("simdisk: file too large")
)
