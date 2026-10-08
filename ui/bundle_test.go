package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const libA = "export const X = 1;\nfunction h() { return X; }\nexport function f() { return h(); }\n"
const mainJS = "import { f } from \"./lib/a.js\";\nexport function g() { return f() + 1; }\n"

func mapFS(main, a string) fstest.MapFS {
	return fstest.MapFS{
		"static/js/lib/a.js": {Data: []byte(a)},
		"static/js/main.js":  {Data: []byte(main)},
	}
}

// AT-ART-12
func TestBundleFS(t *testing.T) {
	got, err := bundleFS(mapFS(mainJS, libA), "static/js/main.js")
	if err != nil {
		t.Fatal(err)
	}
	want := "\"use strict\";\n// ---- static/js/lib/a.js ----\nconst __fl_lib_a = (() => {\nconst X = 1;\nfunction h() { return X; }\nfunction f() { return h(); }\nreturn { X, f };\n})();\n// ---- static/js/main.js ----\nconst __fl_main = (() => {\nconst { f } = __fl_lib_a;\nfunction g() { return f() + 1; }\nreturn { g };\n})();\n"
	if got != want {
		t.Fatalf("bundle mismatch:\n got %q\nwant %q", got, want)
	}
	cyclic := "import { g } from \"../main.js\";\n" + libA
	_, err = bundleFS(mapFS(mainJS, cyclic), "static/js/main.js")
	if err == nil || err.Error() != "ui: static/js/lib/a.js:1: import cycle static/js/main.js -> static/js/lib/a.js -> static/js/main.js" {
		t.Fatalf("cycle: %v", err)
	}
}

