// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package httpcorpus is corpus program C3 of the exact-backend spike: net/http over
// netsim with concurrent requests on one shared client.
package httpcorpus

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/shims/netsim"
)

// Server serves GET /k/<n> (n bytes), PUT /k (stores the body) and POST /echo (streams the body
// back in 4 KiB chunks) on :80.
func Server(ctx context.Context, p *faultline.Process) error {
	var mu sync.Mutex
	stored := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /k/{n}", func(w http.ResponseWriter, r *http.Request) {
		n, err := strconv.Atoi(r.PathValue("n"))
		if err != nil || n < 0 || n > 1<<16 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write(bytes.Repeat([]byte{'x'}, n))
	})
	mux.HandleFunc("PUT /k", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		stored += len(b)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /echo", func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).EnableFullDuplex(); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		buf := make([]byte, 4096)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				http.NewResponseController(w).Flush()
			}
			if err != nil {
				return
			}
		}
	})
	ln, err := p.Net.Listen("tcp", ":80")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux} //nolint:gosec // G112: a ReadHeaderTimeout would add a timer to the measured program
	p.Go(func() {
		<-ctx.Done()
		srv.Close()
	})
	_ = srv.Serve(ln)
	return nil
}

// request performs request j of a goroutine: kind 0 GET, 1 PUT, 2 POST /echo, with size bytes.
func request(ctx context.Context, hc *http.Client, kind, size int) error {
	var req *http.Request
	var err error
	payload := bytes.Repeat([]byte{'p'}, size)
	switch kind {
	case 0:
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, "http://srv/k/"+strconv.Itoa(size), nil)
	case 1:
		req, err = http.NewRequestWithContext(ctx, http.MethodPut, "http://srv/k", bytes.NewReader(payload))
	default:
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, "http://srv/echo", bytes.NewReader(payload))
	}
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	switch {
	case kind == 0 && (resp.StatusCode != http.StatusOK || len(body) != size):
		return fmt.Errorf("GET /k/%d: status %d, %d bytes", size, resp.StatusCode, len(body))
	case kind == 1 && resp.StatusCode != http.StatusNoContent:
		return fmt.Errorf("PUT /k (%d bytes): status %d", size, resp.StatusCode)
	case kind == 2 && (resp.StatusCode != http.StatusOK || !bytes.Equal(body, payload)):
		return fmt.Errorf("POST /echo (%d bytes): status %d, %d bytes back", size, resp.StatusCode, len(body))
	}
	return nil
}

// Client is cli's main: 3 goroutines, each 100 requests with sizes from p.Rand() in [1, 65536],
// on one shared client. Failures are counted; the first error is kept.
func Client(failures *atomic.Int64, first *atomic.Value) faultline.ProcessFunc {
	return func(ctx context.Context, p *faultline.Process) error {
		hc := netsim.HTTPClient(p.Net)
		var wg sync.WaitGroup
		for g := range 3 {
			wg.Add(1)
			p.Go(func() {
				defer wg.Done()
				for j := range 100 {
					size := 1 + p.Rand().IntN(1<<16)
					if err := request(ctx, hc, (g+j)%3, size); err != nil && ctx.Err() == nil {
						if failures.Add(1) == 1 {
							first.Store(err.Error())
						}
					}
				}
			})
		}
		wg.Wait()
		<-ctx.Done()
		return nil
	}
}
