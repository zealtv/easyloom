package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// escapes finds an ANSI sequence, so a test can check that one is present, and
// that none has been cut in half by a wrap.
var escapes = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain strips styling, leaving what a reader would see the shape of.
func plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = escapes.ReplaceAllString(line, "")
	}
	return out
}

func colourStyles() styles { return newStyles(true) }
func plainStyles() styles  { return newStyles(false) }

// With colour on, the marker is dropped: the style stands in for it.
//
// Whether an escape sequence actually reaches the terminal is lipgloss's
// business and depends on the colour profile it detects, which under `go test`
// is none. What this renderer decides is which text to keep, and that is what
// is asserted here; TestColourStylesAreNotPlainOnes covers the other half.
func TestTheMarkerIsDroppedWhenColourIsOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"heading", "# Markdown in the preview", "Markdown in the preview"},
		{"deep heading", "### What gets styled", "What gets styled"},
		{"bold", "the **dependency budget** is spent", "the dependency budget is spent"},
		{"italic", "an *asterisk* here", "an asterisk here"},
		{"code", "`renderPreview` prints it", "renderPreview prints it"},
		{"wikilink", "settle it on [[tui-markdown-preview]]", "settle it on tui-markdown-preview"},
		{"bullet keeps its marker", "- headings and lists", "- headings and lists"},
		{"numbered bullet", "1. first", "1. first"},
		{"indent is kept", "  * nested", "  * nested"},
		{"quote marker is kept", "> parked for now", "> parked for now"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderMarkdown(tc.in, 60, colourStyles(), true)
			if len(got) != 1 {
				t.Fatalf("renderMarkdown = %q, want one line", got)
			}
			if text := plain(got)[0]; text != tc.want {
				t.Errorf("text = %q, want %q", text, tc.want)
			}
		})
	}
}

// With colour off there is nothing to replace a marker, so it stays. A bare
// "Heading" would be a lie about what is in the file.
func TestMarkersAreKeptWhenColourIsOff(t *testing.T) {
	for _, in := range []string{
		"# Markdown in the preview",
		"### What gets styled",
		"the **dependency budget** is spent",
		"an *asterisk* here",
		"`renderPreview` prints it",
		"settle it on [[tui-markdown-preview]]",
		"- a list item",
		"> a quote",
	} {
		got := renderMarkdown(in, 60, plainStyles(), false)
		if len(got) != 1 || got[0] != in {
			t.Errorf("renderMarkdown(%q) = %q, want it unchanged", in, got)
		}
	}
}

// The other half of the contract: with colour on there is a style to stand in
// for the marker that was dropped, and with colour off there is not — which is
// exactly why the markers are kept there.
func TestColourStylesAreNotPlainOnes(t *testing.T) {
	on, off := newStyles(true), newStyles(false)

	if !on.mdHeading.GetBold() || !on.mdStrong.GetBold() || !on.mdEmphasis.GetItalic() {
		t.Error("headings and emphasis are not styled with colour on")
	}
	for name, style := range map[string]lipgloss.Style{
		"mdInlineCode": on.mdInlineCode,
		"mdCode":       on.mdCode,
		"mdBullet":     on.mdBullet,
		"mdQuote":      on.mdQuote,
		"mdLink":       on.mdLink,
	} {
		if style.GetForeground() == (lipgloss.NoColor{}) {
			t.Errorf("%s has no colour with colour on", name)
		}
	}
	for name, style := range map[string]lipgloss.Style{
		"mdHeading": off.mdHeading, "mdStrong": off.mdStrong, "mdEmphasis": off.mdEmphasis,
		"mdInlineCode": off.mdInlineCode, "mdCode": off.mdCode, "mdBullet": off.mdBullet,
		"mdQuote": off.mdQuote, "mdLink": off.mdLink,
	} {
		if style.GetBold() || style.GetItalic() || style.GetForeground() != (lipgloss.NoColor{}) {
			t.Errorf("%s is styled with colour off", name)
		}
	}
}

func TestNothingIsStyledWithColourOff(t *testing.T) {
	got := renderMarkdown("# Heading\n\nSome **bold** and `code`.\n", 40, plainStyles(), false)
	for _, line := range got {
		if escapes.MatchString(line) {
			t.Errorf("an escape sequence survived NO_COLOR: %q", line)
		}
	}
}

// A fence is a place where * means an asterisk.
func TestFencedCodeIsNeverRestyled(t *testing.T) {
	in := "```sh\n" +
		"grep -r '**not bold**' .\n" +
		"# not a heading\n" +
		"```\n"
	got := plain(renderMarkdown(in, 60, colourStyles(), true))

	want := []string{"```sh", "grep -r '**not bold**' .", "# not a heading", "```"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("fenced block = %q, want %q", got, want)
	}
}

func TestAnUnclosedFenceRunsToTheEnd(t *testing.T) {
	got := plain(renderMarkdown("```\none\ntwo\n", 60, colourStyles(), true))
	want := []string{"```", "one", "two"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("unclosed fence = %q, want %q", got, want)
	}
}