// TestBundleFSErrors pins ART-076's text, reason included, for every line rule. Keyword lines
// (ART-075) that are not in the allowed form fail, whatever follows the keyword or precedes it.
func TestBundleFSErrors(t *testing.T) {
	const malformed = `malformed import; want import { a, b } from "./x.js";`
	const unsupported = "unsupported export; use export function, export async function, export const or export class"
	const afterComment = "import or export after a comment on its line; give the comment its own line"
	cases := []struct {
		name, main, want string
	}{
		{"crlf", "const a = 1;\r\n", "1: carriage return; use \\n line endings"},
		{"lone carriage return", "const a = 1;\rconst b = 2;\n", "1: carriage return; use \\n line endings"},
		{"line separator", "const a = 1;\u2028export const b = 2;\n", "1: line separator (U+2028); use \\n line endings, or write \\u2028 in strings"},
		{"paragraph separator", "const a = 1;\n\u2029export const b = 2;\n", "2: paragraph separator (U+2029); use \\n line endings, or write \\u2029 in strings"},
		{"byte-order mark", "\uFEFFimport { f } from \"./lib/a.js\";\n", "1: byte-order mark (U+FEFF); save the file as UTF-8 without one"},
		{"top-level await", "await f();\n", "1: top-level await is not allowed; each module runs inside a function"},
		{"tab after await", "await\tf();\n", "1: top-level await is not allowed; each module runs inside a function"},
		{"byte-order mark inside a line", "const a = 1;\nconst b = \"x\uFEFFy\";\n", "2: byte-order mark (U+FEFF); save the file as UTF-8 without one"},
		{"default import", "import x from \"./lib/a.js\";\n", "1: " + malformed},
		{"tab after import", "import\t{ f } from \"./lib/a.js\";\n", "1: " + malformed},
		{"quote after import", "import'./lib/a.js';\n", "1: " + malformed},
		{"comment after import", "import/**/{ f } from \"./lib/a.js\";\n", "1: " + malformed},
		{"indented import", "  import { f } from \"./lib/a.js\";\n", "1: " + malformed},
		{"tab-indented import", "\timport { f } from \"./lib/a.js\";\n", "1: " + malformed},
		{"non-ASCII byte after import", "importé = 1;\n", "1: " + malformed},
		{"dynamic import at the line start", "import(\"./lib/a.js\");\n", "1: " + malformed},
		{"bare import", "import\n", "1: " + malformed},
		{"bare specifier", "import { f } from \"lib/a.js\";\n", "1: " + malformed},
		{"import after code", "const a = 1;\nimport { f } from \"./lib/a.js\";\n", "2: import after the first statement (line 1); imports come first"},
		{"import after a block comment and code", "/*\n  header\n*/\n\nf();\nimport { f } from \"./lib/a.js\";\n", "6: import after the first statement (line 5); imports come first"},
		{"key named import", "const o = {\n  import: 1,\n};\n", "2: a key or method named import cannot start a line; write it in quotes (\"import\")"},
		{"outside static/js", "import { f } from \"../x.js\";\n", `1: import "../x.js" resolves outside static/js/`},
		{"backslash in the specifier", "import { f } from \"../a\\b.js\";\n", `1: import "../a\b.js" resolves outside static/js/`},
		{"missing module", "import { f } from \"./lib/c.js\";\n", "1: import of missing module static/js/lib/c.js"},
		{"not exported", "import { f, z } from \"./lib/a.js\";\n", `1: import "./lib/a.js": z is not exported by static/js/lib/a.js (it exports X, f)`},
		{"export default", "export default 1\n", "1: " + unsupported},
		{"export let", "export let y = 1\n", "1: " + unsupported},
		{"export var", "export var y = 1\n", "1: " + unsupported},
		{"export braces", "export { g }\n", "1: " + unsupported},
		{"export star", "export * from \"./lib/a.js\";\n", "1: " + unsupported},
		{"tab after export", "export\tconst y = 1;\n", "1: " + unsupported},
		{"indented export", "  export const y = 1;\n", "1: " + unsupported},
		{"tab-indented export", "\texport const y = 1;\n", "1: " + unsupported},
		{"bare export", "export\n", "1: " + unsupported},
		{"key named export", "const o = {\n  export: () => 1,\n};\n", "2: a key or method named export cannot start a line; write it in quotes (\"export\")"},
		{"method named export", "class C {\n  export() {}\n}\n", "2: a key or method named export cannot start a line; write it in quotes (\"export\")"},
		{"export after a comment", "/* doc */ export const y = 1;\n", "1: " + afterComment},
		{"import after a closing comment", "/*\n doc\n*/ import { f } from \"./lib/a.js\";\n", "3: " + afterComment},
		{"await after a comment", "/* x */ await f();\n", "1: top-level await is not allowed; each module runs inside a function"},
		{"await after a closing comment", "/*\n doc\n*/ await f();\n", "3: top-level await is not allowed; each module runs inside a function"},
		{"a name imported twice on one line", "import { f, f } from \"./lib/a.js\";\n", `1: import "./lib/a.js": f is already imported (line 1)`},
		{"a name imported twice", "import { f } from \"./lib/a.js\";\nimport { X, f } from \"./lib/a.js\";\n", `2: import "./lib/a.js": f is already imported (line 1)`},
		{"a name imported twice after a header", "// header\nimport { f } from \"./lib/a.js\";\nimport { f } from \"./lib/a.js\";\n", `3: import "./lib/a.js": f is already imported (line 2)`},
		{"bare await", "await\n", "1: top-level await is not allowed; each module runs inside a function"},
		{"bare await after a comment", "/* x */ await\n", "1: top-level await is not allowed; each module runs inside a function"},
	}
	for _, c := range cases {
		_, err := bundleFS(mapFS(c.main, libA), "static/js/main.js")
		if want := "ui: static/js/main.js:" + c.want; err == nil || err.Error() != want {
			t.Errorf("%s: err = %v, want %q", c.name, err, want)
		}
	}
	slash := fstest.MapFS{
		"static/js/main.js": {Data: []byte("import { z } from \"./a\\b.js\";\n")},
		"static/js/a\\b.js": {Data: []byte("export const y = 1;\n")},
	}
	if _, err := bundleFS(slash, "static/js/main.js"); err == nil || err.Error() != `ui: static/js/main.js:1: import "./a\b.js": z is not exported by static/js/a\b.js (it exports y)` {
		t.Errorf("backslash, not exported: %v", err)
	}
	entries := []struct{ entry, want string }{
		{"static/js/nope.js", "ui: static/js/nope.js: open static/js/nope.js: file does not exist"},
		{"static/x.js", "ui: static/x.js: module is not a .js file under static/js/"},
		{"static/js/main.mjs", "ui: static/js/main.mjs: module is not a .js file under static/js/"},
	}
	for _, e := range entries {
		if _, err := bundleFS(mapFS(mainJS, libA), e.entry); err == nil || err.Error() != e.want {
			t.Errorf("entry %s: err = %v, want %q", e.entry, err, e.want)
		}
	}
	if _, err := bundleFS(mapFS(mainJS, libA), "static/js/nope.js"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing entry's error does not wrap fs.ErrNotExist: %v", err)
	}
}

