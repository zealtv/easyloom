# Changelog

## v0.3.1 (2026-11-28)

- The ready line for tireless-watcher's map moves from `@tw-next` to
  `@tw-idle`, written as `next: fix-flake +2 queued`. `@tw-next` is no longer
  set.
- The README says EasyLoom has a glance for
  [Tireless Watcher](https://github.com/zealtv/tireless-watcher), and no
  longer carries the glance's detail.

## v0.3.0 (2026-11-27)

EasyLoom now tells a tmux session map about itself, from its own pane.

- The command palette opens on `>` instead of `:`. `:` no longer opens it.
- Inside tmux, EasyLoom keeps a few options on its own pane for a map of the
  session to read: `@tw-next` names the stitch the queue would hand over
  next and how many more are queued, and `@tw_status` with
  `@tw_status_text` says what EasyLoom is doing while it claims, ties, parks
  or queues a stitch (`fail`, with the loom's words, if the loom refused).
- It also publishes the queue as a glance for the map: a badge line that
  counts the queue by state, most pressing first (a record the loom cannot
  resolve leads, as `!`), and the queue itself in the loom's order, one row
  per stitch with the glyph the queue pane draws. An id too long for its
  row keeps its head and tail, with an ellipsis in the middle.
- All of these options are cleared when EasyLoom exits, and nothing is
  written outside tmux.

## v0.2.0 (2026-11-25)

EasyLoom is now the terminal UI alone.

- The browser frontend is gone: `easyloom web` and its `--port`, `--no-open`
  and `--dev` flags have been removed, along with the HTTP server and the
  embedded web assets. Run `easyloom [path]` for the terminal UI as before.
- Building and testing need only Go. Node is no longer used, not even for
  tests.
- Inside tmux, EasyLoom sets the pane option `@tw-focus` to the tabs and the
  list, so a small live preview of the pane (such as tireless-watcher's) shows
  the queue rather than the screen's edge.

## v0.1.0 (2026-11-19)

The first public release: a terminal viewer and controller for a
[loom](https://github.com/zealtv/loom) in format v2.

- Run `easyloom` inside a repository with a loom, or `easyloom <path>`.
- See the ready frontier, the queue, the thread tree and the impact of
  resolving each stitch, beside a markdown preview of the selected stitch.
- Claim, tie, park, tend, drop and queue stitches from the command palette
  (`:`), which runs loom itself; reorder the queue and undo the last move.
- Open a stitch in `$EDITOR` with `enter`, or its folder in the file manager.
- Follows changes to the loom on disk as they happen.
- Explains a v1 loom instead of opening it, and points at loom's migration.
- Builds as one static binary for macOS and Linux; install with `go install`.
