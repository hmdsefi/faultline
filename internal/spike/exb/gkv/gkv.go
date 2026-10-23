// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package gkv is corpus program C2 of the exact-backend spike: a goroutine-style KV
// store with a fixed leader, two followers, a write-ahead log on fsx, TCP replication and HTTP
// clients.
package gkv

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/check/history"
	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simdisk"
	"github.com/hmdsefi/faultline/shims/fsx"
	"github.com/hmdsefi/faultline/shims/netsim"
)

// Record is one WAL entry.
type Record struct {
	Index uint64 `json:"index"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Frame encodes r as a uint32 big-endian length followed by its JSON.
func Frame(r Record) []byte {
	body, _ := json.Marshal(r)
	out := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(out, uint32(len(body))) //nolint:gosec // G115: a record's JSON is far below 4 GiB
	copy(out[4:], body)
	return out
}

// ParseWAL returns the records of data and the length of its valid prefix. A torn tail (a
// length beyond the remaining bytes, or JSON that does not parse) ends the prefix.
func ParseWAL(data []byte) ([]Record, int) {
	var recs []Record
	off := 0
	for len(data)-off >= 4 {
		n := int(binary.BigEndian.Uint32(data[off:]))
		if n > len(data)-off-4 {
			break
		}
		var r Record
		if err := json.Unmarshal(data[off+4:off+4+n], &r); err != nil {
			break
		}
		recs, off = append(recs, r), off+4+n
	}
	return recs, off
}

// wal is the append-only log /wal of one incarnation. Holding mu across fsx calls is safe: fsx
// operations never wait for the driver (FSX §3).
type wal struct {
	mu   sync.Mutex
	f    fsx.File
	recs []Record
}

// openWAL replays /wal, truncates a torn tail and opens the log for appending (EXB-051).
func openWAL(fsys fsx.FS) (*wal, error) {
	data, err := fsx.ReadFile(fsys, "/wal")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	recs, valid := ParseWAL(data)
	f, err := fsys.OpenFile("/wal", os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	if valid < len(data) {
		if err := f.Truncate(int64(valid)); err != nil {
			f.Close()
			return nil, err
		}
		if err := f.Sync(); err != nil {
			f.Close()
			return nil, err
		}
	}
	if err := fsys.SyncDir("/"); err != nil { // make the entry of /wal durable
		f.Close()
		return nil, err
	}
	return &wal{f: f, recs: recs}, nil
}

// append writes and syncs one record.
func (l *wal) append(r Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(Frame(r)); err != nil {
		return err
	}
	if err := l.f.Sync(); err != nil {
		return err
	}
	l.recs = append(l.recs, r)
	return nil
}

// sleep waits for d or the end of ctx.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// replication is one record sent to one follower.
type replication struct {
	frame []byte
	index uint64
	acks  chan<- uint64
}

// replicate owns the leader's connection to one follower: it redials every 200 ms after an
// error, sends each record and forwards the follower's 8-byte acknowledgement.
func replicate(ctx context.Context, p *faultline.Process, addr string, reqs <-chan replication) {
	var conn net.Conn
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	for ctx.Err() == nil {
		if conn == nil {
			c, err := p.Net.DialContext(ctx, "tcp", addr)
			if err != nil {
				sleep(ctx, 200*time.Millisecond)
				continue
			}
			conn = c
		}
		var r replication
		select {
		case r = <-reqs:
		case <-ctx.Done():
			return
		}
		ack := make([]byte, 8)
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if _, err := conn.Write(r.frame); err != nil {
			conn.Close()
			conn = nil
			continue
		}
		if _, err := io.ReadFull(conn, ack); err != nil {
			conn.Close()
			conn = nil
			continue
		}
		if binary.BigEndian.Uint64(ack) == r.index {
			r.acks <- r.index
		}
	}
}

// Leader is s1's main: it serves PUT and GET on :8080 and replicates every write to followers.
func Leader(followers []string) faultline.ProcessFunc {
	return func(ctx context.Context, p *faultline.Process) error {
		l, err := openWAL(p.FS)
		if err != nil {
			return err
		}
		var kvMu sync.Mutex
		kv := map[string]string{}
		for _, r := range l.recs {
			kv[r.Key] = r.Value
		}
		next := uint64(len(l.recs)) + 1
		sem := make(chan struct{}, 1) // serializes PUTs; waiting on a channel is durable
		var reps []chan replication
		for _, addr := range followers {
			ch := make(chan replication, 64)
			reps = append(reps, ch)
			p.Go(func() { replicate(ctx, p, addr, ch) })
		}
		mux := http.NewServeMux()
		mux.HandleFunc("PUT /k/{key}", func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			select {
			case sem <- struct{}{}:
			case <-r.Context().Done():
				return
			}
			defer func() { <-sem }()
			rec := Record{Index: next, Key: r.PathValue("key"), Value: string(body)}
			if err := l.append(rec); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			next++
			kvMu.Lock()
			kv[rec.Key] = rec.Value
			kvMu.Unlock()
			acks := make(chan uint64, len(reps))
			for _, ch := range reps {
				select {
				case ch <- replication{frame: Frame(rec), index: rec.Index, acks: acks}:
				default: // the follower's queue is full: it will not ack this record
				}
			}
			t := time.NewTimer(time.Second)
			defer t.Stop()
			select {
			case <-acks:
				w.WriteHeader(http.StatusNoContent)
			case <-t.C:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		})
		mux.HandleFunc("GET /k/{key}", func(w http.ResponseWriter, r *http.Request) {
			kvMu.Lock()
			v, ok := kv[r.PathValue("key")]
			kvMu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, v)
		})
		ln, err := p.Net.Listen("tcp", ":8080")
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
}

// Follower is s2's and s3's main: it appends every record it receives on :7000 to /wal, syncs,
// and replies with the record's index.
func Follower(ctx context.Context, p *faultline.Process) error {
	l, err := openWAL(p.FS)
	if err != nil {
		return err
	}
	ln, err := p.Net.Listen("tcp", ":7000")
	if err != nil {
		return err
	}
	p.Go(func() {
		<-ctx.Done()
		ln.Close()
	})
	for {
		c, err := ln.Accept()
		if err != nil {
			return nil
		}
		p.Go(func() { serveFollower(c, l) })
	}
}

func serveFollower(c net.Conn, l *wal) {
	defer c.Close()
	hdr := make([]byte, 4)
	for {
		if _, err := io.ReadFull(c, hdr); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(hdr))
		if _, err := io.ReadFull(c, body); err != nil {
			return
		}
		var r Record
		if err := json.Unmarshal(body, &r); err != nil {
			return
		}
		if err := l.append(r); err != nil {
			return
		}
		ack := make([]byte, 8)
		binary.BigEndian.PutUint64(ack, r.Index)
		if _, err := c.Write(ack); err != nil {
			return
		}
	}
}

// Client is c1's and c2's main: every Uniform(50ms, 150ms) a PUT or a GET (equal chance) of one
// of 5 keys, each recorded in w.History through p.Do. PUT values are unique ("<client>-<n>").
func Client(w *faultline.World) faultline.ProcessFunc {
	return func(ctx context.Context, p *faultline.Process) error {
		hc := netsim.HTTPClient(p.Net)
		hc.Timeout = 2 * time.Second
		name := p.Node.Name()
		for n := 1; ; n++ {
			sleep(ctx, kernel.Uniform(p.Rand(), 50*time.Millisecond, 150*time.Millisecond))
			if ctx.Err() != nil {
				return nil
			}
			key := fmt.Sprintf("k%d", p.Rand().IntN(5))
			if p.Rand().IntN(2) == 0 {
				op(ctx, p, w, hc, http.MethodPut, key, fmt.Sprintf("%s-%d", name, n))
			} else {
				op(ctx, p, w, hc, http.MethodGet, key, "")
			}
		}
	}
}

// op performs one request and records it: PUT 204 is OK, GET 200 and 404 are OK, PUT 503 and
// errors are Info (indeterminate).
func op(ctx context.Context, p *faultline.Process, w *faultline.World, hc *http.Client, method, key, value string) {
	name := p.Node.Name()
	f := strings.ToLower(method)
	var input any = map[string]string{"key": key}
	if method == http.MethodPut {
		input = map[string]string{"key": key, "value": value}
	}
	var id int64
	if p.Do("invoke", func() { id = w.History.Invoke(name, f, input) }) != nil {
		return
	}
	status, output := history.Info, any(nil)
	var body io.Reader
	if method == http.MethodPut {
		body = strings.NewReader(value)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://s1:8080/k/"+key, body)
	if err == nil {
		resp, err := hc.Do(req)
		if err == nil {
			b, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case rerr != nil:
			case method == http.MethodPut && resp.StatusCode == http.StatusNoContent:
				status = history.OK
			case method == http.MethodGet && resp.StatusCode == http.StatusOK:
				status, output = history.OK, string(b)
			case method == http.MethodGet && resp.StatusCode == http.StatusNotFound:
				status = history.OK
			}
		}
	}
	_ = p.Do("complete", func() { w.History.Complete(id, status, output) })
}

// CheckDurable is the final check of TestGKV: every PUT acknowledged with 204 is a
// record of s1's /wal. The last acknowledged value of a key need not be its last record: a later
// PUT that got 503 is still in the log.
func CheckDurable(ops []history.Op, v *simdisk.Volume) error {
	data, err := v.ReadFile("/wal")
	if err != nil {
		return err
	}
	recs, _ := ParseWAL(data)
	have := map[[2]string]bool{}
	for _, r := range recs {
		have[[2]string{r.Key, r.Value}] = true
	}
	for _, o := range ops {
		if o.F != "put" || o.Status != history.OK {
			continue
		}
		var in struct{ Key, Value string }
		if err := o.DecodeInput(&in); err != nil {
			return err
		}
		if !have[[2]string{in.Key, in.Value}] {
			return fmt.Errorf("put %s=%s acknowledged at %s is not in s1's /wal", in.Key, in.Value, o.Return)
		}
	}
	return nil
}
