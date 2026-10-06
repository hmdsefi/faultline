package history

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Processes returns the distinct process names of ops, sorted ascending (byte order).
func Processes(ops []Op) []string {
	if len(ops) == 0 {
		return nil
	}
	seen := map[string]bool{} // membership only
	var out []string
	for _, op := range ops {
		if !seen[op.Process] {
			seen[op.Process] = true
			out = append(out, op.Process)
		}
	}
	slices.Sort(out)
	return out
}

// ForProcess returns, in input order, the ops whose Process is process.
func ForProcess(ops []Op, process string) []Op {
	var out []Op
	for _, op := range ops {
		if op.Process == process {
			out = append(out, op)
		}
	}
	return out
}

// SortByCall sorts ops in place by Call ascending, then ID ascending.
func SortByCall(ops []Op) {
	slices.SortFunc(ops, func(a, b Op) int {
		switch {
		case a.Call < b.Call:
			return -1
		case a.Call > b.Call:
			return 1
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
}

// DecodeInput unmarshals op.Input (a json.RawMessage) into v with json.Unmarshal.
func (op Op) DecodeInput(v any) error { return decode(op.ID, "input", op.Input, v) }

// DecodeOutput unmarshals op.Output (a json.RawMessage) into v with json.Unmarshal.
func (op Op) DecodeOutput(v any) error { return decode(op.ID, "output", op.Output, v) }

// decode implements HIS-033.
func decode(id int64, field string, value, v any) error {
	raw, ok := value.(json.RawMessage)
	if !ok {
		return fmt.Errorf("history: op %d: %s is %T, not json.RawMessage", id, field, value)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("history: op %d: decode %s: %w", id, field, err)
	}
	return nil
}
