// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package netsim

import "errors"

// errPending is returned by the temporary methods below. They let *Conn and *Listener satisfy
// net.Conn and net.Listener until the close code replaces both Close methods and deletes this file.
var errPending = errors.New("netsim: not implemented yet")

func (c *Conn) Close() error     { return errPending }
func (l *Listener) Close() error { return errPending }
