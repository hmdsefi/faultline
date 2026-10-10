package etcdraft

import (
	"fmt"

	"go.etcd.io/raft/v3"
	"go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"

	"github.com/hmdsefi/faultline/kernel"
)

// maxLogText is the byte limit of an etcdraft.raft_log record's text (ETC-100).
const maxLogText = 512

// raftLogger routes raft's log calls into the trace (ETC-100 to ETC-102). node is nil
// for the global logger installed with raft.SetLogger.
type raftLogger struct {
	sim   *kernel.Sim
	node  *kernel.Node
	level LogLevel
}

var _ raft.Logger = (*raftLogger)(nil)

// sanitize applies ETC-101 to log arguments.
func sanitize(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = sanitizeArg(a)
	}
	return out
}

func sanitizeArg(a any) any {
	switch v := a.(type) {
	case *raftpb.HardState:
		if v == nil {
			return "<nil>"
		}
		return raft.DescribeHardState(v)
	case *raftpb.Entry:
		if v == nil {
			return "<nil>"
		}
		return raft.DescribeEntry(v, nil)
	case *raftpb.Message:
		if v == nil {
			return "<nil>"
		}
		return raft.DescribeMessage(v, nil)
	case *raftpb.Snapshot:
		if v == nil {
			return "<nil>"
		}
		return raft.DescribeSnapshot(v)
	case *raftpb.ConfState:
		if v == nil {
			return "<nil>"
		}
		return raft.DescribeConfState(v)
	case *raftpb.ConfChange:
		if v == nil {
			return "<nil>"
		}
		return raft.DescribeConfChange(v)
	case *raftpb.ConfChangeV2:
		if v == nil {
			return "<nil>"
		}
		return raft.DescribeConfChange(v)
	case proto.Message:
		return "<" + string(proto.MessageName(v)) + ">"
	}
	return a
}

func (l *raftLogger) emit(level, msg string) {
	if len(msg) > maxLogText {
		msg = msg[:maxLogText]
	}
	r := kernel.Record{Kind: "etcdraft.raft_log", Text: msg, Attrs: []kernel.Attr{{Key: "level", Value: level}}}
	if l.node != nil {
		r.Node = l.node.ID()
	}
	l.sim.Emit(r)
}

func (l *raftLogger) log(lvl LogLevel, name, msg string) {
	if lvl <= l.level {
		l.emit(name, msg)
	}
}

func (l *raftLogger) Debug(v ...any) { l.log(LogDebug, "debug", fmt.Sprint(sanitize(v)...)) }
func (l *raftLogger) Debugf(format string, v ...any) {
	l.log(LogDebug, "debug", fmt.Sprintf(format, sanitize(v)...))
}
func (l *raftLogger) Info(v ...any) { l.log(LogInfo, "info", fmt.Sprint(sanitize(v)...)) }
func (l *raftLogger) Infof(format string, v ...any) {
	l.log(LogInfo, "info", fmt.Sprintf(format, sanitize(v)...))
}
func (l *raftLogger) Warning(v ...any) { l.log(LogWarning, "warning", fmt.Sprint(sanitize(v)...)) }
func (l *raftLogger) Warningf(format string, v ...any) {
	l.log(LogWarning, "warning", fmt.Sprintf(format, sanitize(v)...))
}
func (l *raftLogger) Error(v ...any) { l.log(LogError, "error", fmt.Sprint(sanitize(v)...)) }
func (l *raftLogger) Errorf(format string, v ...any) {
	l.log(LogError, "error", fmt.Sprintf(format, sanitize(v)...))
}

func (l *raftLogger) Fatal(v ...any) { l.fatal(fmt.Sprint(sanitize(v)...)) }
func (l *raftLogger) Fatalf(format string, v ...any) {
	l.fatal(fmt.Sprintf(format, sanitize(v)...))
}
func (l *raftLogger) Panic(v ...any) { l.panic(fmt.Sprint(sanitize(v)...)) }
func (l *raftLogger) Panicf(format string, v ...any) {
	l.panic(fmt.Sprintf(format, sanitize(v)...))
}

func (l *raftLogger) panic(msg string) {
	l.emit("panic", msg)
	panic(msg)
}

func (l *raftLogger) fatal(msg string) {
	l.emit("fatal", msg)
	panic("raft fatal: " + msg)
}
