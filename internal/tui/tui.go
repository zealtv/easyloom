// Package tui is the terminal frontend: a Bubble Tea program over the same
// snapshot cache the rest of EasyLoom reads.
//
// There is no server in this path. The program owns a snapshot.Cache directly
// and reads it on every render, which is a pointer load and never blocks — a
// snapshot costs seconds on a large loom, and the render loop must never wait
// on one.
package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/zealtv/easyloom/internal/loom"
	"github.com/zealtv/easyloom/internal/picker"
	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/twfocus"
	"github.com/zealtv/easyloom/internal/userconfig"
	"github.com/zealtv/easyloom/internal/warm"
)

// pollInterval is how often the program asks the cache whether anything
// changed. It is a pointer load per tick, and it bounds how stale the age in
// the status bar can look.
const pollInterval = 250 * time.Millisecond

// Options configure one run of the terminal frontend.
type Options struct {
	// Target is the resolved loom. Its script path has already been shown to
	// the user by the time the program starts.
	Target target.Target
	// Version is the probe result. Only a v2 target reaches the program.
	Version target.Version
	// Cache is seeded with the probe's snapshot before the program starts, so
	// the first frame draws without running map a second time.
	Cache *snapshot.Cache
	// Mutator invokes loom to change something. A nil one is a read-only
	// session, which the lifecycle keys report rather than pretending.
	Mutator *loom.Runner

	// Set holds the looms open. A nil one is a session that cannot switch
	// looms, and the picker key says so rather than doing nothing.
	Set *warm.Set
	// Getenv resolves the user configuration directory, so the picker can read
	// and edit the registry. Nil reads the real environment.
	Getenv userconfig.Getenv
	// Prober is shared with the picker's scan.
	Prober *target.Prober
	// ScanRoot is where the picker's scan starts. Empty takes the parent of
	// the working directory.
	ScanRoot string

	// Output and Input are the terminal. Both default to the process's.
	Output io.Writer
	Input  io.Reader

	// Colour is false for NO_COLOR or a non-terminal output.
	Colour bool
	// Width and Height are the initial size. Zero takes a conventional 80x24,
	// which a terminal immediately replaces with a WindowSizeMsg.
	Width, Height int
	// Interactive is false when the output is not a terminal, in which case
	// one frame is written and the program never starts.
	Interactive bool
}

