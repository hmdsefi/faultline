package leasekv

import (
	"strconv"
	"time"
)

// The messages between the nodes.
type (
	// Acquire asks the lock for the lease, or renews it for the holder. Sent is the server's
	// clock when it sent the request.
	Acquire struct{ Sent time.Time }
	// Grant gives the lease to the server that sent the Acquire: epoch Epoch, valid for TTL from
	// the Acquire's Sent.
	Grant struct {
		Epoch uint64
		TTL   time.Duration
		Sent  time.Time
	}
	// Busy tells a server that Holder has the lease.
	Busy struct{ Holder string }
	// Sync asks the store for all its data. It raises the store's fence to Epoch.
	Sync struct{ Epoch uint64 }
	// SyncOK carries the store's data at the time it handled the Sync of Epoch.
	SyncOK struct {
		Epoch uint64
		Data  map[string][]string
	}
	// Write asks the store to append Value to Key for request Seq of Client.
	Write struct {
		Epoch      uint64
		Client     string
		Seq        uint64
		Key, Value string
	}
	// Written confirms request Seq of Client. List is the key's list after the write.
	Written struct {
		Client string
		Seq    uint64
		Key    string
		List   []string
	}
	// Fenced rejects a request of Epoch, which is below the store's fence.
	Fenced struct{ Epoch, Fence uint64 }
	// Append asks a server to append Value to Key.
	Append struct {
		Seq        uint64
		Key, Value string
	}
	// Get asks a server for the list of Key.
	Get struct {
		Seq uint64
		Key string
	}
	// AppendOK confirms the Append Seq.
	AppendOK struct{ Seq uint64 }
	// GetOK answers the Get Seq.
	GetOK struct {
		Seq  uint64
		List []string
	}
	// NotLeader tells a client that the server does not serve requests now. Hint names the
	// server that the lock named last as the holder, or is empty.
	NotLeader struct {
		Seq  uint64
		Hint string
	}
)

func (m Acquire) Describe() string   { return "acquire" }
func (m Grant) Describe() string     { return "grant epoch=" + u(m.Epoch) + " ttl=" + m.TTL.String() }
func (m Busy) Describe() string      { return "busy holder=" + m.Holder }
func (m Sync) Describe() string      { return "sync epoch=" + u(m.Epoch) }
func (m SyncOK) Describe() string    { return "sync ok epoch=" + u(m.Epoch) }
func (m Fenced) Describe() string    { return "fenced epoch=" + u(m.Epoch) + " fence=" + u(m.Fence) }
func (m Get) Describe() string       { return "get #" + u(m.Seq) + " " + m.Key }
func (m AppendOK) Describe() string  { return "append ok #" + u(m.Seq) }
func (m GetOK) Describe() string     { return "get ok #" + u(m.Seq) + " len=" + strconv.Itoa(len(m.List)) }
func (m NotLeader) Describe() string { return "not leader #" + u(m.Seq) + " hint=" + m.Hint }
func (m Append) Describe() string    { return "append #" + u(m.Seq) + " " + m.Key + "+=" + m.Value }
func (m Write) Describe() string {
	return "write epoch=" + u(m.Epoch) + " " + m.Client + "#" + u(m.Seq) + " " + m.Key + "+=" + m.Value
}
func (m Written) Describe() string {
	return "written " + m.Client + "#" + u(m.Seq) + " " + m.Key + " len=" + strconv.Itoa(len(m.List))
}

func u(v uint64) string { return strconv.FormatUint(v, 10) }
