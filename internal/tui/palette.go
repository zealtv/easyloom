package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The command palette: every command, found by typing, run by enter.
//
// The commands that change a stitch's state live here and nowhere else. A
// TUI in one tmux pane receives whatever is typed while it has focus, and a
// sentence meant for the pane beside it used to claim, tie and queue things.
// So those take three deliberate steps — open, find, run — and the palette
// selects nothing until the reader has typed or moved, which makes ":" then
// enter do nothing at all. What stays on a bare key only moves around: the
// selection, the scroll, the view and the folds. Refreshing, switching looms
// and quitting are here too — a stray q closing the program is as much a
// surprise as a stray c claiming a stitch — with ctrl+c left as the chord
// that always quits.
//
// Every command is listed, the ones with keys too, with their key beside
// them, so the palette is also the answer to "what can I do here".

// paletteKey opens the palette.
const paletteKey = ":"

// paletteRows is how many commands the palette shows at once. It does not
// shrink as the list filters, so the box stays still under the typing.
const paletteRows = 10

// palette is the open palette's state.
type palette struct {
	query string
	// cursor indexes the filtered commands, or is -1 before the reader has
	// typed or moved: nothing is selected, so enter runs nothing.
	cursor int
}

// command is one entry in the palette.
type command struct {
	name string
	// words are other names it can be found by.
	words string
	glyph string
	hue   lipgloss.Style
	// target is the stitch it acts on: the selected one, named once in the
	// palette's rule rather than on each row.
	target string
	// key is the bare key that also runs it, if there is one.
	key string
	// disabled says why it cannot run here, or is empty.
	disabled string
	run      func(Model) (Model, tea.Cmd)
}