// Run draws the loom until the user quits.
//
// It restores the terminal on the way out, including on a panic: a crashed TUI
// must not leave the terminal wrecked. Quitting cancels any map run in flight.
func Run(ctx context.Context, opts Options) error {
	if opts.Output == nil {
		opts.Output = os.Stdout
	}
	if opts.Input == nil {
		opts.Input = os.Stdin
	}

	model := New(opts)
	if opts.Interactive {
		model.focus.ID = os.Getenv("TMUX_PANE")
	}

	// Not a terminal: draw one frame in plain text and stop. An interactive
	// program with no keyboard would just hang holding the pipe open.
	if !opts.Interactive {
		_, err := fmt.Fprintln(opts.Output, model.View())
		return err
	}

	program := tea.NewProgram(
		model,
		tea.WithContext(ctx),
		tea.WithOutput(opts.Output),
		tea.WithInput(opts.Input),
		tea.WithAltScreen(),
		// For the wheel. Reporting the mouse costs the terminal's own
		// drag-to-select, which most terminals hand back on shift+drag.
		tea.WithMouseCellMotion(),
	)

	// Bubble Tea restores the terminal for a panic inside its own loop. This
	// covers a panic anywhere else on the way through Run.
	defer func() {
		if r := recover(); r != nil {
			program.Kill()
			panic(r)
		}
	}()

	_, err := program.Run()
	// Quit cancels the map run in flight rather than leaving a subprocess
	// behind holding the loom. There is no cache to close when the program
	// started cold and the user quit out of the picker without opening
	// anything; the loom set closes whatever the picker did open.
	if opts.Cache != nil {
		opts.Cache.Close()
	}
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// tickMsg asks the model to look at the cache again. The cache does not push:
// the generation it publishes is the honest signal that something changed, and
// polling it costs one atomic load.
type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// view is which body the whole pane is showing. The queue and the tree are
// views the body switches to, not panes squeezed in beside the frontier:
// easyloom at 20 columns still has to draw something legible.
type view int

const (
	viewFrontier view = iota
	viewQueue
	viewTree
	viewImpact
	viewCount
)

// title is the heading over each view's list.
func (v view) title() string {
	switch v {
	case viewQueue:
		return " QUEUE"
	case viewTree:
		return " THREADS"
	case viewImpact:
		return " IMPACT"
	default:
		return " FRONTIER"
	}
}

// tabs titles the list. With colour, every view is named and the one that is
// up is lit, so the strip says where tab goes next; without colour there is no
// way to light one, and only the current view is named.
func (m Model) tabs() string {
	if !m.colour {
		return m.styles.heading.Render(m.view.title())
	}
	var b strings.Builder
	for v := view(0); v < viewCount; v++ {
		title := v.title()
		if v != m.view {
			b.WriteString(m.styles.faint.Render(title) + " ")
			continue
		}
		// Lit from the word, not the space that leads every title.
		b.WriteString(" " + m.styles.tab.Render(strings.TrimSpace(title)) + " ")
	}
	return strings.TrimRight(b.String(), " ")
}

// tally colours a header's counts: each "N word" gets its number in the hue
// of what it counts, and a zero recedes. The words are the views' own, so this
// only has to know what each one means.
func (m Model) tally(summary string) string {
	parts := strings.Split(summary, " · ")
	for i, part := range parts {
		n, word, ok := strings.Cut(part, " ")
		if !ok {
			parts[i] = m.styles.dim.Render(part)
			continue
		}
		hue := m.styles.dim
		switch word {
		case "ready", "loose ends":
			hue = m.styles.ready
		case "waiting", "parked":
			hue = m.styles.waiting
		case "queued":
			hue = m.styles.queue
		case "skipped", "disagree":
			hue = m.styles.warn
		}
		if n == "0" {
			hue = m.styles.faint
		}
		parts[i] = hue.Render(n) + m.styles.dim.Render(" "+word)
	}
	return strings.Join(parts, m.styles.faint.Render(" · "))
}

// next is the view tab moves on to: frontier → queue → tree → impact →
// frontier. Impact goes last because it is the view you reach for when the
// frontier has not already answered the question.
func (v view) next() view { return (v + 1) % viewCount }

// name is what to call a view in the key hints.
func (v view) name() string { return strings.ToLower(strings.TrimSpace(v.title())) }

// cursor is one view's place in its own list.
type cursor struct {
	index int
	id    string
}

// Model is the program state. Everything it draws about the loom comes from
// the cache; it keeps no second copy of protocol state.
type Model struct {
	// focus tells a pane previewer where the list is (twfocus). Only Run
	// gives it a pane, so tests and one-frame output report nothing.
	focus twfocus.Pane

	loom    target.Target
	version target.Version
	cache   *snapshot.Cache

	state  snapshot.CacheState
	width  int
	height int

	// refreshing is true from the moment r is pressed until an attempt
	// finishes. Attempts, not the generation, is the signal: a refresh that
	// fails still finishes.
	refreshing bool
	baseline   uint64
	frame      int

	// view is which body is drawn: the frontier, the queue, or the thread tree.
	view view
	// selected indexes the active view's list. It is kept on an ID across
	// refreshes: a snapshot that reorders the frontier must not move the
	// selection out from under the user, and neither must one that reshapes
	// the tree.
	selected   int
	selectedID string
	// places is where each view was left. Three views means the single stash
	// slot the thread tree added no longer does.
	places [viewCount]cursor
	// expanded holds the nodes the user has opened or closed, by ID, so a
	// background re-snapshot does not fold the tree up underneath them.
	expanded map[string]bool

	// preview holds the selected stitch's instructions, read once per
	// selection rather than on every render.
	preview     string
	previewErr  error
	previewPath string
	// palette is the command palette while it is open.
	palette *palette

	// scroll is how many lines of the instructions are scrolled past. It
	// belongs to one stitch's instructions, so a new selection starts at the
	// top.
	scroll int
	// dependencies opens the full dependency list in the preview's scrolling
	// area. A new stitch always opens on its instructions.
	dependencies bool

	// colour is false under NO_COLOR or a non-terminal output. The markdown
	// renderer needs it directly: with no colour to replace them, a heading's
	// # and an emphasis's ** are kept rather than deleted.
	colour bool

	// mutator is the only way to the loom's write half. It is nil when the
	// loom is open read-only, and the keys say so rather than failing.
	mutator *loom.Runner
	// pending is the mutation in flight. One at a time: they contend for the
	// queue lock, and a burst of keystrokes should not become a burst of
	// commands racing each other.
	pending *mutation
	// over is the optimistic view of what the pending mutation will do, and
	// undo is what to put back if the loom refuses it.
	over overlay
	undo overlay
	// lastMove is the command that would put the last queue move back. One
	// level is enough: u is a keystroke guard, not a history.
	lastMove *mutation
	// prompt is the drop reason being typed.
	prompt *prompt
	// picker is the loom list, when it is up. Like prompt, it takes every key
	// while it is open.
	picker   *picker.Model
	set      *warm.Set
	getenv   userconfig.Getenv
	prober   *target.Prober
	scanRoot string
	// failure is the loom's own words about a refused mutation; notice is a
	// key that had nothing to do.
	failure string
	notice  string

	styles styles
}

// New returns the model without starting anything.
func New(opts Options) Model {
	m := Model{
		loom:    opts.Target,
		version: opts.Version,
		cache:   opts.Cache,
		mutator: opts.Mutator,
		width:   opts.Width,
		height:  opts.Height,
		colour:  opts.Colour,
		styles:  newStyles(opts.Colour),

		set:      opts.Set,
		getenv:   opts.Getenv,
		prober:   opts.Prober,
		scanRoot: opts.ScanRoot,
	}
	if m.prober == nil {
		m.prober = &target.Prober{}
	}
	if m.scanRoot == "" {
		m.scanRoot = picker.ScanRoot(os.Getwd)
	}
	if m.width <= 0 {
		m.width = 80
	}
	if m.height <= 0 {
		m.height = 24
	}
	if opts.Cache != nil {
		m.state = opts.Cache.Current()
	} else {
		// Started with no loom: the picker is the program until one is chosen.
		m.state = coldState
		m = m.openPicker()
	}
	return m.reselect()
}

// Init starts the poll. Nothing else runs at startup: the cache is already
// seeded with the probe's snapshot.
func (m Model) Init() tea.Cmd { return tick() }

// Update handles keys, resizes, and the poll, then tells a pane previewer where the list
// is now.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	nm, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	if f := nm.focus.Report(nm.focusRect()); f != nil {
		cmd = tea.Batch(cmd, f)
	}
	return nm, cmd
}

