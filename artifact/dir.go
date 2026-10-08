package artifact

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Dir returns filepath.Join(root, SanitizePackage(pkg), SanitizeTest(test), fmt.Sprintf("%016x", seed)).
func Dir(root, pkg, test string, seed uint64) string {
	return filepath.Join(root, SanitizePackage(pkg), SanitizeTest(test), fmt.Sprintf("%016x", seed))
}

// SanitizePackage maps an import path to one path element ('/' becomes '_').
func SanitizePackage(pkg string) string { return sanitize(pkg, "_") }

// SanitizeTest maps a test name to one path element ('/' becomes "__").
func SanitizeTest(name string) string { return sanitize(name, "__") }

// sanitize implements ART-002.
func sanitize(s, rep string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', strings.IndexByte(".-_+=@,~#", c) >= 0:
			b.WriteByte(c)
		case c == '/':
			b.WriteString(rep)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		out = "_" + out
	}
	if len(out) > 100 {
		out = out[:83] + "-" + fmt.Sprintf("%016x", fnv1a64(s))
	}
	return out
}

// fnv1a64 is FNV-1a 64 over the bytes of s.
func fnv1a64(s string) uint64 {
	h := uint64(0xcbf29ce484222325)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 0x100000001b3
	}
	return h
}
