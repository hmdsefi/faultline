package leasekv

import "time"

// Conn sends messages for one node. A node gets a new Conn each time it starts.
type Conn interface {
	Send(to string, msg any)
}

// The leases of the lock: the servers take "kv", the store replicas take "store".
const (
	leaseKV    = "kv"
	leaseStore = "store"
)

// The messages between the nodes.
type (
	// Acquire asks the lock for the lease named Lease, or renews it for the holder. Sent is the
	// sender's clock when it sent the request.
	Acquire struct {
		Lease string
		Sent  time.Time
	}
	// Grant gives the node the lease it asked for: epoch Epoch, valid for TTL from the Acquire's
	// Sent.
	Grant struct {
		Epoch uint64
		TTL   time.Duration
		Sent  time.Time
	}
	// Busy tells a node that Holder has the lease it asked for.
	Busy struct{ Holder string }

	// Sync asks the store for all its data. It raises the store's fence to the kv epoch Epoch.
	Sync struct{ Epoch uint64 }
	// SyncOK answers the Sync of Epoch with the store's data.
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
	// Fenced rejects a request of kv epoch Epoch, which is below the store's fence. Client and Seq
	// name the write it rejects; Client is empty for a Sync.
	Fenced struct {
		Epoch  uint64
		Client string
		Seq    uint64
	}
	// NotPrimary tells a server that the replica is not the primary. Hint names the primary the
	// replica knows, or is empty.
	NotPrimary struct{ Hint string }

	// Replicate carries the primary's log from index Prev+1 on to the backup. PrevEpoch is the
	// store epoch of entry Prev. Commit is the primary's commit index.
	Replicate struct {
		Epoch, Prev, PrevEpoch uint64
		Entries                []Entry
		Commit                 uint64
	}
	// Appended tells the primary of Epoch that the backup's log matches its log up to Index.
	Appended struct{ Epoch, Index uint64 }
	// Mismatch tells the primary of Epoch that the backup's log has no entry Prev of the
	// Replicate's PrevEpoch. Len is the length of the backup's log.
	Mismatch struct{ Epoch, Prev, Len uint64 }
	// Stale rejects a Replicate: the replica knows the newer store epoch Epoch.
	Stale struct{ Epoch uint64 }

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
	// Fwd passes the request Req (an Append or a Get) of Client to the server that holds the kv
	// lease.
	Fwd struct {
		Client string
		Req    any
	}
	// FwdReply carries the answer Reply (an AppendOK, GetOK or NotLeader) to a Fwd of Client.
	FwdReply struct {
		Client string
		Reply  any
	}
)

// Entry is one entry of a replica's log. A write entry appends Value to Key for request Seq of
// Client. An entry with an empty Key raises the fence to KV. Epoch is the store epoch of the
// primary that appended it, KV the kv epoch of the request.
type Entry struct {
	Epoch, KV  uint64
	Client     string
	Seq        uint64
	Key, Value string
}

// seq returns the Seq of a client request or of an answer to one.
func seq(msg any) uint64 {
	switch m := msg.(type) {
	case Append:
		return m.Seq
	case Get:
		return m.Seq
	case AppendOK:
		return m.Seq
	case GetOK:
		return m.Seq
	case NotLeader:
		return m.Seq
	}
	return 0
}