// focusRect is the part of the screen a small preview should show: the view's
// tabs and its list (the queue, on the queue view), under the status bar. With
// the picker, the palette or no snapshot up, it names nothing.
func (m Model) focusRect() twfocus.Rect {
	if m.picker != nil || m.palette != nil || m.state.Snapshot == nil || m.width < 1 {
		return twfocus.Rect{}
	}
	status := m.status()
	if m.cache == nil {
		status = m.pickerStatus()
	}
	list, _ := share(m.budget(status, m.footer()), m.listSize())
	// The status bar and its rule, then the list's title (the tabs) and the list.
	return twfocus.Rect{X: 0, Y: lines(status) + 1, W: m.width, H: 1 + list}
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// A terminal that cannot report its size sends zeros. Keep the last
		// good size rather than collapsing the layout to nothing.
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		return m, nil

	case tea.KeyMsg:
		// A drop reason is being typed, so every key belongs to it — including
		// the ones that are otherwise commands.
		if m.prompt != nil {
			return m.updatePrompt(msg)
		}
		// The picker takes every key while it is up, the same way the prompt
		// does — including the ones that are otherwise commands.
		if m.picker != nil {
			return m.updatePicker(msg)
		}
		if m.palette != nil {
			return m.updatePalette(msg)
		}
		// Bare keys only move around. Everything else — every mutation,
		// refreshing, switching looms and quitting — is in the palette, so a
		// sentence typed into the wrong tmux pane cannot change the loom or
		// close the program. ctrl+c still quits: it is a chord, not a letter.
		switch msg.String() {
		case paletteKey:
			return m.openPalette(), nil
		case "ctrl+c":
			return m, tea.Quit
		case "tab":
			return m.switchView(), nil
		case "up", "k":
			return m.move(-1), nil
		case "down", "j":
			return m.move(1), nil
		case "shift+up":
			return m.scrollBy(-1), nil
		case "shift+down":
			return m.scrollBy(1), nil
		case "pgup":
			return m.scrollBy(-m.page()), nil
		case "pgdown":
			return m.scrollBy(m.page()), nil
		case "ctrl+u":
			return m.scrollBy(-max(1, m.page()/2)), nil
		case "ctrl+d":
			return m.scrollBy(max(1, m.page()/2)), nil
		case "home", "g":
			return m.moveTo(0), nil
		case "end", "G":
			return m.moveTo(len(m.ids()) - 1), nil
		case "enter":
			// The selected stitch is the selected stitch whichever pane you
			// are on, so this is bound on all three. The tree keeps space and
			// ←/→ for folding, which is what enter used to do there.
			return m.edit()
		case " ":
			return m.toggleExpand(nil), nil
		case "right", "l":
			return m.toggleExpand(open(true)), nil
		case "left", "h":
			return m.toggleExpand(open(false)), nil
		}
		return m, nil

	case tea.MouseMsg:
		return m.wheel(msg), nil

	case openedMsg:
		return m.opened(msg)

	case scannedMsg:
		return m.scanned(msg)

	case mutationMsg:
		return m.finish(msg)

	case editedMsg:
		return m.edited(msg)

	case revealedMsg:
		return m.revealed(msg)

	case tickMsg:
		m.frame++
		if m.cache != nil {
			m.state = m.cache.Current()
		}
		if m.refreshing && m.state.Attempts > m.baseline {
			m.refreshing = false
		}
		// A newer projection is authoritative, so whatever the optimistic
		// overlay believed is discarded — silently, and whether or not it
		// agrees. We are a viewer.
		if !m.over.empty() && m.state.Generation != m.over.generation {
			m.over = overlay{}
			m.undo = overlay{}
		}
		return m.reselect(), tick()
	}
	return m, nil
}

