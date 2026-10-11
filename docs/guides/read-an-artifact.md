# Read a failure artifact

Every failing seed leaves an artifact directory with the files you need to debug it. This guide
shows where to find the directory and which file to open first. Then it follows a failure back to
its cause, in the text timeline and in the timeline page.

The examples use the failing seed `0x287372ab06f1482e` of `TestKV` from [Get
started](../getting-started.md).

## Find the artifact directory

The failure report ends with the directory's path:

```text
artifacts: /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
```

The path is the artifact root, then the package import path with `/` written as `_`, the test name,
and the seed in hex. The default root is a folder named `faultline-` plus your user ID, under your
system's temporary directory. `FAULTLINE_ARTIFACTS=<dir>` sets another root, and
`FAULTLINE_ARTIFACTS=off` turns artifacts off. [Artifact files](../reference/artifacts.md) has the
exact naming rules.

The paths on this page are examples, from a machine with user ID 501 and the temporary directory
`/tmp`. In the commands, use the `artifacts:` path from your own output.

faultline writes the directory from a second run of the failing seed, which keeps every record. A
seed that stopped through `t.Fatal`, `t.FailNow` or `t.SkipNow` gets no artifact. Passing seeds get
one only under `FAULTLINE_TRACE=full`.

The directory of the example holds eight files:

```sh
ls -1 /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
```

```text
hb.mmd
history.jsonl
report.json
report.txt
schedule.json
timeline.html
timeline.txt
trace.jsonl
```

Each file answers one question:

- `report.txt`: what failed, when, and how do you replay it?
- `timeline.txt`: what led up to the failure? This file is the one you read most.
- `timeline.html`: the same run as an interactive page.
- `schedule.json`: which faults ran, and when?
- `history.jsonl`: what did each client ask for, and what did it get back? It exists when the test
  records operations with `w.History`.
- `trace.jsonl`: every record of the run, one JSON object per line, for `grep` and `jq`.
- `hb.mmd`: the chain of records that caused the failure, as a Mermaid flowchart.
- `report.json`: the report as data for scripts, with the replay command, the versions, the options
  and the trace hash.

## Start with report.txt

`report.txt` holds the failure exactly as `go test` printed it. When the failure is a panic, it also
holds the full stack, where the console shows at most 40 lines of it.

```text
--- FAIL: TestKV/seed=0x287372ab06f1482e
    faultline: invariant "acked puts survive" violated at t=5.873034010s (event 1455)
      k169=v169 was acknowledged, but n1 holds ""
    replay:    FAULTLINE_SEED=0x287372ab06f1482e go test -v -run '^TestKV$' example.com/kv
    artifacts: /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/
```

The first line names the failed check, the virtual time and the event number. The message comes from
the check. A check that names the key and the node, as this one does, tells you where to look next.

## Walk back through timeline.txt

`timeline.txt` starts with a header that sums up the run:

```text
faultline timeline v1
test:     TestKV/seed=0x287372ab06f1482e
package:  example.com/kv
seed:     0x287372ab06f1482e
status:   fail
failure:  invariant "acked puts survive" violated at t=5.873034010s (event 1455)
records:  3631 (dropped 0), trace hash 0x34d831a670e30100
nodes:    1=n1 [server], 2=c1 [client]
slice:    200 records, root 3629, truncated at cap 200
legend:   ! failure record, * causal slice, <- cause
```

After the header, every record of the run has one line with these columns:

- the mark: `!` for the failure record, `*` for a record in the causal slice;
- `SEQ`, the record's number in the run;
- `TIME`, the virtual time;
- `NODE`, the node and its incarnation, such as `n1#6` for the server's sixth boot, or `global`;
- `KIND`, such as `net.send`, `disk.sync` or `fault.crash`;
- `TEXT`, the record's text and attributes, then `<-` and the number of the record that caused it.

The *causal slice* is the set of records that led to the failure. faultline builds it backward from
the failure record. It follows each record's cause, and the previous record of the same incarnation,
until it has 200 records. `slice:` in the header says when the run had more.

To find the cause, search for the `!` line and read upward through the `*` lines. A search for the
key from the message finds the records that name that key:

```sh
grep -n 'k169' /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/timeline.txt
```

```text
3595:* 3583 5.546690866s c1#1   history.invoke     c1 invoke put "k169=v169" id=169 process=c1 f=put input="\"k169=v169\"" <-3582
3596:* 3584 5.546690866s c1#1   net.send           send #362 c1 -> n1: put k169=v169 msg=362 from=c1 to=n1 payload="put k169=v169" <-3583
3602:* 3590 5.551630705s n1#6   net.send           send #363 n1 -> c1: ack k169 msg=363 from=n1 to=c1 payload="ack k169" <-3589
3641:! 3629 5.873034010s global check.violation    invariant "acked puts survive" violated kind=invariant check="acked puts survive" error="k169=v169 was acknowledged, but n1 holds \"\"" event=1455 <-3628
3642:  3630 5.873034010s global kernel.fail        invariant "acked puts survive": k169=v169 was acknowledged, but n1 holds "" <-3629
```

