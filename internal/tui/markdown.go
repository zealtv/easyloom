package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// A small line-oriented markdown renderer for the preview pane.
//
// It is deliberately not a markdown implementation. It covers what a loom's
// instructions are actually written in — headings, paragraphs, lists, fenced
// code, block quotes, and inline emphasis, code and [[stitch-id]] links — and
// leaves everything else as the text it already is. Tables, images, footnotes
// and HTML are not in a loom's instructions.
//
// Glamour would do this properly and brings goldmark and chroma with it, which
// is a large tree for a preview pane in a viewer. The dependency budget tui
// set is bubbletea, lipgloss and charmbracelet/x/term, and it is spent. This
// is a few hundred lines of pure function instead, which has the side benefit
// of being testable in a way glamour's output is not.
//
// Two rules run through all of it:
//
//   - With colour off the output must still be readable markdown. A marker a
//     colour would have replaced is kept, not silently deleted: `# Heading`
//     without a bold to show for it is still a heading, and a bare `Heading`
//     is a lie about what is in the file.
//   - Wrapping happens over already-styled spans, never over rendered text, so
//     an escape sequence can never be split across two lines. A styled run
//     that crosses a wrap becomes two styled runs.

var (
	fenceRE   = regexp.MustCompile("^(\\s*)(`{3,}|~{3,})(.*)$")
	headingRE = regexp.MustCompile(`^(#{1,6})[ \t]+(.*)$`)
	bulletRE  = regexp.MustCompile(`^([ \t]*)([-*+]|\d+[.)])[ \t]+(.*)$`)
	quoteRE   = regexp.MustCompile(`^([ \t]*)>[ \t]?(.*)$`)
)

// isRule reports a thematic break: three or more of -, * or _, alone on a
// line. Go's regexp has no backreference, and a hand-written check is clearer
// than the alternation that would replace one.
func isRule(line string) bool {
	trimmed := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, line)
	if len(trimmed) < 3 {
		return false
	}
	switch trimmed[0] {
	case '-', '*', '_':
	default:
		return false
	}
	return strings.Count(trimmed, string(trimmed[0])) == len(trimmed)
}

// inlineRules are tried in order at every position, and the earliest match
// wins. Code comes first because a backtick span is a place where `*` means an
// asterisk.
var inlineRules = []struct {
	name string
	re   *regexp.Regexp
}{
	{"code", regexp.MustCompile("`([^`]+)`")},
	{"wikilink", regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)},
	{"strong", regexp.MustCompile(`\*\*([^*]+)\*\*|__([^_]+)__`)},
	{"emphasis", regexp.MustCompile(`\*([^*]+)\*|_([^_]+)_`)},
}

// span is a run of text and how to draw it. id names the style so adjacent
// runs of the same kind can be coalesced into one escape sequence rather than
// one per word.
type span struct {
	text  string
	style lipgloss.Style
	id    string
}

// renderMarkdown turns instructions into styled lines no wider than width.
//
// It is a pure function: text and a width in, lines out. No model, no
// filesystem, no I/O — the preview pane has already read the file.
func renderMarkdown(text string, width int, st styles, colour bool) []string {
	if width < 1 {
		width = 1
	}
	// The trailing newline every well-formed file ends with is a terminator,
	// not a blank line, and drawing it as one wastes a row of a pane that has
	// a dozen.
	normal := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	src := strings.Split(strings.TrimRight(normal, "\n"), "\n")

	out := []string{}
	for i := 0; i < len(src); i++ {
		line := src[i]

		if fence := fenceRE.FindStringSubmatch(line); fence != nil {
			i = appendFence(&out, src, i, fence, width, st)
			continue
		}
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		if isRule(line) {
			out = append(out, st.mdRule.Render(strings.Repeat("─", min(width, 12))))
			continue
		}
		if heading := headingRE.FindStringSubmatch(line); heading != nil {
			out = append(out, headingLines(heading, width, st, colour)...)
			continue
		}
		if quote := quoteRE.FindStringSubmatch(line); quote != nil {
			out = append(out, quoteLines(quote, width, st, colour)...)
			continue
		}
		if bullet := bulletRE.FindStringSubmatch(line); bullet != nil {
			body, next := gather(src, i, bullet[3])
			out = append(out, bulletLines(bullet, body, width, st, colour)...)
			i = next
			continue
		}
		body, next := gather(src, i, line)
		out = append(out, wrap(tokens(inline(body, st, colour)), width, "", "", st.mdBullet)...)
		i = next
	}
	return out
}

