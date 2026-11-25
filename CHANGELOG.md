# Changelog

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
