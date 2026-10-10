// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package simnet

import (
	"errors"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/hmdsefi/faultline/kernel"
)

// MaxDelay is the largest value accepted for Link.Latency, Link.Jitter, Link.Tail and
// RawOptions.Delay.
const MaxDelay = 24 * time.Hour

// MaxPPM is a probability of 1 in parts per million.
const MaxPPM uint32 = 1_000_000

// Link configures one directed link. The zero value is a perfect link: zero latency, no jitter,
// no loss, no duplication, no FIFO guarantee.
type Link struct {
	// Latency is the fixed part of the one-way delay. 0 ≤ Latency ≤ MaxDelay.
	Latency time.Duration
	// Jitter is the maximum uniform extra delay: each copy adds kernel.Uniform(r, 0, Jitter).
	// 0 ≤ Jitter ≤ MaxDelay. Reordering emerges from jitter unless FIFO is set.
	Jitter time.Duration
	// TailPPM is the probability, per copy, that the copy also gets a tail delay.
	// 0 ≤ TailPPM ≤ MaxPPM.
	TailPPM uint32
	// Tail is the maximum tail delay: a tail copy adds kernel.Uniform(r, 0, Tail).
	// 0 ≤ Tail ≤ MaxDelay.
	Tail time.Duration
	// DropPPM is the probability that a message is lost at send time. 0 ≤ DropPPM ≤ MaxPPM.
	DropPPM uint32
	// DupPPM is the probability that a message that was not lost is delivered twice.
	// The duplicate gets an independent delay. 0 ≤ DupPPM ≤ MaxPPM.
	DupPPM uint32
	// FIFO makes every copy on this link arrive at least 1 ns after the previous copy scheduled
	// on this link.
	FIFO bool
}

// Validate reports the first invalid field of l, checked in the order Latency, Jitter, Tail,
// TailPPM, DropPPM, DupPPM, or nil if l is valid. Error messages are listed in NET §7.
func (l Link) Validate() error {
	if err := checkDelay("Latency", l.Latency); err != nil {
		return err
	}
	if err := checkDelay("Jitter", l.Jitter); err != nil {
		return err
	}
	if err := checkDelay("Tail", l.Tail); err != nil {
		return err
	}
	if err := checkPPM("TailPPM", l.TailPPM); err != nil {
		return err
	}
	if err := checkPPM("DropPPM", l.DropPPM); err != nil {
		return err
	}
	return checkPPM("DupPPM", l.DupPPM)
}

func checkDelay(field string, d time.Duration) error {
	if d < 0 || d > MaxDelay {
		return errors.New("invalid link: " + field + " " + d.String() + " out of range [0s, " + MaxDelay.String() + "]")
	}
	return nil
}

func checkPPM(field string, v uint32) error {
	if v > MaxPPM {
		return errors.New("invalid link: " + field + " " + strconv.FormatUint(uint64(v), 10) + " exceeds 1000000")
	}
	return nil
}

// Decision is the outcome of the random draws for one message on one link (see Network.Decide).
type Decision struct {
	Drop     bool          // the message is lost
	Delay    time.Duration // delay of copy 1, before the FIFO rule; 0 if Drop
	Dup      bool          // a duplicate is created; false if Drop
	DupDelay time.Duration // delay of copy 2, before the FIFO rule; 0 unless Dup
}

// linkState is the state of one directed link.
type linkState struct {
	override *Link       // nil: Config.Default applies
	last     kernel.Time // last delivery time scheduled on the link (NET-014)
	hasLast  bool
	rng      *rand.Rand // the link's stream; nil until first needed (NET §6.1)
	stats    Stats
}

// linkState returns the state of link i -> j (indices), creating it on first use.
func (nw *Network) linkState(i, j int) *linkState {
	ls := nw.links[i][j]
	if ls == nil {
		ls = &linkState{}
		nw.links[i][j] = ls
	}
	return ls
}

// effective returns the effective configuration of link i -> j (NET-005).
func (nw *Network) effective(i, j int) Link {
	if ls := nw.links[i][j]; ls != nil && ls.override != nil {
		return *ls.override
	}
	return nw.cfg.Default
}

// stream returns the stream of link i -> j: "net/link/<from name>/<to name>" (NET §6.1).
func (nw *Network) stream(i, j int) *rand.Rand {
	ls := nw.linkState(i, j)
	if ls.rng == nil {
		ls.rng = nw.s.Rand("net/link/" + nw.nodes[i].name + "/" + nw.nodes[j].name)
	}
	return ls.rng
}

// chance decides with probability ppm without drawing at the edges (NET-013).
func chance(r *rand.Rand, ppm uint32) bool {
	if ppm == 0 {
		return false
	}
	if ppm >= MaxPPM {
		return true
	}
	return kernel.Chance(r, ppm)
}

// uniform returns a duration in [0, max] without drawing when max is 0 (NET-013).
func uniform(r *rand.Rand, max time.Duration) time.Duration {
	if max == 0 {
		return 0
	}
	return kernel.Uniform(r, 0, max)
}

// delay draws the delay of one copy (NET-013).
func delay(l Link, r *rand.Rand) time.Duration {
	x := l.Latency
	x += uniform(r, l.Jitter)
	if chance(r, l.TailPPM) {
		x += uniform(r, l.Tail)
	}
	return x
}

// decide draws the decisions for one message (NET-013).
func decide(l Link, r *rand.Rand) Decision {
	if chance(r, l.DropPPM) {
		return Decision{Drop: true}
	}
	d1 := delay(l, r)
	if chance(r, l.DupPPM) {
		return Decision{Delay: d1, Dup: true, DupDelay: delay(l, r)}
	}
	return Decision{Delay: d1}
}