// TestBundleFSForbidden checks every forbidden token of ART-075 with ART-076's reason, in code
// and in a comment, and that the list's order decides which of two tokens on a line is reported.
func TestBundleFSForbidden(t *testing.T) {
	tokens := []struct{ token, reason string }{
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
	for _, tok := range tokens {
		// In code, a // comment, a one-line block comment, and block comment text.
		for _, c := range []struct {
			text string
			line int
		}{
			{"x = \"" + tok.token + "\";", 2},
			{"// " + tok.token, 2},
			{"/* " + tok.token + " */", 2},
			{"/*\n" + tok.token + "\n*/", 3},
		} {
			_, err := bundleFS(mapFS("const a = 1;\n"+c.text+"\n", libA), "static/js/main.js")
			if want := fmt.Sprintf("ui: static/js/main.js:%d: %s", c.line, tok.reason); err == nil || err.Error() != want {
				t.Errorf("%q: err = %v, want %q", c.text, err, want)
			}
		}
	}
	_, err := bundleFS(mapFS("eval(fetch(1));\n", libA), "static/js/main.js")
	if err == nil || err.Error() != "ui: static/js/main.js:1: network access (fetch) is not allowed" {
		t.Errorf("eval(fetch(1)): %v", err)
	}
}

func TestBundleFSRules(t *testing.T) {
	cases := []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{"element ids outside the entry", fstest.MapFS{
			"static/js/main.js":  {Data: []byte("import { f } from \"./lib/a.js\";\nf();\n")},
			"static/js/lib/a.js": {Data: []byte("export function f() { return document.getElementById(\"app\"); }\n")},
		}, "ui: static/js/lib/a.js:1: only the entry module may read element IDs"},
		{"one name per export const line", fstest.MapFS{
			"static/js/main.js": {Data: []byte("import { b } from \"./a.js\";\n")},
			"static/js/a.js":    {Data: []byte("export const a = 1, b = 2;\n")},
		}, `ui: static/js/main.js:1: import "./a.js": b is not exported by static/js/a.js (it exports a)`},
		{"no exports", fstest.MapFS{
			"static/js/main.js": {Data: []byte("import { b } from \"./a.js\";\n")},
			"static/js/a.js":    {Data: []byte("const b = 2;\n")},
		}, `ui: static/js/main.js:1: import "./a.js": b is not exported by static/js/a.js (it has no exports)`},
		{"one name from two modules", fstest.MapFS{
			"static/js/main.js": {Data: []byte("import { a } from \"./a.js\";\nimport { a } from \"./b.js\";\n")},
			"static/js/a.js":    {Data: []byte("export const a = 1;\n")},
			"static/js/b.js":    {Data: []byte("export const a = 2;\n")},
		}, `ui: static/js/main.js:2: import "./b.js": a is already imported (line 1)`},
		{"cycle that leaves out the entry", fstest.MapFS{
			"static/js/main.js": {Data: []byte("import { a } from \"./a.js\";\n")},
			"static/js/a.js":    {Data: []byte("import { b } from \"./b.js\";\nexport const a = 1;\n")},
			"static/js/b.js":    {Data: []byte("import { a } from \"./a.js\";\nexport const b = 2;\n")},
		}, "ui: static/js/b.js:1: import cycle static/js/a.js -> static/js/b.js -> static/js/a.js"},
		{"duplicate module id", fstest.MapFS{
			"static/js/main.js": {Data: []byte("import { a } from \"./a-b.js\";\nimport { b } from \"./a_b.js\";\n")},
			"static/js/a-b.js":  {Data: []byte("export const a = 1;\n")},
			"static/js/a_b.js":  {Data: []byte("export const b = 1;\n")},
		}, "ui: static/js/a_b.js: module id a_b is also used by static/js/a-b.js"},
	}
	for _, c := range cases {
		_, err := bundleFS(c.fsys, "static/js/main.js")
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	ok := fstest.MapFS{
		"static/js/main.js": {Data: []byte("// header comment\n\nimport { a } from \"./a.js\";\nconsole.log(a);\n")},
		"static/js/a.js":    {Data: []byte("export class a {}\n")},
	}
	got, err := bundleFS(ok, "static/js/main.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "const __fl_main = (() => {\nconst { a } = __fl_a;\n// header comment\n\nconsole.log(a);\nreturn {};\n})();\n") {
		t.Fatalf("bundle:\n%s", got)
	}
	// Ids replace bytes: the two bytes of "é" give "__", so é.js and _.js do not collide.
	if id := moduleID("static/js/é.js"); id != "__" {
		t.Errorf("moduleID(é.js) = %q, want \"__\"", id)
	}
	if id := moduleID("static/js/timeline/model.js"); id != "timeline_model" {
		t.Errorf("moduleID(timeline/model.js) = %q", id)
	}
	accents := fstest.MapFS{
		"static/js/main.js": {Data: []byte("import { a } from \"./é.js\";\nimport { b } from \"./_.js\";\n")},
		"static/js/é.js":    {Data: []byte("export const a = 1;\n")},
		"static/js/_.js":    {Data: []byte("export const b = 1;\n")},
	}
	if got, err := bundleFS(accents, "static/js/main.js"); err != nil || !strings.Contains(got, "const { a } = __fl___;\nconst { b } = __fl__;\n") {
		t.Errorf("é.js and _.js: err = %v, bundle:\n%s", err, got)
	}
	// A keyword followed by an identifier byte starts an identifier, not an import or export line,
	// and an indented await belongs to an async function.
	keywordish := "importance = 1;\nimport_x = 1;\nexport$ = 1;\nawaited = 1;\nasync function w() {\n  await w();\n  /* again */ await w();\n\t/* again */ await w();\n}\n"
	if got, err := bundleFS(mapFS(keywordish, libA), "static/js/main.js"); err != nil || !strings.Contains(got, "\nimport_x = 1;\nexport$ = 1;\nawaited = 1;\n") {
		t.Errorf("identifiers that start with a keyword: err = %v, bundle:\n%s", err, got)
	}
}

// TestBundleFSForms pins the bundle of ART-075's other allowed forms: names with digits and $,
// export async function, and block comments, whose text is not module syntax.
func TestBundleFSForms(t *testing.T) {
	cases := []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{"digits, $ and async", fstest.MapFS{
			"static/js/main.js": {Data: []byte("/**\n * Header.\n */\nimport { $x1, run2 } from \"./a.js\";\n$x1; run2;\n")},
			"static/js/a.js":    {Data: []byte("export const $x1 = 1;\nexport async function run2() {}\n")},
		}, "\"use strict\";\n// ---- static/js/a.js ----\nconst __fl_a = (() => {\nconst $x1 = 1;\nasync function run2() {}\nreturn { $x1, run2 };\n})();\n" +
			"// ---- static/js/main.js ----\nconst __fl_main = (() => {\nconst { $x1, run2 } = __fl_a;\n/**\n * Header.\n */\n$x1; run2;\nreturn {};\n})();\n"},
		{"commented-out export, import and await", fstest.MapFS{
			"static/js/main.js": {Data: []byte("/*\n  License text, no stars.\nimport { gone } from \"./gone.js\";\n*/\nimport { g } from \"./a.js\";\n  /* one line */\ng();\n")},
			"static/js/a.js":    {Data: []byte("/*\nexport function old() {}\nawait old();\n*/ /* two\nexport function gone() {}\n*/\nexport function g() { return 1; }\n")},
		}, "\"use strict\";\n// ---- static/js/a.js ----\nconst __fl_a = (() => {\n/*\nexport function old() {}\nawait old();\n*/ /* two\nexport function gone() {}\n*/\nfunction g() { return 1; }\nreturn { g };\n})();\n" +
			"// ---- static/js/main.js ----\nconst __fl_main = (() => {\nconst { g } = __fl_a;\n/*\n  License text, no stars.\nimport { gone } from \"./gone.js\";\n*/\n  /* one line */\ng();\nreturn {};\n})();\n"},
	}
	for _, c := range cases {
		got, err := bundleFS(c.fsys, "static/js/main.js")
		if err != nil || got != c.want {
			t.Errorf("%s: err = %v\n got %q\nwant %q", c.name, err, got, c.want)
		}
	}
}

// ART-076 items 2 and 3: imports are followed in source order, each module once and before its
// importers, the entry last; a module's import lines keep their order.
func TestBundleFSOrder(t *testing.T) {
	diamond := fstest.MapFS{
		"static/js/main.js": {Data: []byte("import { b } from \"./b.js\";\nimport { c } from \"./c.js\";\nb; c;\n")},
		"static/js/b.js":    {Data: []byte("import { d } from \"./d.js\";\nexport const b = d;\n")},
		"static/js/c.js":    {Data: []byte("import { d } from \"./d.js\";\nexport const c = d;\n")},
		"static/js/d.js":    {Data: []byte("export const d = 1;\n")},
	}
	got, err := bundleFS(diamond, "static/js/main.js")
	if err != nil {
		t.Fatal(err)
	}
	want := "\"use strict\";\n" +
		"// ---- static/js/d.js ----\nconst __fl_d = (() => {\nconst d = 1;\nreturn { d };\n})();\n" +
		"// ---- static/js/b.js ----\nconst __fl_b = (() => {\nconst { d } = __fl_d;\nconst b = d;\nreturn { b };\n})();\n" +
		"// ---- static/js/c.js ----\nconst __fl_c = (() => {\nconst { d } = __fl_d;\nconst c = d;\nreturn { c };\n})();\n" +
		"// ---- static/js/main.js ----\nconst __fl_main = (() => {\nconst { b } = __fl_b;\nconst { c } = __fl_c;\nb; c;\nreturn {};\n})();\n"
	if got != want {
		t.Fatalf("diamond bundle:\n got %q\nwant %q", got, want)
	}
}

// checkScripts is run by node. It compiles each bundle as a classic script, the way
// timeline.html's <script> element runs it. A bundle with an id is also run in an empty context
// (no DOM) and must return the exports of its last module: those of the module imported natively
// from file, or the listed ones. Any difference means the bundled page breaks where native
// modules work. It prints each failure.
const checkScripts = `const vm = require("vm");
const { pathToFileURL } = require("url");
const list = JSON.parse(require("fs").readFileSync(0, "utf8"));
const fail = (s, why) => { console.log(s.name + ": " + why); process.exitCode = 1; };
(async () => {
  for (const s of list) {
    let script;
    try { script = new vm.Script(s.src + (s.id ? "\n;__fl_" + s.id : ""), { filename: s.name }); } catch (e) { fail(s, e.message); continue; }
    if (!s.id) continue;
    let got;
    try { got = Object.keys(script.runInNewContext({})).sort(); } catch (e) { fail(s, "run: " + e.name + ": " + e.message); continue; }
    const want = s.file ? Object.keys(await import(pathToFileURL(s.file))).sort() : [...s.exports].sort();
    if (got.join() !== want.join()) fail(s, "exports " + got.join(", ") + ", want " + want.join(", "));
  }
})();`

// script is one bundle for checkScripts: run it when ID is set, and compare its exports with
// File's native ones, or with Exports.
type script struct {
	Name    string   `json:"name"`
	Src     string   `json:"src"`
	ID      string   `json:"id,omitempty"`
	File    string   `json:"file,omitempty"`
	Exports []string `json:"exports"`
}

// TestBundleIsClassicScript checks that modules that follow ART-075's line rules bundle into a
// classic script that behaves as the native modules do (ART-076 item 3): every module of FS as an
// entry, the tests' fixtures, and lines that look like imports, exports or top-level await. Lines
// that would need a module script must have failed instead. Bundling FS needs no node, so it runs
// before the node check can skip.
func TestBundleIsClassicScript(t *testing.T) {
	var scripts []script
	err := fs.WalkDir(FS, "static/js", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		src, err := bundleFS(FS, p)
		if err != nil {
			return err
		}
		s := script{Name: p, Src: src}
		// The entry reads the page's elements, so only the other modules run without a DOM.
		if p != TimelineEntry {
			file, err := filepath.Abs(filepath.FromSlash(p))
			if err != nil {
				return err
			}
			s.ID, s.File = moduleID(p), file
		}
		scripts = append(scripts, s)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Identifiers and text that start with a keyword are code and must bundle; the bundles whose
	// main.js has no undeclared names also run.
	for _, c := range []struct {
		main    string
		exports []string
	}{
		{mainJS, []string{"g"}},
		{"importance = 1;\nimport_x = 1;\nexports.y = 1;\nexport$ = 1;\n", nil},
		{"// import { f } from \"./lib/a.js\";\nconst s = \"export const\";\n", []string{}},
		{"async function w() {\n  await w();\n  /* again */ await w();\n\t/* again */ await w();\n}\n", []string{}},
		{"/*\nimport { gone } from \"./gone.js\";\nexport function old() {}\n*/\nexport function g() { return 1; }\n", []string{"g"}},
		{"const o = {\n  \"export\": () => 1,\n  \"import\"() {},\n};\nexport const k = \"\\u2028\";\n", []string{"k"}},
	} {
		src, err := bundleFS(mapFS(c.main, libA), "static/js/main.js")
		if err != nil {
			t.Fatalf("%q: %v", c.main, err)
		}
		s := script{Name: "main.js: " + strings.TrimSpace(c.main), Src: src, Exports: c.exports}
		if c.exports != nil {
			s.ID = "main"
		}
		scripts = append(scripts, s)
	}
	// Imports and exports in other forms fail (TestBundleFSErrors has the texts); one that bundled
	// would have to parse too.
	for _, main := range []string{
		"import\t{ f } from \"./lib/a.js\";\n",
		"import'./lib/a.js';\n",
		"import/**/{ f } from \"./lib/a.js\";\n",
		"  import { f } from \"./lib/a.js\";\n",
		"export\tconst y = 1;\n",
		"  export const y = 1;\n",
		"\timport { f } from \"./lib/a.js\";\n",
		"\texport const y = 1;\n",
		"\uFEFFimport { f } from \"./lib/a.js\";\n",
		"await f();\n",
		"const a = 1;\u2028export const b = 2;\n",
		"/* doc */ export const y = 1;\n",
		"/* x */ await f();\n",
		"import { f, f } from \"./lib/a.js\";\n",
	} {
		if src, err := bundleFS(mapFS(main, libA), "static/js/main.js"); err == nil {
			scripts = append(scripts, script{Name: "main.js: " + strings.TrimSpace(main), Src: src})
		}
	}
	node := nodeOrSkip(t)
	in, err := json.Marshal(scripts)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runNode(t, node, in, "-e", checkScripts); err != nil {
		t.Fatalf("bundles that do not parse as a classic script or do not behave as the modules: %v\n%s", err, out)
	}
}
