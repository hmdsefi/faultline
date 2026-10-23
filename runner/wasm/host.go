// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package wasm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// epochUnixNano is kernel.Epoch (2000-01-01 UTC) in Unix nanoseconds.
var epochUnixNano = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()

// Host compiles a module once and runs instances of it.
type Host struct {
	rt      wazero.Runtime
	mod     wazero.CompiledModule
	module  string        // the module path as given: argv[0] of every instance
	quantum int64         // FAULTLINE_WASM_CLOCK_QUANTUM
	Compile time.Duration // compilation time (fast when cached)
}

// NewHost reads and compiles module with the compilation cache in cacheDir.
func NewHost(ctx context.Context, module, cacheDir string, quantum int64) (*Host, error) {
	cache, err := wazero.NewCompilationCacheWithDir(cacheDir)
	if err != nil {
		return nil, err
	}
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCompilationCache(cache))
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	bin, err := os.ReadFile(module)
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	start := time.Now()
	mod, err := rt.CompileModule(ctx, bin)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("compile %s: %v", module, err)
	}
	return &Host{rt: rt, mod: mod, module: module, quantum: quantum, Compile: time.Since(start)}, nil
}

// Close releases the runtime.
func (h *Host) Close(ctx context.Context) error { return h.rt.Close(ctx) }

// Instance describes one instantiation.
type Instance struct {
	Seed   uint64            // instance seed
	Args   []string          // arguments after argv[0]
	Env    map[string]string // the complete environment, PWD included
	Stdout io.Writer
	Stderr io.Writer
}

// Result is the outcome of an instance.
type Result struct {
	Code       int           // proc_exit code; 0 if the module returned
	Run        time.Duration // wall time of the instance
	Virtual    time.Duration // virtual time consumed
	ClockReads int
}

// Run instantiates the module once, with its own virtual clock and random source. A trap or
// another runtime error is returned as an error; a proc_exit code is Result.Code.
func (h *Host) Run(ctx context.Context, in Instance) (Result, error) {
	var v int64 // virtual nanoseconds since instance start
	reads := 0
	q := h.quantum
	cfg := wazero.NewModuleConfig().
		WithName(""). // anonymous: the compiled module can be instantiated again
		WithArgs(append([]string{h.module}, in.Args...)...).
		WithFSConfig(wazero.NewFSConfig().WithDirMount("/", "/")).
		WithStdin(bytes.NewReader(nil)).
		WithStdout(in.Stdout).
		WithStderr(in.Stderr).
		WithRandSource(NewPRNG(in.Seed)).
		WithNanotime(func() int64 { reads++; v += q; return 1_000_000_000 + v }, 1). // F4: never 0
		WithWalltime(func() (int64, int32) {
			reads++
			v += q
			ns := epochUnixNano + v
			return ns / 1e9, int32(ns % 1e9)
		}, 1).
		WithNanosleep(func(ns int64) {
			if ns > 0 {
				v += ns
			}
		}).
		WithOsyield(func() {})
	keys := make([]string, 0, len(in.Env))
	for k := range in.Env {
		keys = append(keys, k)
	}
	slices.Sort(keys) // EXB-011: ascending key order
	for _, k := range keys {
		cfg = cfg.WithEnv(k, in.Env[k])
	}
	start := time.Now()
	m, err := h.rt.InstantiateModule(ctx, h.mod, cfg)
	res := Result{Run: time.Since(start), Virtual: time.Duration(v), ClockReads: reads}
	if m != nil {
		m.Close(ctx)
	}
	var ee *sys.ExitError
	switch {
	case err == nil:
		return res, nil
	case errors.As(err, &ee):
		res.Code = int(ee.ExitCode())
		return res, nil
	}
	return res, err
}

// HostEnv returns the runner's environment as a map with PWD set to the working directory.
// Keys are unique, so the guest sees each variable once (Go's wasip1 runtime keeps the first of
// duplicate keys); callers override entries before Run.
func HostEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, val, ok := strings.Cut(kv, "="); ok && k != "" {
			env[k] = val
		}
	}
	if wd, err := os.Getwd(); err == nil {
		env["PWD"] = wd
	}
	return env
}
