// Package repl copies every write from a primary to its replicas over TCP.
package repl

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"sync"
)

// Replica holds a copy of the primary's writes.
type Replica struct {
	ln   net.Listener
	mu   sync.Mutex
	data map[string]string
}

// StartReplica listens on a free local port and applies every "key=value" line it receives.
func StartReplica() (*Replica, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	r := &Replica{ln: ln, data: make(map[string]string)}
	go r.serve()
	return r, nil
}

// Addr is the address the primary sends writes to.
func (r *Replica) Addr() string { return r.ln.Addr().String() }

// Close stops accepting connections.
func (r *Replica) Close() error { return r.ln.Close() }

// Get returns the replica's value for key.
func (r *Replica) Get(key string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data[key]
}

func (r *Replica) serve() {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return
		}
		go r.apply(conn)
	}
}

func (r *Replica) apply(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		if key, value, ok := strings.Cut(sc.Text(), "="); ok {
			r.mu.Lock()
			r.data[key] = value
			r.mu.Unlock()
		}
	}
}

// Primary copies each write to every replica.
type Primary struct {
	conns []net.Conn
}

// StartPrimary connects to the replicas at addrs.
func StartPrimary(addrs ...string) (*Primary, error) {
	p := &Primary{}
	for _, a := range addrs {
		c, err := net.Dial("tcp", a)
		if err != nil {
			return nil, err
		}
		p.conns = append(p.conns, c)
	}
	return p, nil
}

// Put sends the write to every replica in the background.
func (p *Primary) Put(key, value string) {
	for _, c := range p.conns {
		go fmt.Fprintf(c, "%s=%s\n", key, value)
	}
}
