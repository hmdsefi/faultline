#!/usr/bin/env python3
"""trigger.py <trunc|sync|fwd|epoch> <trace.jsonl>...: finds the designed trigger of a candidate
in the traces of failing faultline seeds. Each candidate's function below describes its trigger.

For each trace it prints one line: the seed, "match" or "no", and for a match the steps of the
trigger with their trace record numbers (seq), in order. Exit 0. Used by calibrate.sh.
"""
import json
import re
import sys


class Trace:
    def __init__(self, path):
        self.path = path
        self.sends = {}  # msg id -> (seq, t, from, to, payload)
        self.events = []  # (seq, t, kind, data)
        self.failure = ''
        self.msgs = {}  # record seq of a send, delivery or drop -> msg id
        with open(path) as f:
            for i, line in enumerate(f):
                r = json.loads(line)
                if i == 0:
                    self.seed = r.get('seed', '?')
                    continue
                kind, a = r['kind'], dict(r.get('attrs', []))
                seq, t = r['seq'], r['at']
                if kind == 'net.send':
                    self.sends[a['msg']] = (seq, t, a['from'], a['to'], a.get('payload', ''))
                    self.events.append((seq, t, 'send', self.sends[a['msg']]))
                    self.msgs[seq] = a['msg']
                elif kind in ('net.deliver', 'net.drop'):
                    s = self.sends.get(a['msg'])
                    if s:
                        self.events.append((seq, t, kind[4:], (seq, t, s[2], s[3], s[4])))
                        self.msgs[seq] = a['msg']
                elif kind == 'fault.crash':
                    self.events.append((seq, t, 'crash', r['text'].split()[-1]))
                elif kind == 'fault.restart':
                    self.events.append((seq, t, 'restart', r['text'].split()[-1]))
                elif kind == 'fault.partition':
                    groups = [g.split(',') for g in r['text'].split(' ', 1)[1].split('|')]
                    self.events.append((seq, t, 'partition', groups))
                elif kind == 'fault.heal':
                    self.events.append((seq, t, 'heal', None))
                elif kind == 'check.violation' and not self.failure:
                    self.failure = r.get('text', '') + ': ' + a.get('error', '')

    def delivered(self, to=None, frm=None, pat=None, after=-1, before=float('inf'), kind='deliver'):
        rx = re.compile(pat) if pat else None
        for seq, t, k, d in self.events:
            if k != kind or t <= after or t >= before:
                continue
            _, _, f, o, p = d
            if (to is None or o == to) and (frm is None or f == frm) and (rx is None or rx.search(p)):
                yield seq, t, f, o, p

    def sent(self, frm=None, to=None, pat=None, after=-1, before=float('inf')):
        rx = re.compile(pat) if pat else None
        for seq, t, k, d in self.events:
            if k != 'send' or t <= after or t >= before:
                continue
            _, _, f, o, p = d
            if (frm is None or f == frm) and (to is None or o == to) and (rx is None or rx.search(p)):
                yield seq, t, f, o, p

    def faults(self, kind):
        return [(seq, t, d) for seq, t, k, d in self.events if k == kind]

    def partitions(self):
        """(seq, start, groups, heal seq, heal time) for every partition."""
        out, cur = [], None
        for seq, t, k, d in self.events:
            if k == 'partition':
                cur = (seq, t, d)
            elif k == 'heal' and cur:
                out.append((cur[0], cur[1], cur[2], seq, t))
                cur = None
        if cur:
            out.append((cur[0], cur[1], cur[2], None, float('inf')))
        return out


STORES, SERVERS = ('st1', 'st2'), ('s1', 's2')


def side(groups, node):
    for i, g in enumerate(groups):
        if node in g:
            return i
    return -1


def ts(t):
    return '%.6fs' % (t / 1e9)


def num(p, key):
    m = re.search(r'\b' + key + r'=(\d+)', p)
    return int(m.group(1)) if m else None