// Wrapping is on display width, and a style must never be cut in half by it.
func TestWrappingRespectsWidthAndNeverSplitsAStyle(t *testing.T) {
	in := "The **dependency budget** that [[tui]] set is bubbletea, lipgloss and " +
		"`charmbracelet/x/term`, and it is spent for a preview pane."

	for _, width := range []int{80, 40, 24, 12, 3, 1} {
		got := renderMarkdown(in, width, colourStyles(), true)
		for _, line := range got {
			if w := lipgloss.Width(escapes.ReplaceAllString(line, "")); w > width {
				t.Errorf("width %d: line is %d cells wide: %q", width, w, line)
			}
			// Every escape sequence on the line is whole. Wrapping happens
			// over spans and each piece is rendered as a unit, so a sequence
			// cannot be split — this checks the property holds wherever
			// lipgloss's profile puts real escapes in the output.
			for _, found := range escapes.FindAllString(line, -1) {
				if !strings.HasSuffix(found, "m") {
					t.Errorf("width %d: truncated escape %q", width, found)
				}
			}
			if strings.Count(line, "\x1b") != len(escapes.FindAllString(line, -1)) {
				t.Errorf("width %d: an escape was cut in half: %q", width, line)
			}
		}
		// Nothing is lost to the wrap.
		joined := strings.Join(plain(got), " ")
		for _, word := range []string{"dependency", "bubbletea", "lipgloss", "spent"} {
			if !strings.Contains(strings.ReplaceAll(joined, " ", ""), word) &&
				!strings.Contains(joined, word) {
				t.Errorf("width %d: %q went missing:\n%s", width, word, joined)
			}
		}
	}
}

// The instructions in this loom contain box-drawing and other non-ASCII, and a
// wrap that counts bytes would break them.
func TestWrappingCountsCellsNotBytes(t *testing.T) {
	got := renderMarkdown("┌────────┐ and — an em dash — plus … more", 20, plainStyles(), false)
	for _, line := range got {
		if w := lipgloss.Width(line); w > 20 {
			t.Errorf("line is %d cells wide: %q", w, line)
		}
	}
}

// A list item wrapped at 78 columns in the file reflows to the pane, and its
// continuation lines line up under the text rather than under the bullet.
func TestListContinuationLinesAreIndentedUnderTheText(t *testing.T) {
	in := "* **fenced code blocks** — dimmed or tinted as a block, contents never\n" +
		"  restyled. A fence is a place where an asterisk means an asterisk.\n"
	got := plain(renderMarkdown(in, 40, colourStyles(), true))

	if len(got) < 2 {
		t.Fatalf("expected the item to wrap: %q", got)
	}
	if !strings.HasPrefix(got[0], "* ") {
		t.Errorf("first line lost its bullet: %q", got[0])
	}
	for _, line := range got[1:] {
		if !strings.HasPrefix(line, "  ") {
			t.Errorf("continuation is not indented under the text: %q", line)
		}
		if strings.HasPrefix(strings.TrimSpace(line), "* ") {
			t.Errorf("continuation grew a second bullet: %q", line)
		}
	}
}

func TestBlankLinesSurvive(t *testing.T) {
	got := plain(renderMarkdown("one\n\ntwo\n", 40, colourStyles(), true))
	if strings.Join(got, "|") != "one||two" {
		t.Errorf("got %q, want one, blank, two", got)
	}
}

// Malformed markdown is normal input. None of this should panic, and none of
// it should disappear.
func TestMalformedMarkdownRendersSomethingReasonable(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"heading with no space", "#nospace", "#nospace"},
		{"stray asterisk", "a * lonely asterisk", "a * lonely asterisk"},
		{"unclosed bold", "**never closed", "**never closed"},
		{"unclosed code", "a `backtick that never ends", "a `backtick that never ends"},
		{"empty wikilink", "[[]]", "[[]]"},
		{"unclosed wikilink", "[[dangling", "[[dangling"},
		{"empty document", "", ""},
		{"a lone marker pair", "**", "**"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := plain(renderMarkdown(tc.in, 60, colourStyles(), true))
			if strings.Join(got, "\n") != tc.want {
				t.Errorf("renderMarkdown(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRendererNeverPanics(t *testing.T) {
	// Every awkward shape at every awkward width.
	inputs := []string{
		"", "\n", "\n\n\n", "#", "###### ", "- ", "> ", "```", "```\n```", "~~~\n~~~",
		"***", "___", "---", "* * *", "[[", "]]", "`", "**", "\t\ttabbed",
		"a\rb", "\r\n", strings.Repeat("x", 500), strings.Repeat("* ", 200),
		"# " + strings.Repeat("héading ", 40),
	}
	for _, in := range inputs {
		for _, width := range []int{-5, 0, 1, 2, 7, 40, 200} {
			for _, colour := range []bool{true, false} {
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("panic on %q at width %d (colour %v): %v", in, width, colour, r)
						}
					}()
					renderMarkdown(in, width, newStyles(colour), colour)
				}()
			}
		}
	}
}

// The renderer takes text and a width and returns lines. It reads nothing and
// keeps nothing, which is what makes the table above cheap.
func TestRendererIsPure(t *testing.T) {
	in := "# heading\n\n- item **one**\n- item `two`\n\n> quoted\n"
	first := renderMarkdown(in, 30, colourStyles(), true)
	second := renderMarkdown(in, 30, colourStyles(), true)
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Error("two calls with the same input disagreed")
	}
}
