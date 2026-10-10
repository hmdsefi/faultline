// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package ui

import (
	"bytes"
	"strings"
	"testing"
	"testing/fstest"
)

func pageFS(tmpl, css, js string) fstest.MapFS {
	return fstest.MapFS{
		TimelineTemplate: {Data: []byte(tmpl)},
		ThemeCSS:         {Data: []byte(css)},
		TimelineEntry:    {Data: []byte(js)},
	}
}

const miniTemplate = "<title>{{TITLE}}</title><style>{{CSS}}</style><script type=\"application/json\">{{DATA}}</script><script>{{JS}}</script>"

// ART-071
func TestTimelineHTMLErrors(t *testing.T) {
	var buf bytes.Buffer
	if err := TimelineHTML(&buf, "t", []byte("{")); err == nil || err.Error() != "ui: timeline data is not valid JSON" {
		t.Fatalf("invalid JSON: %v", err)
	}
	cases := []struct {
		name string
		fsys fstest.MapFS
		want string
	}{
		{"missing placeholder", pageFS("{{TITLE}}{{CSS}}{{JS}}", "", "const a = 1;\n"), "ui: template: placeholder {{DATA}} appears 0 times"},
		{"twice", pageFS(miniTemplate+"{{JS}}", "", "const a = 1;\n"), "ui: template: placeholder {{JS}} appears 2 times"},
		{"style close", pageFS(miniTemplate, "a{}</STYLE>", "const a = 1;\n"), `ui: theme.css contains "</style"`},
		{"script close", pageFS(miniTemplate, "", "const a = \"</Script>\";\n"), `ui: script contains "</script"`},
		{"comment open", pageFS(miniTemplate, "", "const a = \"<!--<script>\";\n"), `ui: script contains "<!--"`},
	}
	for _, c := range cases {
		if err := timelineHTML(c.fsys, &buf, "t", []byte("{}")); err == nil || err.Error() != c.want {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

func TestTimelineHTMLReplacement(t *testing.T) {
	var buf bytes.Buffer
	fsys := pageFS(miniTemplate, "b{color:red}", "const s = \"{{DATA}}\";\n")
	if err := timelineHTML(fsys, &buf, "a<b & \"c\"", []byte(`{"x":"</script>{{TITLE}}","y":"<!--<script>"}`)); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.HasPrefix(got, "<title>a&lt;b &amp; &#34;c&#34;</title><style>b{color:red}</style>") {
		t.Fatalf("title/css: %s", got)
	}
	if !strings.Contains(got, `{"x":"\u003c/script>{{TITLE}}","y":"\u003c!--\u003cscript>"}`) {
		t.Fatalf("data not escaped or rescanned: %s", got)
	}
	if !strings.Contains(got, `const s = "{{DATA}}";`) {
		t.Fatalf("script text was rescanned: %s", got)
	}
}

func TestTimelineHTMLRealFS(t *testing.T) {
	var buf bytes.Buffer
	if err := TimelineHTML(&buf, "faultline T", []byte(`{"trace":""}`)); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	if !strings.HasPrefix(page, "<!DOCTYPE html>\n<html lang=\"en\">\n") || !strings.HasSuffix(page, "</script>\n</body>\n</html>\n") {
		t.Fatalf("page frame wrong:\n%s", page[:200])
	}
	for _, p := range []string{"{{TITLE}}", "{{CSS}}", "{{JS}}", "{{DATA}}"} {
		if strings.Contains(page, p) {
			t.Fatalf("placeholder %s left in the page", p)
		}
	}
	// ART-071's CSP line forbids every fetch, so the page works offline.
	csp := `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:">` + "\n"
	if !strings.Contains(page, csp) {
		t.Fatal("the CSP meta line differs from ART-071's")
	}
	// The ui side of AT-ART-11: the stylesheet and the bundle name no URL and close no script.
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Fatal("the page contains a URL")
	}
	if n := strings.Count(strings.ToLower(page), "</script"); n != 2 {
		t.Fatalf("the page has %d </script, want the template's 2", n)
	}
	if strings.Contains(page, "<!--") {
		t.Fatal("the page contains <!--, which can hide a </script> inside a script element")
	}
}