def trunc(tr):
    """S1: backup B acks entry i and crashes, still a backup, before it learns commit >= i; it
    restarts while a partition cuts the primary P off from B and the lock, takes the store lease,
    and after the heal P gets entries from B that replace its entries from prev+1 <= i on."""
    for b, p in (('st1', 'st2'), ('st2', 'st1')):
        restarts = tr.faults('restart')
        for cseq, tc, node in tr.faults('crash'):
            if node != b:
                continue
            rs = [(s, t) for s, t, n in restarts if n == b and t > tc]
            if not rs:
                continue
            rseq, tr_ = rs[0]
            acks = [(s, t, num(pl, 'index')) for s, t, f, o, pl in tr.sent(frm=b, to=p, pat=r'^appended', before=tc)]
            if not acks:
                continue
            aseq, ta, i = max(acks, key=lambda x: x[2])
            if list(tr.delivered(to=b, pat=r'^grant ', after=ta, before=tc)):
                continue  # B was no longer the backup when it crashed
            learned = [num(pl, 'commit') for _, _, _, _, pl in tr.delivered(to=b, frm=p, pat=r'^replicate', before=tc)]
            if not learned or max(learned) >= i:
                continue
            committed = [s for s, t, f, o, pl in tr.sent(frm=p, pat=r'^replicate') if num(pl, 'commit') >= i]
            if not committed:
                continue
            for pseq, tp, groups, hseq, th in tr.partitions():
                if not (tp <= tr_ < th):
                    continue
                if side(groups, p) == side(groups, b) or side(groups, b) != side(groups, 'lock'):
                    continue
                grants = list(tr.delivered(to=b, pat=r'^grant ', after=tr_, before=th))
                if not grants:
                    continue
                gseq, tg, _, _, gp = grants[0]
                epoch = num(gp, 'epoch')
                for dseq, td, _, _, dp in tr.delivered(to=p, frm=b, pat=r'^replicate', after=th):
                    m = re.match(r'replicate epoch=(\d+) prev=(\d+)/', dp)
                    if m and int(m.group(1)) >= epoch and int(m.group(2)) < i and '[' in dp:
                        return ('%s acks entry %d (seq %d), learned commit %d; crash %s (seq %d); restart (seq %d) '
                                'during partition (seq %d); grant store epoch %d (seq %d); heal (seq %d); '
                                '%s gets replicate prev=%s replacing entries from %d (seq %d)') % (
                            b, i, aseq, max(learned), b, cseq, rseq, pseq, epoch, gseq, hseq, p,
                            m.group(2), int(m.group(2)) + 1, dseq)
    return None


def sync(tr):
    """S2: a partition cuts the store primary P and the kv holder H off from the lock; the other
    replica B takes the store lease; the other server H2 takes the kv lease and gets a SyncOK from B
    that holds a shorter list for a key than the last Written for it."""
    for pseq, tp, groups, hseq, th in tr.partitions():
        lock = side(groups, 'lock')
        sg = [(s, t, o, pl) for s, t, f, o, pl in tr.delivered(pat=r'^grant ', before=tp) if o in STORES]
        kg = [(s, t, o, pl) for s, t, f, o, pl in tr.delivered(pat=r'^grant ', before=tp) if o in SERVERS]
        if not sg or not kg:
            continue
        primary, holder = sg[-1][2], kg[-1][2]
        backup = 'st2' if primary == 'st1' else 'st1'
        other = 's2' if holder == 's1' else 's1'
        if side(groups, primary) == lock or side(groups, holder) == lock:
            continue
        if side(groups, backup) != lock or side(groups, other) != lock:
            continue
        sgrant = list(tr.delivered(to=backup, pat=r'^grant ', after=tp, before=th))
        kgrant = list(tr.delivered(to=other, pat=r'^grant ', after=tp, before=th))
        if not sgrant or not kgrant:
            continue
        for oseq, to, f, o, pl in tr.delivered(to=other, frm=backup, pat=r'^sync ok', after=tp, before=th):
            for key in ('k1', 'k2'):
                n = num(pl, key + r'\.len')
                if n is None:
                    n = 0
                last = [(s, num(wp, 'len')) for s, t, wf, wo, wp in tr.sent(pat=r'^written \S+ ' + key + ' ', before=to)]
                if last and last[-1][1] > n:
                    return ('partition (seq %d) cuts %s and %s off from the lock; %s gets the store lease (seq %d); '
                            '%s gets the kv lease (seq %d); sync ok from %s (seq %d) holds %s.len=%d, the last '
                            'written for %s had len=%d (seq %d)') % (
                        pseq, primary, holder, backup, sgrant[0][0], other, kgrant[0][0], backup, oseq,
                        key, n, key, last[-1][1], last[-1][0])
    return None


