# testdata

The fixtures both implementations of the ranking are held to. One copy, read by
`node --test web/` and by `go test ./internal/unblock`, because two copies is
how the two implementations start being tested against different looms.

* `easyloom.json`, `easyloom-bite7.json` — real `map --json` output captured
  from EasyLoom's own development loom: one later, one earlier, before a slice
  of work was done. The arithmetic `unblock` exists to do was worked by hand
  against the earlier one to plan that work, so it is the fixture that says
  whether the module agrees with the person. Their `loom_root` is set to a
  neutral `/tmp/<name>/.loom`, as in the synthetic ones; nothing reads it.
* `cyclic.json`, `synthetic.json` — hand-written looms for the states a healthy
  loom does not have and should not acquire to be tested against: a dependency
  cycle, a stitch parked both directly and by inheritance, a claimed stitch, a
  broken dependency edge.
* `rankings.golden.json` — generated, not a fixture. `make golden`.

Every `*.json` that is not `*.golden.json` is a fixture, on both sides. Adding
one here adds it to both suites.

## What a fixture is for

The real ones prove the rule agrees with a loom people actually worked. The
synthetic ones prove it on the branches the real ones never take, and the
absence of those branches is not theoretical: two deliberate breakages of the Go
port — reordering `promote` ahead of `resume`, and dropping the cycle guard —
were caught by nothing when only the real looms were fixtures, because a healthy
loom never has a stitch that is both directly waiting and waiting by
inheritance, and never has a cycle at all. `synthetic.json` and the third stitch
in `cyclic.json` exist to make those two go red, and they do.

A third breakage, dropping the `other !== id` guard on `releases`, is caught by
nothing here and cannot be: the guard is unreachable in both implementations,
because a stitch with no open children is resolved by its own hypothesis and one
with open children cannot become ready while they stay open. Both files keep the
line anyway, since they are read side by side.

## Regenerating the real ones

They are captured output, and recapturing them changes what the tests mean —
the hand arithmetic in `unblock.test.mjs` is about the loom as it was. Capture a
new fixture under a new name rather than refreshing one in place.

Set the capture's `loom_root` to `/tmp/<name>/.loom` before committing it.

```sh
path/to/.loom/loom.sh map --json > web/testdata/<name>.json
make golden
```
