---
type: regex
pattern: 'func TestKV\(t \*testing\.T\) \{ faultline\.Run\(t, kvOptions, kvWorld\) \}'
target:
  source: file
  path: kv_test.go
---