// startRefresh asks for a snapshot in the background and returns immediately.
// The view keeps drawing the snapshot it already has while the run happens.
func (m Model) startRefresh() Model {
	if m.cache == nil || m.refreshing {
		return m
	}
	m.baseline = m.state.Attempts
	m.refreshing = true
	m.cache.Refresh()
	return m
}

// View renders the whole screen: status bar, body, key hints.
func (m Model) View() string {
	status := m.status()
	if m.cache == nil {
		status = m.pickerStatus()
	}
	footer := m.footer()
	budget := m.budget(status, footer)
	body := m.body(budget)
	if pad := budget - lines(body); pad > 0 {
		body += strings.Repeat("\n", pad)
	}
	frame := strings.Join([]string{
		status,
		m.rule(),
		body,
		m.rule(),
		footer,
	}, "\n")

	if m.palette != nil {
		frame = m.paletteFrame(frame)
	}

	// Clip rather than wrap. A narrow terminal loses the right-hand end of a
	// line; it does not get a layout that reflows into nonsense.
	clip := lipgloss.NewStyle().MaxWidth(m.width)
	if m.height > 0 {
		clip = clip.MaxHeight(m.height)
	}
	return clip.Render(frame)
}

// footer is the key hints, or the drop prompt while one is open. Nothing is
// greyed out on our own reading of what the loom would allow: offer the
// action, and let the loom's refusal be the answer.
func (m Model) footer() string {
	if m.prompt != nil {
		return m.styles.warn.Render(" drop "+m.prompt.id+": ") +
			m.prompt.reason + m.styles.hint.Render("▌   enter confirm · esc cancel")
	}
	if m.picker != nil {
		rows := m.pickerHints()
		return wrapHints(rows[0], m.width, m.styles) + "\n" +
			wrapHints(rows[1], m.width, m.styles)
	}
	return wrapHints(m.hints(), m.width, m.styles)
}

// hint is one key and the action it names. The key is deliberately brighter
// than its label so the footer can be scanned by glyph.
type hint struct {
	key   string
	label string
}

// hints is getting around and the palette, nothing more. Everything else,
// refresh, switching looms and quit included, is in the palette, so one row
// is all the footer needs.
func (m Model) hints() []hint {
	out := []hint{{"↑/↓", "select"}, {"⇧↑/↓", "scroll"}, {"tab", m.view.next().name()}}
	if m.view == viewTree || m.view == viewImpact {
		out = append(out, hint{"←/→", "fold"})
	}
	return append(out, hint{"enter", "edit"}, hint{paletteKey, "commands"})
}

// selectedHasSuffix reads direct lifecycle state from the selected stitch's
// path. waiting_inherited can coexist with a direct waiting state; the
// directory suffix is the unambiguous fact that resume can act on.
func (m Model) selectedHasSuffix(state string) bool {
	st := m.selectedStitch()
	return st != nil && strings.HasSuffix(filepath.Base(st.Path), "."+state)
}

