package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
)

func position(n int) *int { return &n }

func text(s string) *string { return &s }

// loomWith writes a loom root holding instructions for each path, and returns
// the root.
func loomWith(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range files {
		dir := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, instructionsName), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// projection is a loom with a four-stitch frontier spanning three threads, two
// of them queued, plus parked, archived and legacy records to be counted and
// hidden.
func projection(root string) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		SchemaVersion: snapshot.SupportedSchemaVersion,
		FormatVersion: snapshot.FormatVersionV2,
		LoomRoot:      root,
		// Effective preference order: queued first, then bytewise path order.
		Frontier: []string{"http-server", "ci-workflow", "snapshot-refresh", "docs"},
		Stitches: []snapshot.Stitch{
			{
				ID: "http-server", RootID: "web-shell", Path: "threads/web-shell/http-server",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Ready: true,
				QueuePosition: position(1),
				Dependencies: []snapshot.Dependency{
					{From: "http-server", To: "snapshot-types", Status: snapshot.DependencySatisfied},
				},
			},
			{
				ID: "ci-workflow", RootID: "foundation", Path: "threads/foundation/ci-workflow",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Ready: true,
				QueuePosition: position(2),
			},
			{
				ID: "snapshot-refresh", RootID: "snapshot-model", Path: "threads/snapshot-model/snapshot-refresh",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Ready: true,
			},
			{
				ID: "docs", RootID: "web-shell", Path: "threads/web-shell/docs",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain, Ready: true,
				Dependencies: []snapshot.Dependency{
					{From: "docs", To: "gone", Status: snapshot.DependencyBroken,
						Reason: func() *snapshot.BrokenReason { r := snapshot.BrokenMissing; return &r }()},
				},
			},
			{
				ID: "parked", RootID: "backlog", Path: "threads/backlog/parked",
				Tray: snapshot.TrayThreads, State: snapshot.StateWaiting,
			},
			{
				ID: "parked-child", RootID: "backlog", Path: "threads/backlog/parked/child",
				Tray: snapshot.TrayThreads, State: snapshot.StatePlain, WaitingInherited: true,
			},
			{
				ID: "done", RootID: "done", Path: "tied/done", Tray: snapshot.TrayTied,
				State: snapshot.StateTied, Archived: true, CompletedAt: text("2026-01-01T10:00:00+10:00"),
			},
			{
				ID: "old", RootID: "old", Path: "legacy-v1/tied/old", Tray: snapshot.TrayLegacyTied,
				State: snapshot.StateTied, Archived: true, Legacy: true,
			},
		},
	}
}

func modelOver(t *testing.T, snap *snapshot.Snapshot) Model {
	t.Helper()
	cache := snapshot.NewCache(neverRuns(t))
	cache.Seed(snap)
	t.Cleanup(cache.Close)
	return New(Options{
		Target:  target.Target{Root: snap.LoomRoot, Script: filepath.Join(snap.LoomRoot, "loom.sh")},
		Version: target.Version{Tier: target.TierV2},
		Cache:   cache,
		Width:   100,
		Height:  40,
	})
}

// The frontier is already in effective preference order. It is rendered as
// given, never re-sorted.
func TestFrontierKeepsProjectionOrder(t *testing.T) {
	snap := projection(t.TempDir())
	rows, _ := frontier(snap)

	var ids []string
	for _, r := range rows {
		ids = append(ids, r.id)
	}
	if strings.Join(ids, ",") != strings.Join(snap.Frontier, ",") {
		t.Fatalf("frontier reordered: %v, want %v", ids, snap.Frontier)
	}
}

// The queue is sparse and opt-in: a badge appears only where the projection
// gives a position, and most items having none is normal.
func TestQueueBadgeOnlyWhereQueued(t *testing.T) {
	m := modelOver(t, projection(t.TempDir()))
	view := m.View()

	for _, want := range []string{"queued 1", "queued 2"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
	if strings.Count(view, "queued") != 2 {
		t.Errorf("unqueued stitches carry a badge:\n%s", view)
	}
}

// The ID alone does not say which thread the work is in — the frontier spans
// several at once.
func TestRowNamesItsThread(t *testing.T) {
	m := modelOver(t, projection(t.TempDir()))
	view := m.View()
	for _, want := range []string{"web-shell", "foundation", "snapshot-model"} {
		if !strings.Contains(view, want) {
			t.Errorf("thread %q not shown:\n%s", want, view)
		}
	}
}

// An almost-empty frontier must read as "mostly parked", so the waiting count
// is permanent. Archived and legacy records are filtered out and counted.
func TestCountsAreAlwaysShown(t *testing.T) {
	m := modelOver(t, projection(t.TempDir()))
	view := m.View()
	for _, want := range []string{"4 ready", "2 waiting", "2 hidden"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q:\n%s", want, view)
		}
	}
	// A whole word, not a substring: the frame also shows the loom's path,
	// and macOS puts temp dirs under /var/folders.
	for _, line := range strings.Split(view, "\n") {
		if slices.Contains(strings.Fields(line), "old") {
			t.Errorf("a legacy record reached the list:\n%s", view)
		}
	}
}

// An empty frontier is a real state with its own message, not a blank pane.
// This repo's own loom reaches it.
func TestEmptyFrontierExplainsItself(t *testing.T) {
	snap := projection(t.TempDir())
	snap.Frontier = nil
	for i := range snap.Stitches {
		snap.Stitches[i].Ready = false
	}

	view := modelOver(t, snap).View()
	if !strings.Contains(view, "Nothing is ready") {
		t.Errorf("empty frontier renders blank:\n%s", view)
	}
	if !strings.Contains(view, "0 ready") {
		t.Errorf("counts missing from an empty frontier:\n%s", view)
	}
}

