package artifact

import (
	"path/filepath"
	"strings"
	"testing"
)

// AT-ART-01
func TestSanitize(t *testing.T) {
	cases := []struct {
		fn       func(string) string
		name     string
		in, want string
	}{
		{SanitizePackage, "pkg", "github.com/acme/kv", "github.com_acme_kv"},
		{SanitizePackage, "pkg", "command-line-arguments", "command-line-arguments"},
		{SanitizePackage, "pkg", "example.com/a b/ü", "example.com_a_b___"},
		{SanitizePackage, "pkg", "", "_"},
		{SanitizePackage, "pkg", "..", "_.."},
		{SanitizeTest, "test", "TestKV", "TestKV"},
		{SanitizeTest, "test", "TestKV/raft_3_nodes", "TestKV__raft_3_nodes"},
		{SanitizeTest, "test", "TestX/a:b*c?", "TestX__a_b_c_"},
		{SanitizeTest, "test", "TestX/case#01", "TestX__case#01"},
		{SanitizeTest, "test", "TestLong/" + strings.Repeat("abcdefghij", 11), "TestLong__abcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabcdefghijabc-72ac6d5da30ea3f3"},
		// Only "", "." and ".." are reserved.
		{SanitizePackage, "pkg", ".", "_."},
		{SanitizePackage, "pkg", "...", "..."},
		// The cap applies when out passes 100 bytes; "__" makes out longer than the input.
		{SanitizeTest, "test", "T/" + strings.Repeat("a", 97), "T__" + strings.Repeat("a", 97)},
		{SanitizeTest, "test", "T/" + strings.Repeat("a", 98), "T__" + strings.Repeat("a", 80) + "-e2831902091d0efa"},
		// The cut falls inside the "___" of 日 (bytes of out, not runes), and the hash keeps its
		// leading zero.
		{SanitizeTest, "test", "TestCut/" + strings.Repeat("a", 73) + "日本" + strings.Repeat("b", 16), "TestCut__" + strings.Repeat("a", 73) + "_-05abd7da7bab7067"},
	}
	for _, c := range cases {
		if got := c.fn(c.in); got != c.want {
			t.Errorf("Sanitize %s(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
	if got := Dir("/r", "github.com/acme/kv", "TestKV/a b", 0x5e1f9a2c4b7d3e80); got != filepath.FromSlash("/r/github.com_acme_kv/TestKV__a_b/5e1f9a2c4b7d3e80") {
		t.Errorf("Dir = %q", got)
	}
}

// ART-002: each byte on its own, between two kept bytes so that no other rule applies.
func TestSanitizeBytes(t *testing.T) {
	const kept = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-_+=@,~#"
	for c := 0; c < 256; c++ {
		in := string([]byte{'x', byte(c), 'x'})
		pkg, test := "x_x", "x_x"
		switch {
		case strings.IndexByte(kept, byte(c)) >= 0:
			pkg, test = in, in
		case c == '/':
			test = "x__x"
		}
		if got := SanitizePackage(in); got != pkg {
			t.Errorf("SanitizePackage(%q) = %q, want %q", in, got, pkg)
		}
		if got := SanitizeTest(in); got != test {
			t.Errorf("SanitizeTest(%q) = %q, want %q", in, got, test)
		}
	}
}

// ART-001: filepath.Join cleans the path, and the seed is always 16 hex digits.
func TestDir(t *testing.T) {
	cases := []struct {
		root, pkg, test string
		seed            uint64
		want            string
	}{
		{"/r/", "a/b", "T/c", 1, "/r/a_b/T__c/0000000000000001"},
		{"", "", "", 0, "_/_/0000000000000000"},
	}
	for _, c := range cases {
		if got := Dir(c.root, c.pkg, c.test, c.seed); got != filepath.FromSlash(c.want) {
			t.Errorf("Dir(%q, %q, %q, %#x) = %q, want %q", c.root, c.pkg, c.test, c.seed, got, filepath.FromSlash(c.want))
		}
	}
}