def fwd(tr):
    """F1: the stuck op of the client named in the failure was passed on in a Fwd by server F to
    holder H. The Fwd or H's FwdReply was dropped, or H crashed before it replied. After that loss F
    got its first grant of a new kv epoch, then repeats of the op, and answered none of them."""
    m = re.search(r'(c\d) completed \d+ of (\d+) ops', tr.failure)
    if not m:
        return None
    client, k = m.group(1), int(m.group(2))
    fwdpat = r'^fwd %s (append|get) #%d ' % (client, k)
    replypat = r'^fwd reply %s .*#%d\b' % (client, k)
    for fseq, tf, f, h, pl in tr.sent(pat=fwdpat):
        if f not in SERVERS or list(tr.delivered(to=f, frm=h, pat=replypat, after=tf)):
            continue
        losses = []
        for dseq, td, _, _, _ in tr.delivered(frm=f, to=h, pat=fwdpat, after=tf - 1, kind='drop'):
            if tr.msgs.get(dseq) == tr.msgs.get(fseq):
                losses.append(('the Fwd dropped', dseq, td))
        for dseq, td, _, _, _ in tr.delivered(frm=h, to=f, pat=replypat, after=tf, kind='drop'):
            losses.append(('its FwdReply dropped', dseq, td))
        for cseq, tc, node in tr.faults('crash'):
            if node == h and tc > tf and not list(tr.sent(frm=h, to=f, pat=replypat, after=tf, before=tc)):
                losses.append(('crash %s' % h, cseq, tc))
        if not losses:
            continue
        what, lseq, tl = min(losses, key=lambda x: x[2])
        grants = [(s, t, num(p, 'epoch')) for s, t, _, _, p in tr.delivered(to=f, pat=r'^grant ')]
        new = [(s, t, e) for i, (s, t, e) in enumerate(grants)
               if t > tl and e > max([x[2] for x in grants[:i]], default=0)]
        if not new:
            continue
        gseq, tg, epoch = new[0]
        repeats = [s for s, t, fr, o, p in tr.delivered(to=f, after=tg)
                   if (fr == client and re.match(r'^(append|get) #%d ' % k, p)) or re.match(fwdpat, p)]
        if not repeats:
            continue
        answered = [s for s, t, fr, o, p in tr.sent(frm=f, after=tg)
                    if (o == client and re.search(r'#%d\b' % k, p)) or re.match(replypat, p)]
        if answered:
            continue
        return ('%s passes %s #%d to %s (seq %d); %s (seq %d); %s gets kv epoch %d (seq %d); %d repeats of '
                '#%d reach %s from seq %d on, none answered') % (
            f, client, k, h, fseq, what, lseq, f, epoch, gseq, len(repeats), k, f, repeats[0])
    return None


def epoch(tr):
    """L1: server A gets kv epoch E; server B then gets a higher epoch and acks an append; A, which
    did not crash and sent no store request since B's grant, gets epoch E again and answers a get
    before anyone gets a newer kv grant."""
    grants = [(s, t, o, num(p, 'epoch')) for s, t, f, o, p in tr.delivered(pat=r'^grant ') if o in SERVERS]
    crashes = tr.faults('crash')
    for i, (s1, t1, a, e) in enumerate(grants):
        for s2, t2, b, e2 in grants[i + 1:]:
            if b == a or e2 <= e:
                continue
            acks = list(tr.sent(frm=b, pat=r'(^append ok|^fwd reply \S+ append ok)', after=t2))
            back = [(s, t) for s, t, o, ep in grants if o == a and ep == e and t > t2]
            if not acks or not back:
                break
            s3, t3 = back[0]
            if acks[0][1] > t3 or any(n == a and t1 < t <= t3 for _, t, n in crashes):
                break
            if list(tr.sent(frm=a, pat=r'^(write|sync) ', after=t2, before=t3)):
                break
            newer = [t for s, t, o, ep in grants if t > t3 and ep > e]
            gets = [x for x in tr.sent(frm=a, pat=r'(^get ok|^fwd reply \S+ get ok)', after=t3,
                                       before=newer[0] if newer else float('inf'))]
            if not gets:
                break
            return ('%s gets kv epoch %d (seq %d); %s gets epoch %d (seq %d) and acks an append (seq %d); '
                    '%s gets epoch %d again (seq %d) and answers a get (seq %d)') % (
                a, e, s1, b, e2, s2, acks[0][0], a, e, s3, gets[0][0])
    return None


def main():
    if len(sys.argv) < 3 or sys.argv[1] not in ('trunc', 'sync', 'fwd', 'epoch'):
        sys.exit(__doc__)
    find = globals()[sys.argv[1]]
    for path in sys.argv[2:]:
        tr = Trace(path)
        found = find(tr)
        print('%s %s%s' % (tr.seed, 'match' if found else 'no', ': ' + found if found else ''))


if __name__ == '__main__':
    main()
