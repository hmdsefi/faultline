// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import (
	"errors"
	"time"
)

// errPending is returned by the temporary methods below. They let *Conn and *Listener satisfy
// net.Conn and net.Listener until the byte stream, deadline and close code replaces them.
var errPending = errors.New("netsim: not implemented yet")

func (c *Conn) Read(b []byte) (int, error)         { return 0, errPending }
func (c *Conn) Write(b []byte) (int, error)        { return 0, errPending }
func (c *Conn) SetDeadline(t time.Time) error      { return errPending }
func (c *Conn) SetReadDeadline(t time.Time) error  { return errPending }
func (c *Conn) SetWriteDeadline(t time.Time) error { return errPending }
func (c *Conn) Close() error                       { return errPending }
func (l *Listener) Close() error                   { return errPending }