// gather folds a block's lazy continuation lines into it, so a paragraph or a
// list item wrapped at 78 columns in the file reflows to the pane rather than
// breaking wherever the author's editor happened to break it.
func gather(src []string, at int, first string) (string, int) {
	body := []string{first}
	i := at
	for i+1 < len(src) {
		next := src[i+1]
		if strings.TrimSpace(next) == "" {
			break
		}
		// Anything that starts a block of its own ends this one.
		if fenceRE.MatchString(next) || headingRE.MatchString(next) ||
			bulletRE.MatchString(next) || quoteRE.MatchString(next) || isRule(next) {
			break
		}
		body = append(body, strings.TrimSpace(next))
		i++
	}
	return strings.Join(body, " "), i
}

// appendFence emits a fenced code block and returns the index of its last
// line. An unterminated fence runs to the end of the document, which is what
// every other renderer does and what a truncated file looks like.
func appendFence(out *[]string, src []string, at int, fence []string, width int, st styles) int {
	marker := fence[2]
	*out = append(*out, st.mdCode.Render(clamp(src[at], width)))

	i := at + 1
	for ; i < len(src); i++ {
		line := src[i]
		if closing := fenceRE.FindStringSubmatch(line); closing != nil &&
			strings.TrimSpace(closing[3]) == "" && closing[2][0] == marker[0] &&
			len(closing[2]) >= len(marker) {
			*out = append(*out, st.mdCode.Render(clamp(line, width)))
			return i
		}
		// The contents are never restyled. A fence is a place where `*` means
		// an asterisk, so nothing in here is scanned for markers — the block
		// gets one style and the text inside it is left exactly as it is.
		for _, piece := range split(line, width) {
			*out = append(*out, st.mdCode.Render(piece))
		}
	}
	return i - 1
}

// headingLines draws a heading. The marker is dropped when a bold can stand in
// for it, and kept when it cannot.
func headingLines(heading []string, width int, st styles, colour bool) []string {
	text := heading[2]
	prefix := ""
	if !colour {
		prefix = heading[1] + " "
	}
	spans := inline(text, st, colour)
	for i := range spans {
		spans[i].style = st.mdHeading.Inherit(spans[i].style)
		spans[i].id = "heading:" + spans[i].id
	}
	return wrap(tokens(spans), width, prefix, strings.Repeat(" ", lipgloss.Width(prefix)), st.mdHeading)
}

// quoteLines draws a block quote: the marker dimmed, the contents rendered.
func quoteLines(quote []string, width int, st styles, colour bool) []string {
	prefix := quote[1] + "> "
	cont := quote[1] + "  "
	if !colour {
		cont = prefix
	}
	return wrap(tokens(inline(quote[2], st, colour)), width, prefix, cont, st.mdQuote)
}

// bulletLines draws a list item, keeping its marker and its indent.
func bulletLines(bullet []string, body string, width int, st styles, colour bool) []string {
	indent := strings.ReplaceAll(bullet[1], "\t", "  ")
	prefix := indent + bullet[2] + " "
	cont := indent + strings.Repeat(" ", lipgloss.Width(bullet[2])+1)
	return wrap(tokens(inline(body, st, colour)), width, prefix, cont, st.mdBullet)
}

// inline splits a line into styled spans.
//
// With colour off every marker is kept and every span is drawn plainly, so the
// output is the markdown that is in the file. With colour on the markers are
// replaced by the style they asked for.
func inline(text string, st styles, colour bool) []span {
	if text == "" {
		return nil
	}
	name, match := earliest(text)
	if match == nil {
		return []span{{text: text}}
	}

	inner := ""
	for i := 2; i < len(match)*2; i += 2 {
		if match[i] >= 0 {
			inner = text[match[i]:match[i+1]]
			break
		}
	}

	var style lipgloss.Style
	switch name {
	case "code":
		style = st.mdInlineCode
	case "wikilink":
		style = st.mdLink
	case "strong":
		style = st.mdStrong
	case "emphasis":
		style = st.mdEmphasis
	}

	shown := inner
	if !colour {
		// The marker is what makes it readable without a colour to replace it.
		shown = text[match[0]:match[1]]
	}

	out := []span{}
	if match[0] > 0 {
		out = append(out, inline(text[:match[0]], st, colour)...)
	}
	out = append(out, span{text: shown, style: style, id: name})
	// The remainder is rescanned rather than parsed: an unclosed marker after
	// this one is left as the text it is.
	return append(out, inline(text[match[1]:], st, colour)...)
}

