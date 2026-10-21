// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"net/http"
	"time"
)

// HTTPTransport returns a new transport that dials through h. It sets DialContext to
// h.DialContext. The pool and timeout settings use the same values as http.DefaultTransport:
// MaxIdleConns 100, IdleConnTimeout 90 s, TLSHandshakeTimeout 10 s, and ExpectContinueTimeout 1 s.
// Proxy is nil, because the default proxy function reads environment variables, and
// ForceAttemptHTTP2 is false. All other fields are zero. Serve HTTP on the other side with
// (*http.Server).Serve and a listener from Host.Listen.
func HTTPTransport(h *Host) *http.Transport {
	return &http.Transport{
		DialContext:           h.DialContext,
		Proxy:                 nil,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// HTTPClient returns an http.Client whose Transport is HTTPTransport(h).
func HTTPClient(h *Host) *http.Client { return &http.Client{Transport: HTTPTransport(h)} }
