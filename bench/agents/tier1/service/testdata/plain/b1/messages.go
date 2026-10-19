package leasekv

import "time"

// Conn sends messages for one node. A node gets a new Conn each time it starts.
type Conn interface {
	Send(to string, msg any)
}

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