The server sent the acknowledgment for `k169` at 5.551630705s, in record 3590. Its cause, record
3589, is the append to `/wal`. The lines between record 3590 and the failure show the crash 0.3 ms
later, which kept none of the unsynced writes. [Get started](../getting-started.md#find-the-cause)
walks through all of them.

## Open timeline.html

`timeline.html` shows the same run as a page you open in a browser. It is one self-contained file
that loads nothing from the network. It works offline, and it can be kept with the other files of a
CI run. It embeds up to 50,000 records.

![The timeline page of seed 0x287372ab06f1482e: the failure headline and the replay command at the top, lanes for global, n1 and c1 with six hatched crash bands on n1 and message lines between n1 and c1, a failure marker at 5.873034010s, and side panels listing the failed check and the six crashes](../../.github/assets/timeline.png)

The page has these parts:

- The header names the failure and the run's size, and shows the replay command with a **Copy**
  button.
- The timeline has one lane per node and one for global records. A hatched band marks a node that is
  down, a line joins each message's send and delivery, and a dashed red line marks the failure.
- **Time** and **Sequence** switch the horizontal axis between virtual time and record order.
  **Causal slice**, on by default, dims the records outside the causal slice.
- **Failure** repeats the failed check, its time, its event and its record. **Faults** lists each
  fault with the window in which it was active.
- **Inspector** shows the fields of the record you click, its cause and its effects.
- The **Kinds** row turns record kinds on and off. Scheduler events start hidden.

With the timeline focused, `f` jumps to the failure, `+` and `-` zoom, and `0` fits the whole run.
`t` switches between the light and the dark theme.

## See the causal slice as a graph

`hb.mmd` holds the causal slice as a Mermaid flowchart, with one box per record. Arrows follow each
record's cause and each node's order of records. Faults, messages and the failure have their own
colors. Paste the file into any Mermaid renderer to see the chain as a graph. faultline keeps the
file under 50,000 bytes, the default size limit of mermaid.js. When the slice does not fit, the file
holds a smaller slice: the records closest to the failure.

## Use report.json from scripts

`report.json` holds everything a script needs about the failure. For example, this command prints
the failure's signature, its virtual time and event, and the trace hash:

```sh
jq '{signature: .failure.signature, at: .failure.at, event: .failure.event, trace_hash: .run.trace_hash}' report.json
```

```json
{
  "signature": "invariant:acked puts survive",
  "at": "5.873034010s",
  "event": 1455,
  "trace_hash": "0x34d831a670e30100"
}
```

The *signature* is the failure's kind and the check's name. Seeds that fail the same check share a
signature, so a script can group failing seeds by it.

The other data files work the same way. `trace.jsonl` starts with a header line, then holds one
record per line:

```sh
jq -c 'select(.kind == "fault.crash") | [.t, .text]' trace.jsonl
```

```text
["1.011425347s","crash n1"]
["1.569928720s","crash n1"]
["2.787770290s","crash n1"]
["3.602924326s","crash n1"]
["4.762963737s","crash n1"]
["5.551909068s","crash n1"]
```

`history.jsonl` starts with a header line, then holds one operation per line, with the virtual times
of its call and return in nanoseconds:

```sh
jq -c 'select(.input == "k169=v169")' history.jsonl
```

```text
{"id":169,"process":"c1","f":"put","status":"ok","call":5546690866,"return":5553747583,"call_index":337,"return_index":338,"input":"k169=v169","output":null}
```

`schedule.json` lists the faults. [Replay a failing
seed](replay-a-seed.md#narrow-a-failure-by-editing-its-schedule) shows how to edit and replay it.

## Render the files again

`timeline.txt`, `hb.mmd` and `timeline.html` are built from `report.json` and `trace.jsonl`. The
`faultline` command builds them again, for example with a larger causal slice:

```sh
faultline render -slice-cap 500 /tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e
```

```text
/tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/timeline.txt
/tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/hb.mmd
/tmp/faultline-501/example.com_kv/TestKV/287372ab06f1482e/timeline.html
```

It writes these three files and changes no other file. [The `faultline`
command](../reference/cli.md) shows how to install it and lists its flags.

## Next steps

- [Replay a failing seed](replay-a-seed.md): confirm the replay and narrow the fault schedule.
- [Artifact files](../reference/artifacts.md): every field of every file.
- [Use faultline with an AI coding agent](../ai-agents.md): the debugging loop as an agent runs it.
