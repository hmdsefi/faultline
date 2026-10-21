// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/hmdsefi/faultline"
	"github.com/hmdsefi/faultline/shims/netsim"
)

func TestHTTPTransportSettings(t *testing.T) {
	var tr, tr2 *http.Transport
	var cl *http.Client
	world(t, netsim.Config{}, faultline.Options{Duration: time.Millisecond}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			tr, tr2, cl = netsim.HTTPTransport(p.Net), netsim.HTTPTransport(p.Net), netsim.HTTPClient(p.Net)
			return nil
		})
	})
	if tr.Proxy != nil || tr.ForceAttemptHTTP2 || tr.MaxIdleConns != 100 || tr.IdleConnTimeout != 90*time.Second ||
		tr.TLSHandshakeTimeout != 10*time.Second || tr.ExpectContinueTimeout != time.Second || tr.DialContext == nil {
		t.Fatalf("transport %+v", tr)
	}
	set := map[string]bool{"DialContext": true, "MaxIdleConns": true, "IdleConnTimeout": true,
		"TLSHandshakeTimeout": true, "ExpectContinueTimeout": true}
	v := reflect.ValueOf(tr).Elem()
	for i := range v.NumField() {
		if f := v.Type().Field(i); f.IsExported() && !set[f.Name] && !v.Field(i).IsZero() {
			t.Errorf("field %s = %v, want zero", f.Name, v.Field(i))
		}
	}
	if tr == tr2 {
		t.Error("HTTPTransport returned the same transport twice")
	}
	ct, ok := cl.Transport.(*http.Transport)
	if !ok || ct == tr || ct.MaxIdleConns != 100 || ct.DialContext == nil || ct.Proxy != nil || ct.IdleConnTimeout != 90*time.Second ||
		cl.Timeout != 0 || cl.Jar != nil || cl.CheckRedirect != nil {
		t.Fatalf("client %+v", cl)
	}
}

func TestATNSM29_HTTPOverNetsim(t *testing.T) {
	var statuses result[[]string]
	var rec records
	post := pattern(1 << 20)
	sum := sha256.Sum256(post)
	world(t, netsim.Config{}, faultline.Options{Duration: 30 * time.Second}, func(w *faultline.World) {
		w.AddProcess("a", func(ctx context.Context, p *faultline.Process) error {
			cl := netsim.HTTPClient(p.Net)
			defer cl.CloseIdleConnections()
			var got []string
			for i := range 10 {
				resp, err := cl.Get(fmt.Sprintf("http://b:80/x%d", i))
				if err != nil {
					return err
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				got = append(got, fmt.Sprintf("%d %s", resp.StatusCode, b))
			}
			resp, err := cl.Post("http://b:80/post", "application/octet-stream", bytes.NewReader(post))
			if err != nil {
				return err
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			got = append(got, fmt.Sprintf("%d %s", resp.StatusCode, b))
			statuses.put(got)
			<-ctx.Done()
			return nil
		})
		w.AddProcess("b", func(ctx context.Context, p *faultline.Process) error {
			ln, err := p.Net.Listen("tcp", ":80")
			if err != nil {
				return err
			}
			srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					b, _ := io.ReadAll(r.Body)
					s := sha256.Sum256(b)
					fmt.Fprintf(rw, "%x", s)
					return
				}
				fmt.Fprintf(rw, "hello %s", r.URL.Path) //nolint:gosec // G705: the simulated client reads the path back as plain text
			})}
			go func() { <-ctx.Done(); srv.Close() }()
			if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed && !isClosed(err) {
				return err
			}
			return nil
		})
		rec.capture(w)
	})
	got := statuses.get(t)
	for i := range 10 {
		if got[i] != fmt.Sprintf("200 hello /x%d", i) {
			t.Fatalf("GET %d: %q", i, got[i])
		}
	}
	if got[10] != fmt.Sprintf("200 %x", sum) {
		t.Fatalf("POST: %q", got[10])
	}
	if n := len(rec.kind("net.conn.dial")); n != 1 {
		t.Fatalf("%d dials, want 1 (keep-alive)", n)
	}
}

func isClosed(err error) bool { return errors.Is(err, net.ErrClosed) }