// earliest returns the first inline match in the line, preferring the rule
// listed first when two start at the same place.
func earliest(text string) (string, []int) {
	best := []int(nil)
	name := ""
	for _, rule := range inlineRules {
		loc := rule.re.FindStringSubmatchIndex(text)
		if loc == nil {
			continue
		}
		if best == nil || loc[0] < best[0] {
			best, name = loc, rule.name
		}
	}
	return name, best
}

// token is a word or a run of spaces, carrying the style of the span it came
// from. Wrapping happens over these, so a line is always built from whole
// styled pieces and an escape sequence is never split.
type token struct {
	text  string
	style lipgloss.Style
	id    string
	space bool
}

func tokens(spans []span) []token {
	out := []token{}
	for _, s := range spans {
		for _, piece := range runs(s.text) {
			out = append(out, token{
				text:  piece,
				style: s.style,
				id:    s.id,
				space: strings.TrimSpace(piece) == "",
			})
		}
	}
	return out
}

// runs splits a string into alternating runs of spaces and non-spaces, so a
// break is only ever taken where there was already a space.
func runs(s string) []string {
	out := []string{}
	start := 0
	var inSpace bool
	for i, r := range s {
		space := r == ' ' || r == '\t'
		if i == 0 {
			inSpace = space
			continue
		}
		if space != inSpace {
			out = append(out, s[start:i])
			start, inSpace = i, space
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// wrap lays tokens out to width, with first before the first line and cont
// before every line after it. Prefixes are drawn in prefixStyle.
//
// A line is built from whole styled runs: pieces sharing a style are gathered
// and rendered together, and nothing is ever rendered and then cut. That is
// what makes it impossible for a wrap to split an escape sequence in half.
func wrap(items []token, width int, first, cont string, prefixStyle lipgloss.Style) []string {
	if width < 1 {
		width = 1
	}
	lines := []string{}

	var line, run strings.Builder
	var runStyle lipgloss.Style
	runID := ""
	inRun := false

	closeRun := func() {
		if !inRun {
			return
		}
		line.WriteString(runStyle.Render(run.String()))
		run.Reset()
		inRun = false
	}

	prefix := first
	room := width - lipgloss.Width(prefix)
	if room < 1 {
		room = 1
	}
	if prefix != "" {
		line.WriteString(prefixStyle.Render(prefix))
	}
	used, started, gap := 0, false, ""

	flush := func() {
		closeRun()
		lines = append(lines, line.String())
		line.Reset()
		prefix = cont
		room = width - lipgloss.Width(prefix)
		if room < 1 {
			room = 1
		}
		if prefix != "" {
			line.WriteString(prefixStyle.Render(prefix))
		}
		used, started, gap = 0, false, ""
	}

	for _, item := range items {
		if item.space {
			if started {
				gap = item.text
			}
			continue
		}
		for _, piece := range split(item.text, room) {
			w := lipgloss.Width(piece)
			space := lipgloss.Width(gap)
			if started && used+space+w > room {
				flush()
				space = 0
			}
			if inRun && runID == item.id {
				// The same kind of text as what is already open: extend the
				// run, space and all, rather than closing and reopening it.
				if started && space > 0 {
					run.WriteString(gap)
					used += space
				}
				run.WriteString(piece)
			} else {
				closeRun()
				if started && space > 0 {
					line.WriteString(gap)
					used += space
				}
				runStyle, runID, inRun = item.style, item.id, true
				run.WriteString(piece)
			}
			used += w
			started, gap = true, ""
		}
	}
	closeRun()
	return append(lines, line.String())
}

// split breaks a word too long for the line into pieces that fit. It measures
// display width rather than bytes: a loom's instructions contain box-drawing
// and other non-ASCII.
func split(word string, width int) []string {
	if width < 1 {
		width = 1
	}
	if lipgloss.Width(word) <= width {
		return []string{word}
	}
	out := []string{}
	var piece strings.Builder
	used := 0
	for _, r := range word {
		w := lipgloss.Width(string(r))
		if used+w > width && piece.Len() > 0 {
			out = append(out, piece.String())
			piece.Reset()
			used = 0
		}
		piece.WriteRune(r)
		used += w
	}
	if piece.Len() > 0 {
		out = append(out, piece.String())
	}
	return out
}

// clamp cuts a line to the width, by display cells.
func clamp(s string, width int) string {
	pieces := split(s, width)
	if len(pieces) == 0 {
		return ""
	}
	return pieces[0]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
