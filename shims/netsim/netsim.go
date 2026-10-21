// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

// Package netsim provides net.Conn, net.Listener and net.PacketConn implementations over the
// simulated network, for processes in faultline goroutine mode.
package netsim

import (
	"errors"
	"fmt"
	"time"

	"github.com/hmdsefi/faultline/kernel"
	"github.com/hmdsefi/faultline/kernel/simnet"
)

// Driver is the subset of the goroutine-mode driver contract that netsim uses.
type Driver interface {
	Lock()
	Unlock()
	Alive(node kernel.NodeID, inc uint32) bool
	Post(node kernel.NodeID, inc uint32, kind, key string, run, cancel func())
	OnDriver() bool
}

// MaxDatagram is the largest UDP payload (65535 - 8 - 20).
const MaxDatagram = 65507

// ErrProcessGone is returned (wrapped in *net.OpError) by Host methods of an incarnation that
// crashed, exited, or whose run ended.
var ErrProcessGone = errors.New("netsim: process is not running")

// Config configures a Stack. Zero fields take the defaults of DefaultConfig.
type Config struct {
	MSS           int           // max data bytes per segment; default 1460; 64..65535
	SendBuffer    int           // per endpoint; default 65536; >= MSS
	RecvBuffer    int           // per endpoint; default 65536; >= MSS
	Backlog       int           // per listener (half-open + not yet accepted); default 128; >= 1
	ConnTimeout   time.Duration // default 30s; > 0
	RTOMin        time.Duration // first data/fin retransmission timeout; default 200ms; > 0
	RTOMax        time.Duration // retransmission timeout cap; default 10s; >= RTOMin and >= SynRTO
	SynRTO        time.Duration // first syn retransmission timeout; default 1s; > 0
	Loopback      time.Duration // one-way delay on a node's own links; default 10µs; 0..simnet.MaxDelay
	UDPRecvBuffer int           // per UDP socket, bytes; default 212992; >= MaxDatagram
}

// DefaultConfig returns the defaults listed on Config.
func DefaultConfig() Config {
	return Config{
		MSS: 1460, SendBuffer: 65536, RecvBuffer: 65536, Backlog: 128,
		ConnTimeout: 30 * time.Second, RTOMin: 200 * time.Millisecond, RTOMax: 10 * time.Second,
		SynRTO: time.Second, Loopback: 10 * time.Microsecond, UDPRecvBuffer: 212992,
	}
}

// withDefaults replaces zero fields by DefaultConfig's values.
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.MSS == 0 {
		c.MSS = d.MSS
	}
	if c.SendBuffer == 0 {
		c.SendBuffer = d.SendBuffer
	}
	if c.RecvBuffer == 0 {
		c.RecvBuffer = d.RecvBuffer
	}
	if c.Backlog == 0 {
		c.Backlog = d.Backlog
	}
	if c.ConnTimeout == 0 {
		c.ConnTimeout = d.ConnTimeout
	}
	if c.RTOMin == 0 {
		c.RTOMin = d.RTOMin
	}
	if c.RTOMax == 0 {
		c.RTOMax = d.RTOMax
	}
	if c.SynRTO == 0 {
		c.SynRTO = d.SynRTO
	}
	if c.Loopback == 0 {
		c.Loopback = d.Loopback
	}
	if c.UDPRecvBuffer == 0 {
		c.UDPRecvBuffer = d.UDPRecvBuffer
	}
	return c
}

// Validate applies defaults to zero fields and reports the first invalid field, or nil.
func (c Config) Validate() error {
	// The checks run in the order of NSM §7's message list: field order, except that SynRTO comes
	// before RTOMax because the RTOMax message needs both minimums valid.
	c = c.withDefaults()
	switch {
	case c.MSS < 64 || c.MSS > 65535:
		return fmt.Errorf("invalid netsim config: MSS %d out of range [64, 65535]", c.MSS)
	case c.SendBuffer < c.MSS:
		return fmt.Errorf("invalid netsim config: SendBuffer %d is smaller than MSS %d", c.SendBuffer, c.MSS)
	case c.RecvBuffer < c.MSS:
		return fmt.Errorf("invalid netsim config: RecvBuffer %d is smaller than MSS %d", c.RecvBuffer, c.MSS)
	case c.Backlog < 1:
		return fmt.Errorf("invalid netsim config: Backlog %d must be >= 1", c.Backlog)
	case c.ConnTimeout <= 0:
		return fmt.Errorf("invalid netsim config: ConnTimeout %v must be > 0", c.ConnTimeout)
	case c.RTOMin <= 0:
		return fmt.Errorf("invalid netsim config: RTOMin %v must be > 0", c.RTOMin)
	case c.SynRTO <= 0:
		return fmt.Errorf("invalid netsim config: SynRTO %v must be > 0", c.SynRTO)
	case c.RTOMax < c.RTOMin || c.RTOMax < c.SynRTO:
		return fmt.Errorf("invalid netsim config: RTOMax %v must be >= RTOMin and SynRTO", c.RTOMax)
	case c.Loopback < 0 || c.Loopback > simnet.MaxDelay:
		return fmt.Errorf("invalid netsim config: Loopback %v out of range [0s, 24h0m0s]", c.Loopback)
	case c.UDPRecvBuffer < MaxDatagram:
		return fmt.Errorf("invalid netsim config: UDPRecvBuffer %d is smaller than 65507", c.UDPRecvBuffer)
	}
	return nil
}