// wrapHints keeps every hint and starts another line when the next pair does
// not fit. A pair wider than the terminal remains intact on its own line: the
// frame clips its label, but its key and every later key are still present.
func wrapHints(hints []hint, width int, styles styles) string {
	if width < 1 {
		width = 1
	}
	const gap = "   "
	var rows []string
	var row string
	rowWidth := 0
	for _, h := range hints {
		pair := styles.hintKey.Render(h.key) + " " + styles.hint.Render(h.label)
		pairWidth := lipgloss.Width(pair)
		if row != "" && rowWidth+len(gap)+pairWidth > width {
			rows = append(rows, row)
			row, rowWidth = "", 0
		}
		if row != "" {
			row += gap
			rowWidth += len(gap)
		}
		row += pair
		rowWidth += pairWidth
	}
	if row != "" {
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

// open is a pointer to a fixed expansion choice, for the keys that set one
// rather than toggling.
func open(want bool) *bool { return &want }

// status is where a stale or failed refresh surfaces. The last good snapshot
// keeps rendering behind it; the view is never blanked because a refresh
// failed.
func (m Model) status() string {
	name := m.styles.brand.Render(displayName(m.loom))
	right := m.styles.dim.Render(strings.Join(m.facts(), " · "))
	// The path is the part that gives way. It can be any length, and a deep
	// one would otherwise push the facts — the stitch count, the snapshot's
	// age — off the edge, where the clip silently drops them.
	room := m.width - lipgloss.Width(name) - 2 - lipgloss.Width(right) - 1
	left := name + "  " + m.styles.dim.Render(trimLeft(shortRoot(m.loom), room))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	line := left + strings.Repeat(" ", gap) + right
	if m.state.Stale {
		line += "\n" + m.styles.warn.Render(
			fmt.Sprintf(" stale: the last refresh failed: %v", m.state.Err))
	} else if m.state.Err != nil {
		line += "\n" + m.styles.warn.Render(fmt.Sprintf(" %v", m.state.Err))
	}
	// A mutation plus a refresh is a multi-second round trip on a big loom, so
	// what is running has to be visible or the UI looks frozen.
	if m.pending != nil {
		line += "\n" + m.styles.dim.Render(" "+m.spinner()+" "+m.pending.gerund+"…")
	}
	if m.failure != "" {
		for _, l := range strings.Split(m.failure, "\n") {
			line += "\n" + m.styles.warn.Render(" "+l)
		}
	}
	if m.notice != "" {
		line += "\n" + m.styles.dim.Render(" "+m.notice)
	}
	return line
}

func (m Model) facts() []string {
	facts := []string{string(m.version.Tier)}
	if m.state.Snapshot != nil {
		facts = append(facts, fmt.Sprintf("%d stitches", len(m.state.Snapshot.Stitches)))
		facts = append(facts, m.age())
	} else {
		facts = append(facts, "no snapshot yet")
	}
	if m.refreshing {
		facts = append(facts, m.spinner()+" refreshing")
	}
	return facts
}

func (m Model) age() string {
	if m.state.CompletedAt.IsZero() {
		return "age unknown"
	}
	age := m.state.Age()
	if age < time.Second {
		return "just now"
	}
	return fmt.Sprintf("%s ago", age.Round(time.Second))
}

// spinnerFrames are ASCII so a terminal without a font for braille still shows
// motion rather than replacement characters.
var spinnerFrames = []string{"|", "/", "-", "\\"}

func (m Model) spinner() string {
	return spinnerFrames[m.frame%len(spinnerFrames)]
}

// rows is the frontier as the list draws it.
func (m Model) rows() []row {
	rows, _ := m.rowsAndCounts()
	return rows
}

// rowsAndCounts is the projection's frontier with any unconfirmed mutation
// laid over it, so the list responds on the keystroke rather than when the
// snapshot catches up seconds later.
func (m Model) rowsAndCounts() ([]row, counts) {
	if m.state.Snapshot == nil {
		return nil, counts{}
	}
	rows, c := frontier(m.state.Snapshot)
	return m.over.apply(rows, c)
}

// ids is the active view's list, top to bottom. Selection is the same idea in
// all three: an ID, and the row it currently sits on.
func (m Model) ids() []string {
	switch m.view {
	case viewQueue:
		rows := m.queueRows()
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.id
		}
		return ids
	case viewTree:
		rows := m.treeRows()
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.id
		}
		return ids
	case viewImpact:
		rows := m.impactRows()
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.id
		}
		return ids
	default:
		rows := m.rows()
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.id
		}
		return ids
	}
}

// switchView moves the body on to the next view, each one keeping its own
// place.
func (m Model) switchView() Model {
	m.places[m.view] = cursor{m.selected, m.selectedID}
	m.view = m.view.next()
	m.selected, m.selectedID = m.places[m.view].index, m.places[m.view].id
	return m.reselect()
}