// commands lists what can be done from here, most specific first: the
// selected stitch, the queue, then the views and the program.
func (m Model) commands() []command {
	s := m.styles
	var out []command
	readOnly := ""
	if m.mutator == nil {
		readOnly = "this loom is open read-only"
	}
	mut := func(key string) func(Model) (Model, tea.Cmd) {
		return func(m Model) (Model, tea.Cmd) { return m.mutate(key) }
	}

	if st := m.selectedStitch(); st != nil {
		id := st.ID
		if len(st.Dependencies) > 0 {
			name := "show dependencies"
			if m.dependencies {
				name = "show instructions"
			}
			out = append(out, command{name: name, words: "preview needs requirements details body", glyph: "▤", hue: s.soft,
				target: id, run: func(m Model) (Model, tea.Cmd) {
					m.dependencies = !m.dependencies
					m.scroll = 0
					return m, nil
				}})
		}
		out = append(out,
			command{name: "claim", words: "start take begin stitch", glyph: "◐", hue: s.stitching,
				target: id, disabled: readOnly, run: mut("c")},
			command{name: "tie off", words: "finish done complete", glyph: "✓", hue: s.ready,
				target: id, disabled: readOnly, run: mut("t")},
		)
		if m.selectedHasSuffix("waiting") {
			out = append(out, command{name: "unpark", words: "resume unwait", glyph: "‖", hue: s.waiting,
				target: id, disabled: readOnly, run: mut("w")})
		} else {
			out = append(out, command{name: "park", words: "wait pause", glyph: "‖", hue: s.waiting,
				target: id, disabled: readOnly, run: mut("w")})
		}
		if m.selectedHasSuffix("tending") {
			out = append(out, command{name: "release", words: "untend steward", glyph: "◇", hue: s.tending,
				target: id, disabled: readOnly, run: mut("T")})
		} else {
			out = append(out, command{name: "tend", words: "steward", glyph: "◆", hue: s.tending,
				target: id, disabled: readOnly, run: mut("T")})
		}
		out = append(out, command{name: "drop…", words: "abandon cancel", glyph: "✗", hue: s.warn,
			target: id, disabled: readOnly, run: mut("d")})
		if len(m.unqueuedBeneath(id)) > 0 {
			out = append(out, command{name: "queue with children", words: "add enqueue thread subtree descendants", glyph: "+", hue: s.queue,
				target: id, disabled: readOnly, run: mut("Q")})
		} else if m.isQueued(id) {
			out = append(out, command{name: "unqueue", words: "remove dequeue", glyph: "−", hue: s.queue,
				target: id, disabled: readOnly, run: mut("Q")})
		} else {
			out = append(out, command{name: "queue", words: "add enqueue", glyph: "+", hue: s.queue,
				target: id, disabled: readOnly, run: mut("Q")})
		}
		out = append(out,
			command{name: "edit instructions", words: "open editor", glyph: "✎", hue: s.soft,
				target: id, key: "enter", run: func(m Model) (Model, tea.Cmd) {
					next, cmd := m.edit()
					return next.(Model), cmd
				}},
			command{name: "open folder", words: "finder reveal files directory explorer show", glyph: "▤", hue: s.soft,
				target: id, run: func(m Model) (Model, tea.Cmd) { return m.revealStitch() }},
			command{name: "move up the queue", words: "raise earlier", glyph: "↑", hue: s.queue,
				target: id, disabled: readOnly, run: mut("K")},
			command{name: "move down the queue", words: "lower later", glyph: "↓", hue: s.queue,
				target: id, disabled: readOnly, run: mut("J")},
		)
	}
	out = append(out, command{name: "undo queue move", words: "revert back", glyph: "↶", hue: s.queue,
		disabled: readOnly, run: mut("u")})

	for v := view(0); v < viewCount; v++ {
		if v == m.view {
			continue
		}
		key := ""
		if v == m.view.next() {
			key = "tab"
		}
		target := v
		out = append(out, command{name: "show " + v.name(), words: "view pane go", glyph: "→", hue: s.soft,
			key: key, run: func(m Model) (Model, tea.Cmd) { return m.showView(target), nil }})
	}
	out = append(out, command{name: "open loom folder", words: "finder reveal files directory explorer show root", glyph: "▤", hue: s.soft,
		run: func(m Model) (Model, tea.Cmd) { return m.revealLoom() }})
	out = append(out, command{name: "refresh", words: "reload snapshot", glyph: "↻", hue: s.soft,
		run: func(m Model) (Model, tea.Cmd) { return m.startRefresh(), nil }})
	if m.set != nil {
		out = append(out, command{name: "switch loom", words: "looms picker open", glyph: "⇄", hue: s.soft,
			run: func(m Model) (Model, tea.Cmd) { return m.openPicker(), nil }})
	}
	out = append(out, command{name: "quit", words: "exit close", glyph: "×", hue: s.soft,
		key: "ctrl+c", run: func(m Model) (Model, tea.Cmd) { return m, tea.Quit }})
	return out
}

// showView switches straight to a view, the way tab does one step at a time.
func (m Model) showView(v view) Model {
	for m.view != v {
		m = m.switchView()
	}
	return m
}

// match is a command that matches the query, with where.
type match struct {
	command
	score int
	// hits are the rune indexes of the name the query matched, to light.
	hits []int
}

// filtered is the commands the query matches, best first. An empty query
// matches everything, in the order commands lists it.
func (m Model) filtered() []match {
	var out []match
	query := ""
	if m.palette != nil {
		query = m.palette.query
	}
	for _, c := range m.commands() {
		score, hits, ok := fuzzy(query, c.name)
		if !ok {
			// The other names count, but less than the one on screen.
			if score, _, ok = fuzzy(query, c.words); !ok {
				continue
			}
			score -= 20
			hits = nil
		}
		out = append(out, match{command: c, score: score, hits: hits})
	}
	if query != "" {
		sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	}
	return out
}

// fuzzy matches query against text as a subsequence, ignoring case and the
// query's spaces. It scores a match at the start of a word, and one right
// after the last, above one in the middle of nowhere — so "tie" finds "tie
// off" before "edit instructions".
func fuzzy(query, text string) (score int, hits []int, ok bool) {
	q := []rune(strings.ToLower(strings.ReplaceAll(query, " ", "")))
	if len(q) == 0 {
		return 0, nil, true
	}
	t := []rune(strings.ToLower(text))
	qi, last := 0, -2
	for ti := 0; ti < len(t) && qi < len(q); ti++ {
		if t[ti] != q[qi] {
			continue
		}
		score++
		if ti == 0 || !unicode.IsLetter(t[ti-1]) {
			score += 8
		}
		if ti == last+1 {
			score += 5
		}
		hits = append(hits, ti)
		last = ti
		qi++
	}
	if qi < len(q) {
		return 0, nil, false
	}
	// Earlier is better, and a whole-name match best of all.
	score -= hits[0]
	if len(q) == len(t) {
		score += 20
	}
	return score, hits, true
}

