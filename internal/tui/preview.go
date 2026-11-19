package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/zealtv/easyloom/internal/snapshot"
)

// instructionsName is the artifact every stitch has. The projection gives a
// stitch's path but not its contents.
const instructionsName = "instructions.md"

// instructionsPath joins a projection path onto the loom root for a filesystem
// read.
//
// Reading artifacts does not breach the integration boundary: the boundary is
// about protocol state — readiness, ordering, lifecycle — which stays with the
// projection. Artifacts are files, and reading them is the point of the
// feature. The path still comes from outside this process and lands in a file
// read, so anything escaping the loom root is refused.
func instructionsPath(loomRoot, path string) (string, error) {
	if loomRoot == "" {
		return "", fmt.Errorf("no loom root in the projection")
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if clean == "." || filepath.IsAbs(clean) {
		return "", fmt.Errorf("stitch path %q is not relative to the loom", path)
	}
	full := filepath.Join(loomRoot, clean, instructionsName)
	rel, err := filepath.Rel(loomRoot, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("stitch path %q escapes the loom root", path)
	}
	return full, nil
}

// readInstructions returns a stitch's instructions, or the reason there are
// none to show.
func readInstructions(loomRoot, path string) (string, error) {
	full, err := instructionsPath(loomRoot, path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(string(data), "\t", "    "), nil
}

// renderPreview draws what the selected stitch entails: its protocol facts
// from the projection, then its instructions from disk, rendered as the
// markdown they are written in.
//
// The facts stay put and the instructions or dependency details scroll beneath
// them, with a bar down the right edge when there is more than fits.
func (m Model) renderPreview(st *snapshot.Stitch, height int) string {
	if st == nil {
		return ""
	}
	head, body, window := m.previewParts(st, height)
	if len(body) <= window {
		lines := append(head, body...)
		if height > 0 && len(lines) > height {
			lines = lines[:height]
		}
		return strings.Join(lines, "\n")
	}

	scroll := max(0, min(m.scroll, len(body)-window))
	// Where the reader is, on the facts line: the lines in view of how many.
	where := m.styles.faint.Render(fmt.Sprintf("%d–%d/%d", scroll+1, scroll+window, len(body)))
	if len(head) > 1 {
		head[1] = m.pad(head[1], where+" ")
	}

	bar := m.scrollbar(scroll, window, len(body))
	lines := head
	for i, line := range body[scroll : scroll+window] {
		gap := max(0, m.width-1-lipgloss.Width(line))
		lines = append(lines, line+strings.Repeat(" ", gap)+bar[i])
	}
	return strings.Join(lines, "\n")
}

// previewParts lays the preview out: the head that stays put, the body that
// scrolls, and how many body lines fit under the head in height.
//
// The body is wrapped two cells narrow whether or not it overflows, so the
// bar appearing never reflows the text it scrolls.
func (m Model) previewParts(st *snapshot.Stitch, height int) (head, body []string, window int) {
	if st == nil {
		return nil, nil, 0
	}
	l := m.stitchLook(st)
	head = []string{
		l.hue.Render(l.glyph) + " " + m.styles.heading.Inherit(l.name).Render(st.ID),
		m.styles.dim.Render(m.facts0(st)),
	}
	// At most three dependencies stay inline, and only while leaving room for
	// the instructions. Long lists get one summary, never an unbounded header.
	compact := len(st.Dependencies) > min(3, max(0, height-6))
	if len(st.Dependencies) > 0 {
		if compact || m.dependencies {
			head = append(head, m.dependencySummary(st))
		} else {
			head = append(head, m.dependencyLines(st)...)
		}
	}
	head = append(head, "")
	// Even a very short terminal must leave a row for scrollable content.
	if height > 0 && len(head) >= height {
		head = head[:max(0, height-1)]
	}

	switch {
	case m.dependencies && len(st.Dependencies) > 0:
		for _, line := range m.dependencyLines(st) {
			body = append(body, strings.Split(ansi.Wrap(line, max(1, m.width-2), ""), "\n")...)
		}
	case m.previewErr != nil:
		body = []string{m.styles.warn.Render(
			fmt.Sprintf(" no instructions to show: %v", m.previewErr))}
	default:
		body = renderMarkdown(strings.TrimRight(m.preview, "\n"), m.width-2, m.styles, m.colour)
	}
	return head, body, max(1, height-len(head))
}

func (m Model) dependencySummary(st *snapshot.Stitch) string {
	counts := make(map[snapshot.DependencyStatus]int)
	for _, dep := range st.Dependencies {
		counts[dep.Status]++
	}
	parts := []string{fmt.Sprintf(" %d dependencies", len(st.Dependencies))}
	for _, status := range []snapshot.DependencyStatus{snapshot.DependencyBroken, snapshot.DependencyBlocked, snapshot.DependencySatisfied, snapshot.DependencyInert} {
		if n := counts[status]; n > 0 {
			style := m.styles.warn
			if status == snapshot.DependencySatisfied || status == snapshot.DependencyInert {
				style = m.styles.dim
			}
			parts = append(parts, style.Render(fmt.Sprintf("%d %s", n, status)))
		}
	}
	return strings.Join(parts, m.styles.faint.Render(" · "))
}

// scrollbar draws the bar beside a window of lines: a faint track, and a thumb
// as long as the share of the text in view, as far down as the window is.
func (m Model) scrollbar(scroll, window, total int) []string {
	thumb := max(1, window*window/total)
	at := 0
	if total > window {
		// Rounded, so the thumb reaches the bottom exactly at the end.
		at = (scroll*(window-thumb) + (total-window)/2) / (total - window)
	}
	bar := make([]string, window)
	for i := range bar {
		if i >= at && i < at+thumb {
			bar[i] = m.styles.thumb.Render("┃")
		} else {
			bar[i] = m.styles.faint.Render("│")
		}
	}
	return bar
}

// facts0 is the one-line summary of a stitch's protocol state. Everything on
// it is read from the projection; none of it is inferred from disk.
func (m Model) facts0(st *snapshot.Stitch) string {
	parts := []string{
		string(st.State),
		string(st.Tray),
	}
	if st.Ready {
		parts = append(parts, "ready")
	} else if st.WaitingInherited {
		parts = append(parts, "waiting inherited")
	} else {
		parts = append(parts, "not ready")
	}
	if st.QueuePosition != nil {
		parts = append(parts, fmt.Sprintf("queue %d", *st.QueuePosition))
	}
	if st.State == snapshot.StateTied || st.State == snapshot.StateDropped {
		// Legacy records carry no timestamp, and the protocol forbids guessing
		// one from mtime.
		completed := "unknown"
		if st.CompletedAt != nil {
			completed = *st.CompletedAt
		}
		parts = append(parts, "completed "+completed)
	}
	return " " + strings.Join(parts, " · ")
}

func (m Model) dependencyLines(st *snapshot.Stitch) []string {
	if len(st.Dependencies) == 0 {
		return nil
	}
	lines := make([]string, 0, len(st.Dependencies))
	for _, dep := range st.Dependencies {
		line := fmt.Sprintf("needs %s — %s", dep.To, dep.Status)
		if dep.Reason != nil {
			line += fmt.Sprintf(" (%s)", *dep.Reason)
		}
		switch dep.Status {
		case snapshot.DependencySatisfied:
			lines = append(lines, " "+m.styles.ready.Render("✓")+" "+m.styles.dim.Render(line))
		case snapshot.DependencyInert:
			// The dependent is resolved or archived: history, not a problem.
			lines = append(lines, " "+m.styles.dim.Render("· "+line))
		default:
			lines = append(lines, " "+m.styles.warn.Render("○ "+line))
		}
	}
	return lines
}
