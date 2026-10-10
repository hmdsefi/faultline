// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet

import (
	"fmt"
	"strconv"

	"github.com/hmdsefi/faultline/kernel"
)

// Handle installs h as the handler of n's current incarnation, replacing any previous handler.
// h == nil removes the handler. It panics if n is nil, belongs to another Sim, or is down.
// The handler is removed automatically when n crashes.
func (nw *Network) Handle(n *kernel.Node, h Handler) {
	nw.register()
	i := nw.nodeIndex("Handle", n)
	if n.State() == kernel.NodeDown {
		panic(fmt.Sprintf("simnet: Handle: node %s (id %d) is down", n.Name(), n.ID()))
	}
	ns := &nw.nodes[i]
	ns.handler = h
	ns.inc = n.Incarnation()
	text := "handler installed on " + ns.name
	if h == nil {
		text = "handler removed from " + ns.name
	}
	nw.emit("net.handle", n.ID(), text,
		attr("node", ns.name), attr("installed", strconv.FormatBool(h != nil)))
}
