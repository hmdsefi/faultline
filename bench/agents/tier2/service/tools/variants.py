#!/usr/bin/env python3
"""variants.py: writes the candidate variants trunc, sync, fwd and epoch of both versions from
their reference (testdata/<version>/reference), then patches/<version>-<name>.diff (diff -ru
reference <name>). Each variant is the reference plus the exact text replacements below; a
replacement that does not match exactly once stops the script. Run it after every change to a
reference, then rerun calibrate.sh.
"""
import os
import shutil
import subprocess
import sys

svc = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
variants = os.path.join(svc, 'testdata')


def edit(path, old, new, count=1):
    s = open(path).read()
    if s.count(old) != count:
        raise SystemExit(f'{path}: expected {count} of {old!r}, found {s.count(old)}')
    open(path, 'w').write(s.replace(old, new))


def variant(name, patches):
    ref = os.path.join(variants, version, 'reference')
    d = os.path.join(variants, version, name)
    if os.path.exists(d):
        shutil.rmtree(d)
    shutil.copytree(ref, d)
    for f, old, new in patches:
        edit(os.path.join(d, f), old, new)
    diff = subprocess.run(['diff', '-ru', 'reference', name], cwd=os.path.join(variants, version),
                          capture_output=True, text=True)
    if diff.returncode != 1:
        raise SystemExit(f'diff {version}/{name}: exit {diff.returncode} {diff.stderr}')
    with open(os.path.join(svc, 'patches', f'{version}-{name}.diff'), 'w') as f:
        f.write(diff.stdout)


# Each version's patches, written once with placeholders for the API spelling.
API = {
    'faultline': {
        'N': 'n *kernel.Node, ', 'n': 'n, ', 'n0': 'n', 'from': 'from kernel.NodeID, ', 'send': 's.W.Net.Send(n, ',
        'lsend': 'l.W.Net.Send(n, ', 'name': 'name', 'ret': 'vol.Open(walPath)', 'ind': '\t',
    },
    'plain': {
        'N': '', 'n': '', 'n0': '', 'from': 'from string, ', 'send': 's.Conn.Send(', 'lsend': 'l.Conn.Send(',
        'name': 'from', 'ret': 'nil', 'ind': '',
    },
}


def fill(text):
    for k, v in API[version].items():
        text = text.replace('{' + k + '}', v)
    return text


def patches(name, items):
    variant(name, [(f, fill(old), fill(new)) for f, old, new in items])


for version in ('faultline', 'plain'):
    patches('trunc', [
        ('store.go', '''	s.advance({n}commit)
	return {ret}''', '''	s.log = s.log[:min(commit, uint64(len(s.log)))]
	s.advance({n}commit)
	return {ret}'''),
    ])

    req_sig = ('func (s *Server) request(n *kernel.Node, from kernel.NodeID, client string, req any, fwd bool) {'
               if version == 'faultline' else 'func (s *Server) request(from, client string, req any, fwd bool) {')
    patches('fwd', [
        ('server.go', '''// request handles request req of client: from the client itself, or with fwd in a Fwd from the
// server from. A server that does not serve passes a client's own request on to the holder it
// knows and records it as the client's relay entry. It never passes on a request that came in a
// Fwd.
''' + req_sig + '''
	if !s.serving({n0}) {''', '''// request handles request req of client: from the client itself, or with fwd in a Fwd from the
// server from. It skips a request that matches the client's relay entry. A server that does not
// serve passes a client's own request on to the holder it knows and records it as the client's
// relay entry. It never passes on a request that came in a Fwd.
''' + req_sig + '''
	if r, ok := s.relay[client]; ok && r.seq == seq(req) {
		return
	}
	if !s.serving({n0}) {'''),
    ])

    patches('epoch', [
        ('lock.go', '''// Lock grants the leases, each with its own holder and expiry, and with epochs from one counter.
// The holder renews its lease. Another node gets a lease only after it expired on the lock's
// clock, and then with the next epoch. An expired lease that nobody took since goes back to its
// last holder with the same epoch: nobody else held it in between, so nothing was written under
// another epoch, and what the holder loaded for that epoch is still current.''',
         '''// Lock grants the leases, each with its own holder and expiry, and with epochs from one counter.
// The holder renews its lease. Another node gets a lease only after it expired on the lock's
// clock. A node gets the next epoch with its first grant of a lease, and that epoch again with
// every later grant of the lease.'''),
        ('lock.go', '''type lease struct {
	holder string
	epoch  uint64
	expiry time.Time
}''', '''type lease struct {
	holder string
	expiry time.Time
	epochs map[string]uint64 // per node: the epoch of its grants
}'''),
        ('lock.go', '''ls = &lease{}''', '''ls = &lease{epochs: map[string]uint64{}}'''),
        ('lock.go', '''{ind}	if !held && ls.holder != {name} {
{ind}		l.epoch++
{ind}		ls.holder, ls.epoch = {name}, l.epoch
{ind}	}
{ind}	ls.expiry = now.Add(l.TTL)
{ind}	{lsend}from, Grant{Epoch: ls.epoch, TTL: l.TTL, Sent: req.Sent})''',
         '''{ind}	if !held {
{ind}		ls.holder = {name}
{ind}	}
{ind}	if ls.epochs[{name}] == 0 {
{ind}		l.epoch++
{ind}		ls.epochs[{name}] = l.epoch
{ind}	}
{ind}	ls.expiry = now.Add(l.TTL)
{ind}	{lsend}from, Grant{Epoch: ls.epochs[{name}], TTL: l.TTL, Sent: req.Sent})'''),
    ])

    patches('sync', [
        ('store.go', '''	case Sync:
		s.request({n}from, Entry{KV: m.Epoch})''', '''	case Sync:
		s.sync({n}from, m)'''),
        ('store.go', '''// request appends entry e, if the replica is the primary, and sends it to the backup.''',
         '''// sync answers a Sync with the applied data. When the Sync raises the fence, the primary first
// appends the fence entry, syncs it and sends it to the backup.
func (s *Store) sync({N}{from}m Sync) {
	switch {
	case !s.leading({n0}):
		{send}from, NotPrimary{Hint: s.hint({n0})})
		return
	case m.Epoch < s.fence:
		{send}from, Fenced{Epoch: m.Epoch})
		return
	case m.Epoch > s.fence:
		e := Entry{Epoch: s.epoch, KV: m.Epoch}
		if err := s.persist(record{Index: uint64(len(s.log)) + 1, Entry: e}); err != nil {
			return
		}
		s.log = append(s.log, e)
		s.replicate({n0})
	}
	{send}from, SyncOK{Epoch: m.Epoch, Data: clone(s.data)})
}

// request appends entry e, if the replica is the primary, and sends it to the backup.'''),
        ('store.go', '''		switch {
		case fenced:
			{send}to, Fenced{Epoch: e.KV, Client: e.Client, Seq: e.Seq})
		case e.Key == "":
			{send}to, SyncOK{Epoch: e.KV, Data: clone(s.data)})
		default:
			// A write sent again is answered with the key's current list, which holds the value.
			{send}to, Written{Client: e.Client, Seq: e.Seq, Key: e.Key, List: slices.Clone(s.data[e.Key])})
		}''', '''		if fenced {
			{send}to, Fenced{Epoch: e.KV, Client: e.Client, Seq: e.Seq})
			continue
		}
		// A write sent again is answered with the key's current list, which holds the value.
		{send}to, Written{Client: e.Client, Seq: e.Seq, Key: e.Key, List: slices.Clone(s.data[e.Key])})'''),
    ])
print('variants and patches written')
