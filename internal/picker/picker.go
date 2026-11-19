// Package picker chooses a loom.
//
// It is a plain component rather than a Bubble Tea model, because it is used
// two ways: as an overlay inside the running terminal frontend, and as a
// standalone program when EasyLoom starts outside any repository and has
// nothing to open yet. Both drive the same list.
package picker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zealtv/easyloom/internal/known"
	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/userconfig"
)

// Entry is one loom offered.
type Entry struct {
	Target target.Target
	// Tier is what the loom was last seen at, from the registry. It is drawn
	// immediately and corrected when the loom is opened: probing every entry
	// before the first frame would make the picker slower than the error it
	// replaces.
	Tier target.Tier
	// Missing is true when the deployed script is no longer there. The entry
	// is still shown — a loom on an unmounted drive is not a loom to forget —
	// but choosing it would fail, and it can be forgotten from the list.
	Missing bool
}

// Name is what to call this loom: the repository directory, which is the
// parent of the loom directory.
func (e Entry) Name() string {
	return filepath.Base(filepath.Dir(e.Target.Root))
}

// Model is the picker's state.
type Model struct {
	entries []Entry
	cursor  int
	// notice is the last thing that happened — a scan's result, a forget.
	notice string
	// scanning is true while a scan runs, so the view can say so rather than
	// looking hung.
	scanning bool
	// getenv resolves the configuration directory, for forgetting an entry.
	getenv userconfig.Getenv
}

// New returns a picker over the registry.
//
// A registry that cannot be read is an empty picker and a notice, never a
// failure: the scan action is still there, and that is the way out.
func New(getenv userconfig.Getenv) Model {
	m := Model{getenv: getenv}
	list, err := known.Load(getenv)
	if err != nil {
		m.notice = err.Error()
	}
	m.entries = entriesFrom(list.Looms)
	return m
}

// entriesFrom converts registry records, marking the ones whose loom has gone.
func entriesFrom(looms []known.Loom) []Entry {
	out := make([]Entry, 0, len(looms))
	for _, l := range looms {
		out = append(out, Entry{Target: l.Target(), Tier: l.Tier, Missing: !scriptExists(l.Key)})
	}
	return out
}

func scriptExists(script string) bool {
	info, err := os.Stat(script)
	return err == nil && info.Mode().IsRegular()
}

// Empty reports whether there is nothing to choose. An empty picker opens
// straight into a scan: a list with nothing in it and no way forward is the
// old "no loom found" error with extra steps.
func (m Model) Empty() bool { return len(m.entries) == 0 }

// Entries is the list as drawn.
func (m Model) Entries() []Entry { return m.entries }

// Selected is the entry under the cursor, or nil when the list is empty.
func (m Model) Selected() *Entry {
	if m.cursor < 0 || m.cursor >= len(m.entries) {
		return nil
	}
	return &m.entries[m.cursor]
}

// Notice is the last thing that happened, for the view to show.
func (m Model) Notice() string { return m.notice }

// Scanning reports whether a scan is in flight.
func (m Model) Scanning() bool { return m.scanning }

// Move steps the cursor, clamped rather than wrapped: the ends of a list are a
// place, and wrapping makes a long list disorienting.
func (m Model) Move(delta int) Model {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.entries) {
		m.cursor = len(m.entries) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	return m
}

// MoveTo puts the cursor at an index, clamped.
func (m Model) MoveTo(index int) Model {
	m.cursor = index
	return m.Move(0)
}

// Forget drops the selected entry from the registry and the list. It is what
// an entry whose loom has been deleted needs, since nothing else would ever
// remove it.
func (m Model) Forget() Model {
	entry := m.Selected()
	if entry == nil {
		return m
	}
	if err := known.Forget(m.getenv, entry.Target.Key()); err != nil {
		m.notice = err.Error()
		return m
	}
	m.entries = append(m.entries[:m.cursor:m.cursor], m.entries[m.cursor+1:]...)
	m.notice = "forgot " + entry.Name()
	return m.Move(0)
}

// StartScan marks a scan as running. The scan itself is the caller's, because
// it is a subprocess-heavy walk and the caller owns how it is run.
func (m Model) StartScan() Model {
	m.scanning = true
	m.notice = "scanning…"
	return m
}

// Merge folds a scan's results into the list, keeping registry order at the
// front and appending what is new. A scan does not reorder the looms the user
// already works in.
func (m Model) Merge(found []target.Found) Model {
	m.scanning = false

	known := map[string]bool{}
	for _, e := range m.entries {
		known[e.Target.Key()] = true
	}

	fresh := make([]Entry, 0, len(found))
	for _, f := range found {
		if known[f.Target.Key()] {
			continue
		}
		fresh = append(fresh, Entry{Target: f.Target, Tier: f.Version.Tier})
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].Target.Root < fresh[j].Target.Root })

	m.entries = append(m.entries, fresh...)
	switch len(fresh) {
	case 0:
		m.notice = fmt.Sprintf("scanned %d looms, nothing new", len(found))
	case 1:
		m.notice = "found 1 new loom"
	default:
		m.notice = fmt.Sprintf("found %d new looms", len(fresh))
	}
	return m.Move(0)
}

// ScanRoot is where a scan starts from when the user has not said: the parent
// of the directory EasyLoom was started in, which is where a repository's
// siblings live.
func ScanRoot(getwd func() (string, error)) string {
	dir, err := getwd()
	if err != nil || dir == "" {
		return "."
	}
	if parent := filepath.Dir(dir); parent != dir {
		return parent
	}
	return dir
}

// Scan walks for looms, for the caller to hand back to Merge.
func Scan(ctx context.Context, root string, prober *target.Prober) ([]target.Found, error) {
	return target.Scan(ctx, root, target.ScanOptions{Prober: prober})
}

// Rows renders the list as plain lines, one per entry, for a caller to style
// and frame. Keeping the layout here and the colour in the caller is what lets
// the overlay and the standalone program draw the same list.
func (m Model) Rows() []Row {
	rows := make([]Row, len(m.entries))
	for i, e := range m.entries {
		tier := string(e.Tier)
		if tier == "" {
			tier = "?"
		}
		if e.Missing {
			tier = "missing"
		}
		rows[i] = Row{
			Selected: i == m.cursor,
			Name:     e.Name(),
			Tier:     tier,
			Root:     shorten(e.Target.Root),
			Missing:  e.Missing,
		}
	}
	return rows
}

// Row is one drawn line.
type Row struct {
	Selected bool
	Name     string
	Tier     string
	Root     string
	Missing  bool
}

// shorten writes a path under the home directory with a tilde, which is how
// people say these paths out loud.
func shorten(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

// Cursor is where the selection sits, for a caller that scrolls the list.
func (m Model) Cursor() int { return m.cursor }