// move changes the selection by delta, clamped to the list.
func (m Model) move(delta int) Model { return m.moveTo(m.selected + delta) }

func (m Model) moveTo(index int) Model {
	ids := m.ids()
	if len(ids) == 0 {
		return m
	}
	if index < 0 {
		index = 0
	}
	if index >= len(ids) {
		index = len(ids) - 1
	}
	m.selected = index
	m.selectedID = ids[index]
	return m.loadPreview()
}

// reselect follows the selected ID across a refresh, so a snapshot that
// reorders the frontier — or reshapes the tree — does not move the selection
// to a different stitch.
func (m Model) reselect() Model {
	ids := m.ids()
	if len(ids) == 0 {
		m.selected, m.selectedID = 0, ""
		return m.loadPreview()
	}
	if m.selectedID != "" {
		for i, id := range ids {
			if id == m.selectedID {
				m.selected = i
				return m.loadPreview()
			}
		}
	}
	if m.selected >= len(ids) {
		m.selected = len(ids) - 1
	}
	m.selectedID = ids[m.selected]
	return m.loadPreview()
}

// loadPreview reads the selected stitch's instructions, and only when the file
// it would read has changed. The read is off the render path.
func (m Model) loadPreview() Model {
	st := m.selectedStitch()
	if st == nil {
		m.preview, m.previewErr, m.previewPath, m.scroll = "", nil, "", 0
		m.dependencies = false
		return m
	}
	if st.Path == m.previewPath {
		return m
	}
	m.previewPath, m.scroll = st.Path, 0
	m.dependencies = false
	m.preview, m.previewErr = readInstructions(m.state.Snapshot.LoomRoot, st.Path)
	return m
}

func (m Model) selectedStitch() *snapshot.Stitch {
	if m.state.Snapshot == nil || m.selectedID == "" {
		return nil
	}
	return m.state.Snapshot.Index()[m.selectedID]
}

// body is the two panes that make the TUI worth opening: the ready work, and
// what the selected stitch entails.
func (m Model) body(height int) string {
	if m.picker != nil {
		return m.pickerBody(height)
	}
	if m.state.Snapshot == nil {
		return m.styles.dim.Render("\n No snapshot yet. Press r to take one.\n")
	}
	// Whichever view is up gets the same layout: a titled list over the
	// preview of whatever it has selected.
	rows, active := m.rowsAndCounts()
	size, summary := len(rows), active.header()
	var branches []treeRow
	var queued []queueRow
	var ranked []impactRow
	switch m.view {
	case viewQueue:
		var records queueCounts
		queued, records = m.queueRowsAndCounts()
		size, summary = len(queued), records.header()
	case viewTree:
		branches, active = tree(m.state.Snapshot, m.expanded)
		size, summary = len(branches), active.header()
	case viewImpact:
		var ranking impactCounts
		ranked, ranking = m.impactRowsAndCounts()
		size, summary = len(ranked), ranking.header()
	}

	listHeight, previewHeight := share(height, size)

	header := m.pad(m.tabs(), m.tally(summary)+" ")
	var list string
	switch m.view {
	case viewQueue:
		list = m.renderQueue(queued, listHeight)
	case viewTree:
		list = m.renderTree(branches, listHeight)
	case viewImpact:
		list = m.renderImpact(ranked, listHeight)
	default:
		list = m.renderList(rows, listHeight)
	}
	if pad := listHeight - lines(list); pad > 0 {
		list += strings.Repeat("\n", pad)
	}

	return strings.Join([]string{
		header,
		list,
		m.rule(),
		m.detail(previewHeight),
	}, "\n")
}

// budget is the body's height: the frame less the status bar, the footer and
// the two rules. The footer wraps rather than discarding hints, so its
// current rendered height is part of it.
func (m Model) budget(status, footer string) int {
	budget := m.height - lines(status) - lines(footer) - 2
	if budget < 1 {
		budget = 1
	}
	return budget
}

// share shares the body between the list and the preview: the list gets what
// it needs up to half the space, and the preview takes the rest, less the
// list's title and the rule between them. One line each is the floor.
func share(height, size int) (list, preview int) {
	list = size
	if list == 0 {
		list = 1
	}
	if half := (height - 2) / 2; list > half {
		list = half
	}
	if list < 1 {
		list = 1
	}
	preview = height - list - 2
	if preview < 1 {
		preview = 1
	}
	return list, preview
}

