// Copyright 2026 Hamed Yousefi
// SPDX-License-Identifier: MPL-2.0

package ui

import (
	"fmt"
	"io/fs"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// cssRule is one style rule of theme.css; at is the prelude of the enclosing at-rule, "" at the
// top level.
type cssRule struct {
	at, selector string
	decls        []string // "name: value", in source order
}

var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// parseCSS splits css into style rules, one at-rule level deep, which is all theme.css uses.
func parseCSS(t *testing.T, css, at string) []cssRule {
	t.Helper()
	css = cssComment.ReplaceAllString(css, "")
	var rules []cssRule
	for {
		open := strings.IndexByte(css, '{')
		if open < 0 {
			if strings.TrimSpace(css) != "" {
				t.Fatalf("theme.css: text after the last rule: %q", css)
			}
			return rules
		}
		prelude := strings.TrimSpace(css[:open])
		if strings.HasPrefix(prelude, "@") {
			depth, end := 0, -1
			for i := open; i < len(css) && end < 0; i++ {
				switch css[i] {
				case '{':
					depth++
				case '}':
					depth--
					if depth == 0 {
						end = i
					}
				}
			}
			if end < 0 {
				t.Fatalf("theme.css: unclosed %s", prelude)
			}
			rules = append(rules, parseCSS(t, css[open+1:end], prelude)...)
			css = css[end+1:]
			continue
		}
		end := strings.IndexByte(css[open:], '}')
		if end < 0 {
			t.Fatalf("theme.css: unclosed rule %s", prelude)
		}
		var decls []string
		for _, d := range strings.Split(css[open+1:open+end], ";") {
			if d = strings.TrimSpace(d); d != "" {
				decls = append(decls, d)
			}
		}
		rules = append(rules, cssRule{at: at, selector: prelude, decls: decls})
		css = css[open+end+1:]
	}
}

// selectors splits a selector list at the commas outside parentheses.
func selectors(list string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range list {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(list[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(list[start:]))
}

// tokens returns the custom properties of the rule with selector sel inside at-rule at.
func tokens(t *testing.T, rules []cssRule, at, sel string) map[string]string {
	t.Helper()
	for _, r := range rules {
		if r.at == at && r.selector == sel {
			m := map[string]string{}
			for _, d := range r.decls {
				if name, value, ok := strings.Cut(d, ":"); ok && strings.HasPrefix(name, "--fl-") {
					m[strings.TrimSpace(name)] = strings.TrimSpace(value)
				}
			}
			return m
		}
	}
	t.Fatalf("theme.css has no rule %q in %q", sel, at)
	return nil
}

func themeRules(t *testing.T) []cssRule {
	t.Helper()
	css, err := fs.ReadFile(FS, ThemeCSS)
	if err != nil {
		t.Fatal(err)
	}
	return parseCSS(t, string(css), "")
}

const darkMedia = "@media (prefers-color-scheme: dark)"

// documentRules are the selectors allowed outside .fl-run (ART-077): the token blocks (UI-178,
// UI-193) and the page background and margin of a page that holds a run view.
var documentRules = []string{
	`:root`, `:root[data-theme="dark"]`, `:root:not([data-theme="light"])`, `:root:root`,
	`:root:has(.fl-run)`, `:root:has(.fl-run) body`,
}

// scoped matches a selector under .fl-run: the class itself, not a longer name such as .fl-running.
var scoped = regexp.MustCompile(`^\.fl-run($|[ :.\[>])`)

// ART-077: every rule is scoped under .fl-run, except the document rules.
func TestThemeCSSScope(t *testing.T) {
	for _, r := range themeRules(t) {
		for _, s := range selectors(r.selector) {
			if !scoped.MatchString(s) && !slices.Contains(documentRules, s) {
				t.Errorf("selector %q (in %q) is neither under .fl-run nor a document rule", s, r.at)
			}
		}
	}
}

// approved holds UI-179's and UI-191's per-theme values: light, then dark.
var approved = []struct{ name, light, dark string }{
	{"bg", "#fbfcfe", "#0a101c"},
	{"lane-alt", "#f0f3f8", "#0e1625"},
	{"panel", "#f6f8fb", "#151c2b"},
	{"border", "#d8dfe9", "#24304a"},
	{"fg", "#172030", "#f3f0e8"},
	{"muted", "#4a576b", "#b5c0d0"},
	{"send", "#4f77b2", "#8cb0de"},
	{"deliver", "#2f4f80", "#6b92ce"},
	{"drop", "#a81c40", "#f0607a"},
	{"dup", "#4f77b2", "#8cb0de"},
	{"fault", "#ad6a12", "#eba94f"},
	{"violation", "#a81c40", "#f0607a"},
	{"node", "#283648", "#f3f0e8"},
	{"paused", "#5f7698", "#c9d9ef"},
	{"down", "#6b7686", "#8e99a9"},
	{"disk", "#6b5b45", "#b79c6e"},
	{"assert", "#8a6a20", "#e2c98f"},
	{"history", "#44556a", "#d6bf94"},
	{"event", "#6b7686", "#8a95a6"},
	{"other", "#6b7686", "#8e99a9"},
	{"arrow", "#637389", "#7a90b2"},
	{"density", "#5b677a", "#8e99a9"},
	{"select", "#1a55a0", "#8bbefa"},
	{"search", "#8a6400", "#f0d27a"},
	{"focus", "#1a55a0", "#8ec0ff"},
	{"on-accent", "#ffffff", "#08111f"},
	{"group-1", "#4f77b2", "#8cb0de"},
	{"group-2", "#ad6a12", "#eba94f"},
	{"group-3", "#8c6a2e", "#d6bf94"},
	{"group-4", "#a81c40", "#f0607a"},
	{"group-5", "#2f4f80", "#c9d9ef"},
	{"group-6", "#6b7686", "#6b92ce"},
	{"backdrop-1", "#ebeff5", "#070b14"},
	{"backdrop-2", "rgba(80,122,196,.3)", "rgba(80,122,196,.55)"},
	{"backdrop-3", "rgba(228,161,92,.34)", "rgba(228,161,92,.4)"},
	{"backdrop-4", "rgba(196,168,120,.24)", "rgba(236,222,196,.2)"},
	{"grid-dot", "rgba(40,54,72,.1)", "rgba(243,240,232,.07)"},
	{"glass-fill-1", "linear-gradient(160deg, rgba(255,255,255,.84), rgba(250,251,253,.7))", "linear-gradient(160deg, rgba(8,13,26,.64), rgba(8,13,26,.48))"},
	{"glass-fill-2", "linear-gradient(135deg, rgba(255,255,255,.74), rgba(255,255,255,.5))", "linear-gradient(135deg, rgba(50,56,70,.62), rgba(18,24,38,.58))"},
	{"glass-fill-3", "linear-gradient(135deg, rgba(255,255,255,.95), rgba(252,253,254,.92))", "linear-gradient(135deg, rgba(38,46,62,.9), rgba(18,24,38,.88))"},
	{"glass-border", "rgba(40,54,72,.13)", "rgba(255,255,255,.14)"},
	{"glass-highlight", "rgba(255,255,255,.9)", "rgba(255,255,255,.14)"},
	{"glass-shadow-1", "0 4px 16px rgba(30,42,64,.07)", "0 6px 20px rgba(0,0,0,.28)"},
	{"glass-shadow-2", "0 10px 30px rgba(30,42,64,.1)", "0 10px 30px rgba(0,0,0,.35)"},
	{"glass-shadow-3", "0 18px 48px rgba(30,42,64,.18)", "0 18px 50px rgba(0,0,0,.5)"},
	{"accent-glow", "0 0 20px rgba(29,95,173,.34)", "0 0 24px rgba(127,182,247,.45)"},
}

// shared holds the tokens both themes take from :root: UI-191's blur, saturation and radii, and
// UI-179's font stacks, type scale and spacing.
var shared = []struct{ name, value string }{
	{"glass-blur-1", "16px"}, {"glass-blur-2", "16px"}, {"glass-blur-3", "20px"}, {"glass-saturate", "150%"},
	{"radius-1", "8px"}, {"radius-2", "14px"}, {"radius-3", "22px"}, {"radius-pill", "999px"},
	{"font", `"IBM Plex Sans", system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif`},
	{"mono", `"JetBrains Mono", ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace`},
	{"text-1", "11px"}, {"text-2", "12.5px"}, {"text-3", "13.5px"}, {"text-4", "16px"}, {"text-5", "24px"},
	{"space-3", "12px"}, {"space-4", "16px"}, {"space-5", "24px"},
}

// UI-178, UI-179, UI-191, UI-193: the approved values in both theme blocks, the two dark blocks
// identical, and the reduced-transparency fallbacks.
func TestThemeCSSTokens(t *testing.T) {
	rules := themeRules(t)
	light := tokens(t, rules, "", ":root")
	dark := tokens(t, rules, "", `:root[data-theme="dark"]`)
	media := tokens(t, rules, darkMedia, `:root:not([data-theme="light"])`)
	for _, tok := range approved {
		name := "--fl-" + tok.name
		if light[name] != tok.light || dark[name] != tok.dark {
			t.Errorf("%s = %q / %q, want %q / %q", name, light[name], dark[name], tok.light, tok.dark)
		}
	}
	for _, tok := range shared {
		if light["--fl-"+tok.name] != tok.value {
			t.Errorf("--fl-%s = %q, want %q", tok.name, light["--fl-"+tok.name], tok.value)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(dark)) {
		if media[name] != dark[name] {
			t.Errorf("%s: prefers-color-scheme block %q, data-theme block %q", name, media[name], dark[name])
		}
	}
	if len(media) != len(dark) {
		t.Errorf("the dark blocks define %d and %d tokens", len(media), len(dark))
	}
	fallback := map[string]string{
		"--fl-glass-fill-1": "var(--fl-bg)", "--fl-glass-fill-2": "var(--fl-panel)", "--fl-glass-fill-3": "var(--fl-panel)",
		"--fl-glass-blur-1": "0px", "--fl-glass-blur-2": "0px", "--fl-glass-blur-3": "0px", "--fl-glass-saturate": "100%",
	}
	for _, at := range []string{"@media (prefers-reduced-transparency: reduce)", "@supports not (backdrop-filter: blur(1px))"} {
		got := tokens(t, rules, at, ":root:root")
		if fmt.Sprint(got) != fmt.Sprint(fallback) {
			t.Errorf("%s: %v, want %v", at, got, fallback)
		}
	}
}

// designRules are declarations of the approved design that a spec requirement depends on: the focus
// ring and the scroller padding that keeps it whole (UI-175, UI-193), 24 px and larger targets
// (UI-175), the stage's 520 px height and its column that never outgrows it, the cards under the
// stage, side by side while each gets 380 px, the side column beside both with Faults capped at
// 40 % and the Inspector filling it, the Failure message well that scrolls past min(40vh, 18em)
// except on narrow screens, and the 760 px breakpoint (ART-079 items 8, 12 and 13), the side width
// of 304 px, 280 px under 1180 px (UI-195: the approved design, design-system.md), no filter layer
// on solid glass (UI-193) and no motion (UI-181).
var designRules = []struct{ at, selector, decl string }{
	{"", ".fl-run :focus-visible", "outline: 2px solid var(--fl-focus)"},
	{"", ".fl-run :focus-visible", "outline-offset: 2px"},
	{"", ".fl-run .fl-canvas:focus-visible", "outline-offset: -2px"},
	{"", ".fl-run .fl-scroll", "margin: -4px"},
	{"", ".fl-run .fl-scroll", "padding: 4px"},
	{"@media (max-width: 1180px)", ".fl-run .fl-kinds", "margin: -4px"},
	{"@media (max-width: 1180px)", ".fl-run .fl-kinds", "padding: 4px"},
	{"@media (prefers-reduced-transparency: reduce)", ".fl-run .fl-glass", "backdrop-filter: none"},
	{"", ".fl-run button.fl-row", "cursor: pointer"},
	{"", ".fl-run .fl-round", "width: 32px"},
	{"", ".fl-run .fl-round", "height: 32px"},
	{"", ".fl-run .fl-seg button, .fl-run .fl-toggle", "height: 24px"},
	{"", ".fl-run .fl-chip", "height: 26px"},
	{"", ".fl-run .fl-stage", "min-height: 520px"},
	{"", ".fl-run .fl-stage", "grid-template: minmax(0, 1fr) auto / minmax(0, 1fr)"},
	{"", ".fl-run .fl-side", "height: 0"},
	{"", ".fl-run .fl-side", "min-height: 100%"},
	{"", ".fl-run .fl-side", "grid-template-rows: max-content fit-content(40%) minmax(0, 1fr)"},
	{"", ".fl-run .fl-side > .fl-panel:first-child > .fl-well", "max-height: min(40vh, 18em)"},
	{"", ".fl-run .fl-side > .fl-panel:first-child > .fl-well", "overflow: auto"},
	{"", ".fl-run .fl-side > .fl-panel:first-child > .fl-well", "overscroll-behavior: contain"},
	{"", ".fl-run", "--fl-side-w: 304px"},
	{"@media (max-width: 1180px)", ".fl-run", "--fl-side-w: 280px"},
	{"", ".fl-run .fl-body", `grid-template: "stage side" auto "cards side" auto / minmax(0, 1fr) var(--fl-side-w)`},
	{"", ".fl-run .fl-cards", "grid-template-columns: repeat(auto-fit, minmax(min(380px, 100%), 1fr))"},
	{"@media (max-width: 760px)", ".fl-run .fl-body", "display: flex"},
	{"@media (max-width: 760px)", ".fl-run .fl-body", "flex-direction: column"},
	{"@media (max-width: 760px)", ".fl-run .fl-stage", "display: flex"},
	{"@media (max-width: 760px)", ".fl-run .fl-stage", "flex-direction: column"},
	{"@media (max-width: 760px)", ".fl-run .fl-side", "grid-template-rows: none"},
	{"@media (max-width: 760px)", ".fl-run .fl-side > .fl-panel:first-child > .fl-well", "max-height: none"},
	{"@media (max-width: 760px)", ".fl-run .fl-side > .fl-panel:first-child > .fl-well", "overflow: hidden"},
	{"@media (prefers-reduced-motion: reduce)", ".fl-run *, .fl-run ::before, .fl-run ::after", "transition: none !important"},
}

// TestThemeCSSRules checks that theme.css keeps designRules, that the Legend's rows, which do
// nothing, do not show the pointer that the Faults rows show, and that the Inspector fills the side
// column.
func TestThemeCSSRules(t *testing.T) {
	rules := themeRules(t)
	for _, d := range decls(t, rules, "", ".fl-run .fl-row") {
		if strings.HasPrefix(d, "cursor:") {
			t.Errorf(".fl-run .fl-row { %s }: only button.fl-row is clickable", d)
		}
	}
	// The Inspector fills the side column (ART-079 item 8); align-self would shrink it to its content.
	for _, d := range decls(t, rules, "", ".fl-run .fl-side > .fl-panel:last-child") {
		if strings.HasPrefix(d, "align-self:") {
			t.Errorf(".fl-run .fl-side > .fl-panel:last-child { %s }: the Inspector must fill the side column", d)
		}
	}
	for _, want := range designRules {
		found := false
		for _, r := range rules {
			if r.at == want.at && r.selector == want.selector && slices.Contains(r.decls, want.decl) {
				found = true
			}
		}
		if !found {
			t.Errorf("theme.css: %q { %s } in %q is missing", want.selector, want.decl, want.at)
		}
	}
}

// TestThemePalette checks that both themes define every token render.js reads (PALETTE).
func TestThemePalette(t *testing.T) {
	js, err := fs.ReadFile(FS, "static/js/timeline/render.js")
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`(?m)^export const PALETTE = \[(.*)\];$`).FindSubmatch(js)
	if line == nil {
		t.Fatal("render.js has no PALETTE line")
	}
	names := regexp.MustCompile(`"([a-z0-9-]+)"`).FindAllSubmatch(line[1], -1)
	rules := themeRules(t)
	light := tokens(t, rules, "", ":root")
	dark := tokens(t, rules, "", `:root[data-theme="dark"]`)
	for _, n := range names {
		name := "--fl-" + string(n[1])
		if light[name] == "" || dark[name] == "" {
			t.Errorf("PALETTE reads %s, which a theme does not define", name)
		}
	}
	if len(names) < 20 {
		t.Errorf("PALETTE has %d names", len(names))
	}
}

// rgb parses #rrggbb into 0-255 channels.
func rgb(t *testing.T, hex string) [3]float64 {
	t.Helper()
	n, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	if err != nil || len(hex) != 7 {
		t.Fatalf("not a #rrggbb color: %q", hex)
	}
	return [3]float64{float64(n >> 16), float64(n >> 8 & 0xff), float64(n & 0xff)}
}

func linear(c float64) float64 {
	if c /= 255; c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// luminance is WCAG 2.1 relative luminance.
func luminance(c [3]float64) float64 {
	return 0.2126*linear(c[0]) + 0.7152*linear(c[1]) + 0.0722*linear(c[2])
}

func contrast(a, b [3]float64) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

// UI-179: text tokens reach 4.5:1 and non-text tokens 3:1 on the opaque data surfaces of both
// themes, and --fl-on-accent 4.5:1 on --fl-select. The design's contrast report checks the tokens
// on every glass composite (UI-193); this test checks the surfaces that report once missed (see
// surfaceContrast).
func TestThemeContrast(t *testing.T) {
	rules := themeRules(t)
	for _, theme := range []map[string]string{tokens(t, rules, "", ":root"), tokens(t, rules, "", `:root[data-theme="dark"]`)} {
		surfaceContrast(t, rules, theme)
		if r := contrast(rgb(t, theme["--fl-on-accent"]), rgb(t, theme["--fl-select"])); r < 4.5 {
			t.Errorf("--fl-on-accent on --fl-select: %.2f:1, want 4.5:1", r)
		}
		for _, tok := range approved {
			if !strings.HasPrefix(theme["--fl-"+tok.name], "#") || slices.Contains([]string{"bg", "lane-alt", "panel", "border", "on-accent", "backdrop-1"}, tok.name) {
				continue
			}
			need := 3.0
			if tok.name == "fg" || tok.name == "muted" {
				need = 4.5
			}
			for _, surface := range []string{"bg", "lane-alt"} {
				if r := contrast(rgb(t, theme["--fl-"+tok.name]), rgb(t, theme["--fl-"+surface])); r < need {
					t.Errorf("--fl-%s on --fl-%s (theme with --fl-bg %s): %.2f:1, want %.1f:1", tok.name, surface, theme["--fl-bg"], r, need)
				}
			}
		}
	}
}

// decls returns the declarations of the rule with selector sel inside at-rule at.
func decls(t *testing.T, rules []cssRule, at, sel string) []string {
	t.Helper()
	for _, r := range rules {
		if r.at == at && r.selector == sel {
			return r.decls
		}
	}
	t.Fatalf("theme.css has no rule %q in %q", sel, at)
	return nil
}

// value returns the value of property prop in the rule with selector sel inside at-rule at.
func value(t *testing.T, rules []cssRule, at, sel, prop string) string {
	t.Helper()
	for _, d := range decls(t, rules, at, sel) {
		if name, v, ok := strings.Cut(d, ":"); ok && strings.TrimSpace(name) == prop {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("theme.css: %q has no %s in %q", sel, prop, at)
	return ""
}

// color is sRGB channels 0-255 and an alpha.
type color [4]float64

var (
	rgbaColor = regexp.MustCompile(`^rgba\(([\d.]+),([\d.]+),([\d.]+),([\d.]+)\)$`)
	varColor  = regexp.MustCompile(`^var\((--fl-[a-z0-9-]+)\)$`)
	mixColor  = regexp.MustCompile(`^color-mix\(in srgb, var\((--fl-[a-z0-9-]+)\) (\d+)%, transparent\)$`)
)

// parseColor reads the color forms theme.css uses: #rrggbb, rgba(r,g,b,a), var(--fl-x) and
// color-mix(in srgb, var(--fl-x) p%, transparent), resolving tokens in theme.
func parseColor(t *testing.T, theme map[string]string, v string) color {
	t.Helper()
	if m := varColor.FindStringSubmatch(v); m != nil {
		return parseColor(t, theme, theme[m[1]])
	}
	if m := mixColor.FindStringSubmatch(v); m != nil {
		c := parseColor(t, theme, theme[m[1]])
		p, _ := strconv.Atoi(m[2])
		c[3] *= float64(p) / 100
		return c
	}
	if m := rgbaColor.FindStringSubmatch(v); m != nil {
		var c color
		for i := range c {
			c[i], _ = strconv.ParseFloat(m[i+1], 64)
		}
		return c
	}
	c := rgb(t, v)
	return color{c[0], c[1], c[2], 1}
}

// over composites c over the opaque bg per sRGB channel, as browsers blend.
func over(c color, bg [3]float64) [3]float64 {
	return [3]float64{c[0]*c[3] + bg[0]*(1-c[3]), c[1]*c[3] + bg[1]*(1-c[3]), c[2]*c[3] + bg[2]*(1-c[3])}
}

// saturate applies the glass's backdrop-filter saturate(s), in sRGB or in linear light; browsers
// differ.
func saturate(c [3]float64, s float64, linearLight bool) [3]float64 {
	m := [3][3]float64{
		{0.213 + 0.787*s, 0.715 - 0.715*s, 0.072 - 0.072*s},
		{0.213 - 0.213*s, 0.715 + 0.285*s, 0.072 - 0.072*s},
		{0.213 - 0.213*s, 0.715 - 0.715*s, 0.072 + 0.928*s},
	}
	var v, out [3]float64
	for i := range v {
		v[i] = c[i] / 255
		if linearLight {
			v[i] = linear(c[i])
		}
	}
	for i := range out {
		x := min(1, max(0, m[i][0]*v[0]+m[i][1]*v[1]+m[i][2]*v[2]))
		if linearLight && x <= 0.0031308 {
			x *= 12.92
		} else if linearLight {
			x = 1.055*math.Pow(x, 1/2.4) - 0.055
		}
		out[i] = 255 * x
	}
	return out
}

var (
	glowLayer = regexp.MustCompile(`radial-gradient\(circle (\d+)px at (calc\([^)]*\)|\d+px) (calc\([^)]*\)|\d+px), var\((--fl-backdrop-\d)\), transparent 65%\)`)
	glowPos   = regexp.MustCompile(`^(?:calc\((\d+)% ([+-]) )?(\d+)px\)?$`)
)

// glowAt returns the glows of a .fl-run::after background at a w x h viewport: color, center
// and the radius at which the gradient reaches transparent (65 %). The layer overhangs the
// viewport by inset px on each side.
func glowAt(t *testing.T, theme map[string]string, background string, inset, w, h float64) (out []struct {
	c          color
	x, y, stop float64
}) {
	t.Helper()
	pos := func(s string, dim float64) float64 {
		m := glowPos.FindStringSubmatch(s)
		if m == nil {
			t.Fatalf("glow position %q", s)
		}
		pct, _ := strconv.ParseFloat(m[1], 64)
		px, _ := strconv.ParseFloat(m[3], 64)
		if m[2] == "-" {
			px = -px
		}
		return pct/100*(dim+2*inset) + px - inset
	}
	for _, m := range glowLayer.FindAllStringSubmatch(background, -1) {
		r, _ := strconv.ParseFloat(m[1], 64)
		out = append(out, struct {
			c          color
			x, y, stop float64
		}{parseColor(t, theme, theme[m[4]]), pos(m[2], w), pos(m[3], h), 0.65 * r})
	}
	if len(out) < 3 {
		t.Fatalf(".fl-run::after: %d glows in %q", len(out), background)
	}
	return out
}

// backdrop returns the distinct colors of the page backdrop: --fl-backdrop-1 with the dot grid
// averaged over its 1 px in 22 px, and the glows over it, sampled every 8 px on six viewports
// (the narrow ones with the 760 px block's glows). The glows' 40 px blur is left out, which only
// raises their peaks.
func backdrop(t *testing.T, rules []cssRule, theme map[string]string) [][3]float64 {
	t.Helper()
	if bg := value(t, rules, "", ".fl-run::before", "background"); !strings.HasSuffix(bg, "0 0 / 22px 22px") {
		t.Fatalf(".fl-run::before: a dot grid of %q, want 22 px", bg)
	}
	b1, dot := parseColor(t, theme, theme["--fl-backdrop-1"]), parseColor(t, theme, theme["--fl-grid-dot"])
	dot[3] *= math.Pi / (22 * 22)
	base := over(dot, [3]float64{b1[0], b1[1], b1[2]})
	inset, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(value(t, rules, "", ".fl-run::after", "inset"), "-"), "px"), 64)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[[3]int]bool{}
	var out [][3]float64
	for _, vp := range [][2]float64{{390, 844}, {768, 1024}, {1024, 768}, {1440, 900}, {1920, 1080}, {2560, 1440}} {
		background := value(t, rules, "", ".fl-run::after", "background")
		if vp[0] <= 760 {
			background = value(t, rules, "@media (max-width: 760px)", ".fl-run::after", "background")
		}
		glows := glowAt(t, theme, background, inset, vp[0], vp[1])
		for y := 0.0; y <= vp[1]; y += 8 {
			for x := 0.0; x <= vp[0]; x += 8 {
				c := base
				for _, g := range glows {
					if a := g.c[3] * max(0, 1-math.Hypot(x-g.x, y-g.y)/g.stop); a > 0 {
						c = over(color{g.c[0], g.c[1], g.c[2], a}, c)
					}
				}
				key := [3]int{int(math.Round(c[0])), int(math.Round(c[1])), int(math.Round(c[2]))}
				if !seen[key] {
					seen[key] = true
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// glass returns what glass level n shows over each backdrop color: each stop of
// --fl-glass-fill-n over the color as it is and saturated both ways, and the solid fill of the
// reduced-transparency fallback.
func glass(t *testing.T, rules []cssRule, theme map[string]string, points [][3]float64, n string) [][3]float64 {
	t.Helper()
	sat, err := strconv.ParseFloat(strings.TrimSuffix(theme["--fl-glass-saturate"], "%"), 64)
	if err != nil {
		t.Fatal(err)
	}
	var stops []color
	for _, s := range regexp.MustCompile(`rgba\([^)]*\)`).FindAllString(theme["--fl-glass-fill-"+n], -1) {
		stops = append(stops, parseColor(t, theme, s))
	}
	if len(stops) != 2 {
		t.Fatalf("--fl-glass-fill-%s = %q, want a two-stop gradient", n, theme["--fl-glass-fill-"+n])
	}
	solid := parseColor(t, theme, tokens(t, rules, "@media (prefers-reduced-transparency: reduce)", ":root:root")["--fl-glass-fill-"+n])
	out := [][3]float64{{solid[0], solid[1], solid[2]}}
	for _, p := range points {
		for _, v := range [][3]float64{p, saturate(p, sat/100, false), saturate(p, sat/100, true)} {
			for _, s := range stops {
				out = append(out, over(s, v))
			}
		}
	}
	return out
}

// surfaceContrast checks the surfaces of the owner's amendments of 2026-10-08 (plan 1.4
// Deviations) in theme: the title row's run chips put --fl-fg text on the chip fill straight over
// the backdrop (4.5:1, UI-179); the pressed Time and Sequence ring on the smoked card, the off
// kind chip's dashed edge on the control panel's glass and the overview's view segment on its
// track reach 3:1 against what they touch (UI-193).
func surfaceContrast(t *testing.T, rules []cssRule, own map[string]string) {
	t.Helper()
	theme := tokens(t, rules, "", ":root") // the shared tokens, then the theme's own
	maps.Copy(theme, own)
	run := tokens(t, rules, "", ".fl-run")
	col := func(v string) color { return parseColor(t, theme, v) }
	points := backdrop(t, rules, theme)
	smoked, panel := glass(t, rules, theme, points, "1"), glass(t, rules, theme, points, "2")
	worst := func(bgs [][3]float64, ratio func([3]float64) float64) float64 {
		w := math.Inf(1)
		for _, bg := range bgs {
			w = min(w, ratio(bg))
		}
		return w
	}
	check := func(name string, need, got float64) {
		t.Logf("%s, theme with --fl-bg %s: %.2f:1 at worst over %d backdrop colors", name, theme["--fl-bg"], got, len(points))
		if got < need {
			t.Errorf("%s (theme with --fl-bg %s): %.2f:1, want %.1f:1", name, theme["--fl-bg"], got, need)
		}
	}
	chipText, chip := col(value(t, rules, "", ".fl-run .fl-head .fl-pill", "color")), col(run["--fl-chip"])
	check("title-row chip text on the backdrop", 4.5, worst(points, func(bg [3]float64) float64 {
		fill := over(chip, bg)
		return contrast(over(chipText, fill), fill)
	}))
	on, ring := col(run["--fl-on"]), col(run["--fl-on-line"])
	check("pressed Time or Sequence ring", 3, worst(smoked, func(bg [3]float64) float64 {
		seg := over(chip, bg)
		pressed := over(on, seg)
		edge := over(ring, pressed)
		return min(contrast(edge, pressed), contrast(edge, seg))
	}))
	dashed := col(value(t, rules, "", `.fl-run .fl-chip[aria-pressed="false"]`, "border-color"))
	check("off kind chip's dashed edge", 3, worst(panel, func(bg [3]float64) float64 {
		return contrast(over(dashed, bg), bg)
	}))
	track, seg := col(run["--fl-line"]), col(run["--fl-ov-view"])
	check("overview view segment on its track", 3, worst(panel, func(bg [3]float64) float64 {
		tr := over(track, bg)
		return contrast(over(seg, tr), tr)
	}))
}

// Color-vision simulation matrices for linear RGB (Machado, Oliveira and Fernandes 2009,
// severity 1).
var (
	protanopia   = [3][3]float64{{0.152286, 1.052583, -0.204868}, {0.114503, 0.786281, 0.099216}, {-0.003882, -0.048116, 1.051998}}
	deuteranopia = [3][3]float64{{0.367322, 0.860646, -0.227968}, {0.280085, 0.672501, 0.047413}, {-0.01182, 0.04294, 0.968881}}
)

// lab converts sRGB channels, after the matrix m on linear RGB, to CIELAB (D65).
func lab(c [3]float64, m [3][3]float64) [3]float64 {
	l := [3]float64{linear(c[0]), linear(c[1]), linear(c[2])}
	var s [3]float64
	for i := range s {
		s[i] = min(1, max(0, m[i][0]*l[0]+m[i][1]*l[1]+m[i][2]*l[2]))
	}
	x := (0.4124*s[0] + 0.3576*s[1] + 0.1805*s[2]) / 0.95047
	y := 0.2126*s[0] + 0.7152*s[1] + 0.0722*s[2]
	z := (0.0193*s[0] + 0.1192*s[1] + 0.9505*s[2]) / 1.08883
	f := func(v float64) float64 {
		if v > 0.008856 {
			return math.Cbrt(v)
		}
		return 7.787*v + 16.0/116
	}
	return [3]float64{116*f(y) - 16, 500 * (f(x) - f(y)), 200 * (f(y) - f(z))}
}

// deltaE00 is the CIEDE2000 color difference.
func deltaE00(p, q [3]float64) float64 {
	rad := math.Pi / 180
	c7 := func(c float64) float64 { return math.Sqrt(math.Pow(c, 7) / (math.Pow(c, 7) + math.Pow(25, 7))) }
	g := 0.5 * (1 - c7((math.Hypot(p[1], p[2])+math.Hypot(q[1], q[2]))/2))
	a1, a2 := p[1]*(1+g), q[1]*(1+g)
	c1, c2 := math.Hypot(a1, p[2]), math.Hypot(a2, q[2])
	hue := func(a, b float64) float64 {
		if a == 0 && b == 0 {
			return 0
		}
		return math.Mod(math.Atan2(b, a)/rad+360, 360)
	}
	h1, h2 := hue(a1, p[2]), hue(a2, q[2])
	dh, hm := 0.0, h1+h2
	if c1*c2 != 0 {
		dh = h2 - h1
		if dh > 180 {
			dh -= 360
		} else if dh < -180 {
			dh += 360
		}
		if math.Abs(h1-h2) > 180 {
			if h1+h2 < 360 {
				hm += 360
			} else {
				hm -= 360
			}
		}
		hm /= 2
	}
	dH := 2 * math.Sqrt(c1*c2) * math.Sin(dh/2*rad)
	lm, cm := (p[0]+q[0])/2, (c1+c2)/2
	tt := 1 - 0.17*math.Cos((hm-30)*rad) + 0.24*math.Cos(2*hm*rad) + 0.32*math.Cos((3*hm+6)*rad) - 0.2*math.Cos((4*hm-63)*rad)
	sl := 1 + 0.015*(lm-50)*(lm-50)/math.Sqrt(20+(lm-50)*(lm-50))
	sc, sh := 1+0.045*cm, 1+0.015*cm*tt
	e := (hm - 275) / 25
	rt := -math.Sin(2*30*math.Exp(-e*e)*rad) * 2 * c7(cm)
	dl, dc, dhh := (q[0]-p[0])/sl, (c2-c1)/sc, dH/sh
	return math.Sqrt(dl*dl + dc*dc + dhh*dhh + rt*dc*dhh)
}

// UI-199: the failure red and the fault amber stay at least ΔE00 10 apart under protanopia and
// deuteranopia, in both themes.
func TestThemeColorVision(t *testing.T) {
	rules := themeRules(t)
	for _, theme := range []map[string]string{tokens(t, rules, "", ":root"), tokens(t, rules, "", `:root[data-theme="dark"]`)} {
		for _, red := range []string{"--fl-violation", "--fl-drop"} {
			for _, sim := range []struct {
				name string
				m    [3][3]float64
			}{{"protanopia", protanopia}, {"deuteranopia", deuteranopia}} {
				a, b := rgb(t, theme[red]), rgb(t, theme["--fl-fault"])
				if d := deltaE00(lab(a, sim.m), lab(b, sim.m)); d < 10 {
					t.Errorf("%s %s and --fl-fault %s: ΔE00 %.1f under %s, want at least 10", red, theme[red], theme["--fl-fault"], d, sim.name)
				}
			}
		}
	}
}

// UI-050: every file under static/ fits 64 KiB, all CSS 32 KiB, and all of static/ 512 KiB.
func TestStaticBudgets(t *testing.T) {
	var all, css int
	err := fs.WalkDir(FS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(FS, p)
		if err != nil {
			return err
		}
		if len(b) > 64<<10 {
			t.Errorf("%s is %d bytes, budget 65536", p, len(b))
		}
		all += len(b)
		if strings.HasSuffix(p, ".css") {
			css += len(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if css > 32<<10 || all > 512<<10 {
		t.Errorf("CSS %d bytes (budget 32768), static/ %d bytes (budget 524288)", css, all)
	}
}