func TestSelectionMovesAndPreviewFollows(t *testing.T) {
	root := loomWith(t, map[string]string{
		"threads/web-shell/http-server":       "# HTTP server\n\nnet/http and nothing else.\n",
		"threads/foundation/ci-workflow":      "# CI\n\nGitHub Actions.\n",
		"threads/snapshot-model/snapshot-ref": "unused\n",
	})
	m := modelOver(t, projection(root))

	if !strings.Contains(m.View(), "net/http and nothing else") {
		t.Fatalf("the first stitch's instructions are not previewed:\n%s", m.View())
	}

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if !strings.Contains(m.View(), "GitHub Actions") {
		t.Fatalf("the preview did not follow the selection:\n%s", m.View())
	}

	// Clamped at both ends rather than wrapping.
	for i := 0; i < 10; i++ {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = next.(Model)
	}
	if m.selected != 0 {
		t.Fatalf("selection %d, want the top of the list", m.selected)
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	m = next.(Model)
	if m.selected != 3 {
		t.Fatalf("selection %d, want the end of the list", m.selected)
	}
}

// A refresh that reorders the frontier must not move the selection to a
// different stitch.
func TestSelectionFollowsTheStitchAcrossRefresh(t *testing.T) {
	root := t.TempDir()
	m := modelOver(t, projection(root))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(Model)
	if m.selectedID != "ci-workflow" {
		t.Fatalf("selected %q, want ci-workflow", m.selectedID)
	}

	reordered := projection(root)
	reordered.Frontier = []string{"docs", "ci-workflow", "http-server", "snapshot-refresh"}
	m.state.Snapshot = reordered
	m = m.reselect()

	if m.selectedID != "ci-workflow" || m.selected != 1 {
		t.Fatalf("selection moved to %q at %d", m.selectedID, m.selected)
	}
}

// The preview shows protocol state from the projection, including a broken
// dependency's reason.
func TestPreviewShowsProtocolFacts(t *testing.T) {
	root := loomWith(t, map[string]string{
		"threads/web-shell/http-server": "# HTTP server\n",
		"threads/web-shell/docs":        "# Docs\n",
	})
	m := modelOver(t, projection(root))

	view := m.View()
	for _, want := range []string{"plain", "threads", "ready", "queue 1", "needs snapshot-types", "satisfied"} {
		if !strings.Contains(view, want) {
			t.Errorf("preview missing %q:\n%s", want, view)
		}
	}

	m = m.moveTo(3)
	view = m.View()
	for _, want := range []string{"needs gone", "broken", "missing"} {
		if !strings.Contains(view, want) {
			t.Errorf("broken dependency not explained: missing %q:\n%s", want, view)
		}
	}
}

// completed_at is nil on legacy records. Render it as unknown; never guess it
// from mtime.
func TestCompletedAtUnknownOnLegacy(t *testing.T) {
	snap := projection(t.TempDir())
	legacy := snap.Stitches[7]
	m := modelOver(t, snap)
	if got := m.facts0(&legacy); !strings.Contains(got, "completed unknown") {
		t.Fatalf("legacy completion rendered as %q", got)
	}
	tied := snap.Stitches[6]
	if got := m.facts0(&tied); !strings.Contains(got, "2026-01-01T10:00:00+10:00") {
		t.Fatalf("authoritative completion rendered as %q", got)
	}
}

// A stitch whose instructions are missing says so rather than showing an empty
// pane or crashing.
func TestPreviewReportsAMissingFile(t *testing.T) {
	m := modelOver(t, projection(t.TempDir()))
	if !strings.Contains(m.View(), "no instructions to show") {
		t.Errorf("a missing artifact is not reported:\n%s", m.View())
	}
}

// Resizing must not corrupt the layout with two panes in it either.
func TestPanesSurviveResize(t *testing.T) {
	root := loomWith(t, map[string]string{
		"threads/web-shell/http-server": strings.Repeat("a long line of instructions\n", 40),
	})
	m := modelOver(t, projection(root))

	for _, size := range []tea.WindowSizeMsg{{Width: 40, Height: 10}, {Width: 200, Height: 60}, {Width: 20, Height: 6}} {
		next, _ := m.Update(size)
		m = next.(Model)
		view := m.View()
		got := strings.Split(view, "\n")
		if len(got) > size.Height {
			t.Errorf("%dx%d: %d lines drawn", size.Width, size.Height, len(got))
		}
		for _, line := range got {
			if len([]rune(line)) > size.Width {
				t.Errorf("%dx%d: line overflows: %q", size.Width, size.Height, line)
			}
		}
		if !strings.Contains(view, "FRONTIER") {
			t.Errorf("%dx%d: the list is gone:\n%s", size.Width, size.Height, view)
		}
	}
}

// The path comes from the projection but lands in a filesystem read, so it is
// joined defensively.
func TestInstructionsPathRefusesEscapes(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		"../outside",
		"threads/../../outside",
		"/etc",
		"",
		".",
	} {
		if got, err := instructionsPath(root, path); err == nil {
			t.Errorf("path %q accepted as %q", path, got)
		}
	}

	got, err := instructionsPath(root, "threads/goal/child")
	if err != nil {
		t.Fatalf("a normal path was refused: %v", err)
	}
	if want := filepath.Join(root, "threads", "goal", "child", instructionsName); got != want {
		t.Fatalf("path %q, want %q", got, want)
	}
}