// openPalette opens the palette with nothing typed and nothing selected.
func (m Model) openPalette() Model {
	m.palette = &palette{cursor: -1}
	m.failure, m.notice = "", ""
	return m
}

// updatePalette takes every key while the palette is open — including the
// ones that are otherwise commands, the way the drop prompt does.
func (m Model) updatePalette(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := *m.palette
	matches := m.filtered()
	switch msg.String() {
	case "esc", "ctrl+c", "ctrl+g":
		m.palette = nil
		return m, nil
	case "enter":
		if p.cursor < 0 || p.cursor >= len(matches) {
			return m, nil
		}
		c := matches[p.cursor]
		m.palette = nil
		if c.disabled != "" {
			return m.note(c.disabled), nil
		}
		return c.run(m)
	case "up", "ctrl+p", "ctrl+k", "shift+tab":
		p.cursor = step(p.cursor, -1, len(matches))
	case "down", "ctrl+n", "ctrl+j", "tab":
		p.cursor = step(p.cursor, 1, len(matches))
	case "pgup":
		p.cursor = max(0, p.cursor-paletteRows)
	case "pgdown":
		p.cursor = min(len(matches)-1, max(0, p.cursor)+paletteRows)
	case "backspace", "ctrl+h":
		if r := []rune(p.query); len(r) > 0 {
			p.query = string(r[:len(r)-1])
		}
		p.cursor = typed(p.query)
	case "ctrl+u", "ctrl+w":
		p.query = ""
		p.cursor = -1
	default:
		if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			p.query += string(msg.Runes)
			if msg.Type == tea.KeySpace {
				p.query += " "
			}
			p.cursor = typed(p.query)
		}
	}
	m.palette = &p
	if n := len(m.filtered()); m.palette.cursor >= n {
		m.palette.cursor = n - 1
	}
	return m, nil
}

// typed is where the cursor goes after the query changes: the best match, or
// nowhere once the query is empty again.
func typed(query string) int {
	if strings.TrimSpace(query) == "" {
		return -1
	}
	return 0
}

// step moves the cursor, wrapping at the ends. The first move from nothing
// selected lands on the first command, whichever way it went.
func step(cursor, delta, n int) int {
	if n == 0 {
		return -1
	}
	if cursor < 0 {
		return 0
	}
	return (cursor + delta + n) % n
}

// paletteFrame draws the palette over the frame: the frame faded behind it,
// and the box centred on it.
func (m Model) paletteFrame(frame string) string {
	box := strings.Split(m.renderPalette(), "\n")
	boxWidth := lipgloss.Width(box[0])
	rows := strings.Split(frame, "\n")
	x := max(0, (m.width-boxWidth)/2)
	y := max(0, (len(rows)-len(box))/3)

	for i, row := range rows {
		plain := ansi.Strip(row)
		if pad := m.width - lipgloss.Width(plain); pad > 0 {
			plain += strings.Repeat(" ", pad)
		}
		if i < y || i >= y+len(box) {
			rows[i] = m.styles.faint.Render(plain)
			continue
		}
		left := ansi.Truncate(plain, x, "")
		right := ansi.TruncateLeft(plain, x+boxWidth, "")
		rows[i] = m.styles.faint.Render(left) + box[i-y] + m.styles.faint.Render(right)
	}
	return strings.Join(rows, "\n")
}

