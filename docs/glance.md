# Glance for Tireless Watcher

Inside tmux, EasyLoom sets options on its own pane for tireless-watcher's map
to read. The map's own `docs/interface.md` and `docs/glance.md` define them;
this note records what EasyLoom writes (as of v0.3.0 plus `glance-tw-idle`,
2026-11-28). Nothing is written outside tmux, and every option is unset when
EasyLoom exits.

- `@tw-idle`: the ready line, drawn by the map as written: `next: fix-flake +2
  queued` (the stitch `loom next` would take, and how many more queued stitches
  are still to do). Unset when nothing queued is ready. Replaces `@tw-next`.
- `@tw_status`, `@tw_status_text`, `@tw_status_at`: `work` or `fail`, with the
  loom's own words, while EasyLoom claims, ties, parks or queues a stitch.
- `@tw-focus`: `x,y,w,h`, the tabs and list in the pane's cells, so a live
  preview can show the list rather than a corner of the screen.
- `@tw-glance-far` and `@tw-glance-mid`: a badge line counting the queue by
  state (a record the loom cannot resolve leads, as `!`), and the queue itself,
  one row per stitch. An id too long for its row keeps its head and tail.

Code: `internal/twpane`, `internal/twfocus`, `internal/twglance`.
