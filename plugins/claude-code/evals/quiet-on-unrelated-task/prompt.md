---
description: "The faultline skill stays unloaded for a Go bug unrelated to distributed systems."
tags: [trigger]
max_turns: 5
allowed_tools: [Read, Glob, Grep, Skill]
---

This Go test fails with `got "1.5 KB", want "1.5 KiB"`. What should I change in FormatBytes?

```go
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func TestFormatBytes(t *testing.T) {
	if got, want := FormatBytes(1536), "1.5 KiB"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
```