// renderPalette draws the box: a prompt, the matches with a bar when there
// are more than fit, and the keys.
func (m Model) renderPalette() string {
	s := m.styles
	width := min(72, max(30, m.width-8))
	inner := width - 2 // the border

	p := m.palette
	matches := m.filtered()

	count := s.faint.Render(fmt.Sprintf("%d/%d ", len(matches), len(m.commands())))
	query := s.selected.Render(p.query) + s.thumb.Render("▌")
	if p.query == "" {
		query = s.thumb.Render("▌") + s.faint.Render("type a command")
	}
	prompt := padTo(" "+s.brand.Render(paletteKey)+" "+query, inner-lipgloss.Width(count)) + count

	rows := min(paletteRows, len(m.commands()))
	start := 0
	if p.cursor >= rows {
		start = p.cursor - rows + 1
	}
	var bar []string
	if len(matches) > rows {
		bar = m.scrollbar(start, rows, len(matches))
	}
	lines := []string{prompt, m.paletteRule(inner)}
	for i := 0; i < rows; i++ {
		at := start + i
		edge := " "
		switch {
		case bar != nil:
			edge = bar[i]
		case at == p.cursor:
			edge = s.paletteBar.Render(" ")
		}
		switch {
		case at < len(matches):
			lines = append(lines, m.paletteRow(matches[at], at == p.cursor, inner-1)+edge)
		case i == 0:
			lines = append(lines, padTo(s.faint.Render("   nothing matches"), inner-1)+edge)
		default:
			lines = append(lines, strings.Repeat(" ", inner-1)+edge)
		}
	}
	keys := s.hintKey.Render("↑↓") + s.hint.Render(" select   ") +
		s.hintKey.Render("enter") + s.hint.Render(" run   ") +
		s.hintKey.Render("esc") + s.hint.Render(" close")
	lines = append(lines, s.faint.Render(strings.Repeat("─", inner)), padTo(" "+keys, inner))

	return s.paletteBorder.Render(strings.Join(lines, "\n"))
}

// paletteRow draws one command: glyph, name with the matched letters lit,
// and its bare key. The selected row is a bar, every
// part of it on the bar's background, so a style ending inside it does not
// punch a hole.
func (m Model) paletteRow(c match, selected bool, width int) string {
	s := m.styles
	on := func(st lipgloss.Style) lipgloss.Style {
		if selected {
			return st.Background(s.paletteBar.GetBackground())
		}
		return st
	}

	name := s.live
	if c.disabled != "" {
		name = s.faint
	}
	if selected {
		name = name.Bold(true)
	}
	lit := map[int]bool{}
	for _, h := range c.hits {
		lit[h] = true
	}
	var b strings.Builder
	for i, r := range []rune(c.name) {
		st := name
		if lit[i] {
			st = s.match.Inherit(name)
		}
		b.WriteString(on(st).Render(string(r)))
	}

	marker := on(s.live).Render("   ")
	if selected {
		marker = on(s.selected).Render(" ❯ ")
	}
	left := marker + on(c.hue).Render(c.glyph) + on(s.live).Render(" ") + b.String()
	right := ""
	if c.key != "" {
		right = on(s.hintKey).Render(c.key) + on(s.live).Render(" ")
	}
	if room := width - lipgloss.Width(right); lipgloss.Width(left) > room {
		left = ansi.Truncate(left, room-1, "…")
	}
	gap := max(0, width-lipgloss.Width(left)-lipgloss.Width(right))
	return left + on(s.live).Render(strings.Repeat(" ", gap)) + right
}

// paletteRule divides the prompt from the list, and names the stitch the
// stitch commands act on — once, rather than on every row.
func (m Model) paletteRule(width int) string {
	s := m.styles
	st := m.selectedStitch()
	if st == nil {
		return s.faint.Render(strings.Repeat("─", width))
	}
	l := m.stitchLook(st)
	label := " " + l.hue.Render(l.glyph) + " " + s.soft.Render(st.ID) + " "
	if lipgloss.Width(label) > width-4 {
		label = ansi.Truncate(label, width-5, "…") + " "
	}
	return s.faint.Render("──") + label +
		s.faint.Render(strings.Repeat("─", max(0, width-2-lipgloss.Width(label))))
}

// padTo pads a rendered line with spaces to width cells.
func padTo(line string, width int) string {
	if gap := width - lipgloss.Width(line); gap > 0 {
		return line + strings.Repeat(" ", gap)
	}
	return line
}
