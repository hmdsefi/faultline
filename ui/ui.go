// Package ui embeds faultline's web assets: the Phase 1 run view renderer that timeline.html
// inlines, and (Phase 4) the faultline view app. It imports only the standard library.
package ui

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"
)

// FS holds the web assets. Paths start with "static/". Phase 4 (UI) adds files but keeps this
// declaration (UI-185), and adds Static() fs.FS, which returns fs.Sub(FS, "static").
//
//go:embed static
var FS embed.FS

// Paths in FS.
const (
	TimelineTemplate = "static/timeline.html"       // single-file page template (ART-071)
	TimelineEntry    = "static/js/timeline-main.js" // entry module of timeline.html (ART-077)
	ThemeCSS         = "static/css/theme.css"       // stylesheet inlined into timeline.html
)

// jsRoot is the directory every bundled module lives in (ART-075).
const jsRoot = "static/js/"

var (
	importLine = regexp.MustCompile(`^import \{ ([A-Za-z_$][A-Za-z0-9_$]*(?:, [A-Za-z_$][A-Za-z0-9_$]*)*) \} from "(\.\.?/[^"]+\.js)";$`)
	identifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*`)
)

// exportPrefixes are the allowed export forms (ART-075).
var exportPrefixes = []string{"export function ", "export async function ", "export const ", "export class "}

// badChars are the characters no line may hold, checked in this order, with the reason (ART-075).
// Lines are split at \n only, but JavaScript also ends a line at \r, U+2028 and U+2029, and a
// byte-order mark is invisible: each would hide a keyword from the line rules.
var badChars = []struct{ char, reason string }{
	{"\r", "carriage return; use \\n line endings"},
	{"\u2028", "line separator (U+2028); use \\n line endings, or write \\u2028 in strings"},
	{"\u2029", "paragraph separator (U+2029); use \\n line endings, or write \\u2029 in strings"},
	{"\uFEFF", "byte-order mark (U+FEFF); save the file as UTF-8 without one"},
}

// forbidden lists substrings no bundled module may contain, with the reason (ART-075).
var forbidden = []struct{ token, reason string }{
	{"import(", "dynamic import() is not allowed"},
	{"import.meta", "import.meta is not allowed"},
	{"fetch(", "network access (fetch) is not allowed"},
	{"XMLHttpRequest", "network access (XMLHttpRequest) is not allowed"},
	{"WebSocket", "network access (WebSocket) is not allowed"},
	{"EventSource", "network access (EventSource) is not allowed"},
	{"sendBeacon", "network access (sendBeacon) is not allowed"},
	{"http://", "URLs (http://) are not allowed"},
	{"https://", "URLs (https://) are not allowed"},
	{"innerHTML", "innerHTML is not allowed; use textContent"},
	{"outerHTML", "outerHTML is not allowed; use textContent"},
	{"insertAdjacentHTML", "insertAdjacentHTML is not allowed; use textContent"},
	{"document.write", "document.write is not allowed"},
	{"eval(", "eval is not allowed"},
	{"new Function", "new Function is not allowed"},
	{"Math.random", "Math.random is not allowed (the picture must be deterministic)"},
	{"Date.now", "Date.now is not allowed (the picture must be deterministic)"},
	{"new Date", "new Date is not allowed (the picture must be deterministic)"},
}

// module is one parsed ES module of the closure.
type module struct {
	path     string
	id       string
	imports  []moduleImport // in source order
	imported map[string]int // imported name → its import line (lookups only)
	body     []string       // non-import lines, "export " removed
	exports  []string       // in source order
}

type moduleImport struct {
	names  string // "a, b" as written
	target string // resolved path in the FS
}

// Bundle returns the import closure of entry (a path in FS), in dependency order, as one
// classic script (ART-076).
func Bundle(entry string) (string, error) { return bundleFS(FS, entry) }

// bundler holds the state of one bundleFS call.
type bundler struct {
	fsys    fs.FS
	entry   string
	loaded  map[string]*module // lookups only
	stack   []string           // modules being loaded, for cycle detection
	order   []*module          // post-order
	idOwner map[string]string  // module id → path (lookups only)
}

// bundleFS implements Bundle over any file system (ART-076).
func bundleFS(fsys fs.FS, entry string) (string, error) {
	b := &bundler{fsys: fsys, entry: entry, loaded: map[string]*module{}, idOwner: map[string]string{}}
	if err := b.load(entry); err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("\"use strict\";\n")
	for _, m := range b.order {
		fmt.Fprintf(&out, "// ---- %s ----\n", m.path)
		fmt.Fprintf(&out, "const __fl_%s = (() => {\n", m.id)
		for _, im := range m.imports {
			fmt.Fprintf(&out, "const { %s } = __fl_%s;\n", im.names, b.loaded[im.target].id)
		}
		for _, l := range m.body {
			out.WriteString(l)
			out.WriteByte('\n')
		}
		if len(m.exports) == 0 {
			out.WriteString("return {};\n")
		} else {
			fmt.Fprintf(&out, "return { %s };\n", strings.Join(m.exports, ", "))
		}
		out.WriteString("})();\n")
	}
	return out.String(), nil
}

// moduleID returns the module id of a path under static/js/ (ART-075). It replaces bytes, not
// runes: "é.js" (two bytes) gets "__", so it cannot collide with "_.js".
func moduleID(p string) string {
	id := []byte(strings.TrimSuffix(strings.TrimPrefix(p, jsRoot), ".js"))
	for i, c := range id {
		if !isAlnum(c) {
			id[i] = '_'
		}
	}
	return string(id)
}

func isAlnum(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

// isIdentByte reports whether c can continue an identifier. Bytes of non-ASCII letters are not,
// so such a line counts as a keyword line and fails loudly rather than reaching the bundle.
func isIdentByte(c byte) bool {
	return isAlnum(c) || c == '_' || c == '$'
}

// startsWithKeyword reports whether line, after leading spaces and tabs, begins with the keyword
// kw: kw followed by the end of the line or a byte that cannot continue an identifier (ART-075).
// "import\t{", "import'./a.js'" and an indented "export const" are keyword lines, so they must be
// valid or fail; "importance" is not one.
func startsWithKeyword(line, kw string) bool {
	t := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(t, kw) && (len(t) == len(kw) || !isIdentByte(t[len(kw)]))
}

// isComment reports whether line is a // comment. Block comments are followed in load.
func isComment(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "//")
}

// isExportLine reports whether line is an export line (ART-075).
func isExportLine(line string) bool { return startsWithKeyword(line, "export") }

// isImportLine reports whether line is an import line (ART-075).
func isImportLine(line string) bool { return startsWithKeyword(line, "import") }

// asKey reports whether keyword line `line` is an object key or a method named kw, such as
// "  export: save," or "  export() {". ART-075 rejects such a line like any other keyword line,
// so its error says how to keep it.
func asKey(line, kw string) bool {
	rest := strings.TrimLeft(strings.TrimLeft(line, " \t")[len(kw):], " \t")
	return strings.HasPrefix(rest, ":") || kw == "export" && strings.HasPrefix(rest, "(")
}

// keyReason is the error reason for a key or method named kw at the start of a line.
func keyReason(kw string) string {
	return fmt.Sprintf(`a key or method named %s cannot start a line; write it in quotes ("%s")`, kw, kw)
}

// afterComment follows the block comments at the start of t, which is inside one when open and
// otherwise starts with "/*". It reports whether a comment is still open at the end of t, and
// returns the text after the last comment that closed on the line, without leading blanks.
func afterComment(t string, open bool) (bool, string) {
	if !open {
		t = t[len("/*"):]
	}
	for {
		i := strings.Index(t, "*/")
		if i < 0 {
			return true, ""
		}
		t = strings.TrimLeft(t[i+len("*/"):], " \t")
		if !strings.HasPrefix(t, "/*") {
			return false, t
		}
		t = t[len("/*"):]
	}
}

// load parses p and, recursively, its imports; it appends p to b.order after its imports.
func (b *bundler) load(p string) error {
	if !strings.HasPrefix(p, jsRoot) || !strings.HasSuffix(p, ".js") {
		return fmt.Errorf("ui: %s: module is not a .js file under %s", p, jsRoot)
	}
	data, err := fs.ReadFile(b.fsys, p)
	if err != nil {
		return fmt.Errorf("ui: %s: %w", p, err)
	}
	m := &module{path: p, id: moduleID(p), imported: map[string]int{}}
	if other, ok := b.idOwner[m.id]; ok {
		return fmt.Errorf("ui: %s: module id %s is also used by %s", p, m.id, other)
	}
	b.idOwner[m.id] = p
	b.stack = append(b.stack, p)
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	first := 0         // the line of the first statement, 0 before it
	inComment := false // inside a /* */ comment that opened at the start of a line
	for i, line := range lines {
		n := i + 1
		lineErr := func(reason string) error { return fmt.Errorf("ui: %s:%d: %s", p, n, reason) }
		for _, c := range badChars {
			if strings.Contains(line, c.char) {
				return lineErr(c.reason)
			}
		}
		// Block comment text is not module syntax: a commented-out export or import must not be
		// rewritten or loaded. code is what follows the comment on the line ("" inside it).
		code := line
		if t := strings.TrimLeft(line, " \t"); inComment || strings.HasPrefix(t, "/*") {
			inComment, code = afterComment(t, inComment)
			if isImportLine(code) || isExportLine(code) {
				return lineErr("import or export after a comment on its line; give the comment its own line")
			}
		}
		if isImportLine(code) {
			if err := b.addImport(m, line, n, first); err != nil {
				return err
			}
			continue
		}
		if reason := b.tokenRule(p, line); reason != "" {
			return lineErr(reason)
		}
		// UI-049: the bundle runs each module inside a function, where top-level await is a
		// SyntaxError. An indented line belongs to a function, where await is fine. A comment
		// before the keyword does not hide it; comment text alone (code "") is never checked.
		if strings.HasPrefix(code, "await") && (len(code) == len("await") || !isIdentByte(code[len("await")])) && line[0] != ' ' && line[0] != '\t' {
			return lineErr("top-level await is not allowed; each module runs inside a function")
		}
		if first == 0 && strings.TrimSpace(code) != "" && !isComment(code) {
			first = n
		}
		if isExportLine(code) {
			name, reason := exportName(line)
			if reason != "" {
				return lineErr(reason)
			}
			m.exports = append(m.exports, name)
			line = strings.TrimPrefix(line, "export ")
		}
		m.body = append(m.body, line)
	}
	b.stack = b.stack[:len(b.stack)-1]
	b.loaded[p] = m
	b.order = append(b.order, m)
	return nil
}

// tokenRule returns the reason line of module p breaks ART-075's token rules (the forbidden
// tokens, then element IDs outside the entry module), or "". Comments are checked too.
func (b *bundler) tokenRule(p, line string) string {
	for _, f := range forbidden {
		if strings.Contains(line, f.token) {
			return f.reason
		}
	}
	if strings.Contains(line, "getElementById") && p != b.entry {
		return "only the entry module may read element IDs"
	}
	return ""
}

// addImport checks import line n of m (ART-075), loads the module it names and records the
// import. first is the line of m's first statement, or 0. An import line is checked against the
// import rules only, not against the forbidden tokens (ART-076).
func (b *bundler) addImport(m *module, line string, n, first int) error {
	lineErr := func(reason string) error { return fmt.Errorf("ui: %s:%d: %s", m.path, n, reason) }
	if asKey(line, "import") {
		return lineErr(keyReason("import"))
	}
	if first > 0 {
		return lineErr(fmt.Sprintf("import after the first statement (line %d); imports come first", first))
	}
	sub := importLine.FindStringSubmatch(line)
	if sub == nil {
		return lineErr(`malformed import; want import { a, b } from "./x.js";`)
	}
	// Each import becomes a const in the module's wrapper, so a name bound twice would stop the
	// bundle from parsing; native modules reject it too.
	for _, name := range strings.Split(sub[1], ", ") {
		if at, ok := m.imported[name]; ok {
			return lineErr(fmt.Sprintf(`import "%s": %s is already imported (line %d)`, sub[2], name, at))
		}
		m.imported[name] = n
	}
	target := path.Join(path.Dir(m.path), sub[2])
	if !strings.HasPrefix(target, jsRoot) {
		// importLine keeps '"' out of specifiers, so ART-076's texts quote them as written.
		return lineErr(fmt.Sprintf(`import "%s" resolves outside %s`, sub[2], jsRoot))
	}
	if err := b.require(m.path, target, n); err != nil {
		return err
	}
	dep := b.loaded[target]
	for _, name := range strings.Split(sub[1], ", ") {
		if !slices.Contains(dep.exports, name) {
			has := "it has no exports"
			if len(dep.exports) > 0 {
				has = "it exports " + strings.Join(dep.exports, ", ")
			}
			return lineErr(fmt.Sprintf(`import "%s": %s is not exported by %s (%s)`, sub[2], name, target, has))
		}
	}
	m.imports = append(m.imports, moduleImport{names: sub[1], target: target})
	return nil
}

// require loads target for an import on line n of from, detecting cycles and missing files. A
// cycle's error lists the modules from target round to target, so it names the import to remove.
func (b *bundler) require(from, target string, n int) error {
	if _, ok := b.loaded[target]; ok {
		return nil
	}
	if i := slices.Index(b.stack, target); i >= 0 {
		return fmt.Errorf("ui: %s:%d: import cycle %s -> %s", from, n, strings.Join(b.stack[i:], " -> "), target)
	}
	if _, err := fs.Stat(b.fsys, target); err != nil {
		return fmt.Errorf("ui: %s:%d: import of missing module %s", from, n, target)
	}
	return b.load(target)
}

// exportName returns the identifier an export line exports, or the reason it is not one of
// ART-075's forms.
func exportName(line string) (name, reason string) {
	if asKey(line, "export") {
		return "", keyReason("export")
	}
	for _, pre := range exportPrefixes {
		if strings.HasPrefix(line, pre) {
			if name := identifier.FindString(line[len(pre):]); name != "" {
				return name, ""
			}
		}
	}
	return "", "unsupported export; use export function, export async function, export const or export class"
}