// layout is where the panes fall on screen, for the keys and the wheel that
// act on one of them: the first row of the preview, and its height.
func (m Model) layout() (top, height int) {
	status := m.status()
	if m.cache == nil {
		status = m.pickerStatus()
	}
	budget := m.budget(status, m.footer())
	list, preview := share(budget, m.listSize())
	// The status bar and its rule, the list's title, the list and its rule.
	return lines(status) + 1 + 1 + list + 1, preview
}

// listSize is how many rows the current view's list has.
func (m Model) listSize() int {
	if m.state.Snapshot == nil {
		return 0
	}
	switch m.view {
	case viewQueue:
		return len(m.queueRows())
	case viewTree:
		return len(m.treeRows())
	case viewImpact:
		return len(m.impactRows())
	default:
		return len(m.ids())
	}
}

// page is how far a page scrolls: the instructions' window, less a line kept
// for context.
func (m Model) page() int {
	_, height := m.layout()
	_, _, window := m.previewParts(m.selectedStitch(), height)
	return max(1, window-1)
}

// scrollBy moves the instructions, kept within what there is to show.
func (m Model) scrollBy(delta int) Model {
	st := m.selectedStitch()
	if st == nil {
		return m
	}
	_, height := m.layout()
	_, body, window := m.previewParts(st, height)
	m.scroll = max(0, min(m.scroll+delta, len(body)-window))
	return m
}

// wheelStep is how many lines one notch of the wheel scrolls.
const wheelStep = 3

// wheel scrolls whichever pane the pointer is over: the instructions below,
// the selection above.
func (m Model) wheel(msg tea.MouseMsg) Model {
	if m.picker != nil || m.prompt != nil {
		return m
	}
	var delta int
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		delta = -1
	case tea.MouseButtonWheelDown:
		delta = 1
	default:
		return m
	}
	if m.palette != nil {
		p := *m.palette
		p.cursor = step(p.cursor, delta, len(m.filtered()))
		m.palette = &p
		return m
	}
	if top, _ := m.layout(); msg.Y >= top {
		return m.scrollBy(delta * wheelStep)
	}
	return m.move(delta)
}

// detail is the lower half: what the selected stitch entails, or — for a queue
// record naming a stitch the projection does not have — why there is nothing
// to show.
func (m Model) detail(height int) string {
	if st := m.selectedStitch(); st != nil {
		return m.renderPreview(st, height)
	}
	if m.view == viewQueue && m.selectedID != "" {
		return m.styles.warn.Render(
			" " + m.selectedID + " is a queue record with no stitch behind it.\n" +
				" Nothing here can repair it: remove the record with loom unqueue.")
	}
	// The impact pane's group headers and its disagreement warning are rows
	// about the pane rather than about a stitch, so there is nothing to preview
	// and saying which of the two you are on is more use than a blank half.
	if m.view == viewImpact {
		switch m.selectedID {
		case impactAlarmID:
			return m.styles.warn.Render(
				" The ranking below is computed from readiness this build derives\n" +
					" wrongly. Fix internal/unblock before trusting any of it.")
		case impactLooseEndsID, impactParkedID:
			return m.styles.dim.Render(
				" A group heading. ←/→ or space folds the rows that release nothing.")
		}
	}
	return ""
}

// pad puts left and right on one line, right-aligned to the width.
func (m Model) pad(left, right string) string {
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// lines counts the rendered lines in a block.
func lines(block string) int { return strings.Count(block, "\n") + 1 }

func (m Model) rule() string {
	width := m.width
	if width < 1 {
		width = 1
	}
	return m.styles.dim.Render(strings.Repeat("─", width))
}

// trimLeft fits a path into width cells by dropping its start, which is the
// least telling end of it: the directory the loom lives in is at the other.
func trimLeft(path string, width int) string {
	if lipgloss.Width(path) <= width {
		return path
	}
	if width < 2 {
		return ""
	}
	runes := []rune(path)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[1:]
	}
	return "…" + string(runes)
}

// displayName is what to call this loom: the repository directory it lives in.
func displayName(t target.Target) string {
	if t.Root == "" {
		return "loom"
	}
	name := filepath.Base(filepath.Dir(t.Root))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return filepath.Base(t.Root)
	}
	return name
}

// shortRoot is the loom directory with the user's home elided, which is what
// makes two open looms distinguishable without eating the status bar.
func shortRoot(t target.Target) string {
	root := t.Root
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(root, home+string(filepath.Separator)); ok {
			return "~" + string(filepath.Separator) + rest
		}
	}
	return root
}

type styles struct {
	name     lipgloss.Style
	dim      lipgloss.Style
	heading  lipgloss.Style
	selected lipgloss.Style
	warn     lipgloss.Style
	hint     lipgloss.Style
	hintKey  lipgloss.Style

	// The thread tree's palette. See treeLook.
	live      lipgloss.Style
	goal      lipgloss.Style
	parked    lipgloss.Style
	faint     lipgloss.Style
	struck    lipgloss.Style
	ready     lipgloss.Style
	waiting   lipgloss.Style
	stitching lipgloss.Style
	tending   lipgloss.Style
	queue     lipgloss.Style
	brand     lipgloss.Style
	match     lipgloss.Style

	paletteBorder lipgloss.Style
	paletteBar    lipgloss.Style
	thumb         lipgloss.Style
	tab           lipgloss.Style
	next          lipgloss.Style
	soft          lipgloss.Style

	// The preview's markdown. Kept apart from the chrome above because these
	// style a file's contents rather than the program's own furniture.
	mdHeading    lipgloss.Style
	mdStrong     lipgloss.Style
	mdEmphasis   lipgloss.Style
	mdInlineCode lipgloss.Style
	mdCode       lipgloss.Style
	mdBullet     lipgloss.Style
	mdQuote      lipgloss.Style
	mdLink       lipgloss.Style
	mdRule       lipgloss.Style
}

// newStyles makes every style a no-op when colour is off, so NO_COLOR and a
// redirected stdout produce plain text rather than escape sequences.
func newStyles(colour bool) styles {
	if !colour {
		plain := lipgloss.NewStyle()
		return styles{
			name: plain, dim: plain, heading: plain, selected: plain, warn: plain,
			hint: plain, hintKey: plain,
			live: plain, goal: plain, parked: plain, faint: plain, struck: plain,
			ready: plain, waiting: plain, stitching: plain, tending: plain,
			queue: plain, brand: plain, match: plain,
			paletteBorder: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()),
			paletteBar:    plain,
			thumb:         plain, tab: plain, next: plain, soft: plain,
			mdHeading: plain, mdStrong: plain, mdEmphasis: plain, mdInlineCode: plain,
			mdCode: plain, mdBullet: plain, mdQuote: plain, mdLink: plain, mdRule: plain,
		}
	}
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	faint := lipgloss.AdaptiveColor{Light: "243", Dark: "244"}
	return styles{
		name:     lipgloss.NewStyle().Bold(true),
		dim:      dim,
		heading:  lipgloss.NewStyle().Bold(true),
		selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81")),
		warn:     lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		hint:     dim,
		hintKey:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")),

		// The greys are the faintest that clear a 4.5:1 contrast against a
		// dark or a light background. Fainter reads better on paper, but a
		// terminal enforcing minimum contrast (Ghostty's minimum-contrast)
		// pushes anything under it most of the way to white, and the faded
		// rows come out brighter than the live ones. The hues are the
		// terminal's own palette, so they follow its theme.
		live:      lipgloss.NewStyle(),
		goal:      lipgloss.NewStyle().Bold(true),
		parked:    lipgloss.NewStyle().Italic(true).Foreground(lipgloss.AdaptiveColor{Light: "240", Dark: "250"}),
		faint:     lipgloss.NewStyle().Foreground(faint),
		struck:    lipgloss.NewStyle().Foreground(faint).Strikethrough(true),
		ready:     lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		waiting:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		stitching: lipgloss.NewStyle().Foreground(lipgloss.Color("12")),
		tending:   lipgloss.NewStyle().Foreground(lipgloss.Color("13")),
		queue:     lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
		brand:     lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		match:     lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Underline(true),

		paletteBorder: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("6")),
		paletteBar: lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "254", Dark: "237"}),
		thumb:      lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		next:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2")),
		tab:        lipgloss.NewStyle().Bold(true).Underline(true),
		// Context beside a name, such as the thread a frontier row belongs
		// to: quieter than the name, louder than finished work.
		soft: lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "240", Dark: "248"}),

		mdHeading:    lipgloss.NewStyle().Bold(true),
		mdStrong:     lipgloss.NewStyle().Bold(true),
		mdEmphasis:   lipgloss.NewStyle().Italic(true),
		mdInlineCode: lipgloss.NewStyle().Foreground(lipgloss.Color("151")),
		mdCode:       dim,
		mdBullet:     dim,
		mdQuote:      dim,
		// The same tint the selection uses: what these instructions point at
		// is the thing you are most likely to want to look at next.
		mdLink: lipgloss.NewStyle().Foreground(lipgloss.Color("81")),
		mdRule: dim,
	}
}
