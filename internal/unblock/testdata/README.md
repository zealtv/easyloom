# testdata

The fixtures `go test ./internal/unblock` holds the ranking to, and the golden
that `go test ./internal/tui` reads too. One copy, so the two packages cannot
drift into being tested against different looms.

* `easyloom.json`, `easyloom-bite7.json` — real `map --json` output captured
  from EasyLoom's own development loom: one later, one earlier, before a slice
  of work was done. The arithmetic `unblock` exists to do was worked by hand
  against the earlier one to plan that work, so it is the fixture that says
  whether the package agrees with the person. Their `loom_root` is set to a
  neutral `/tmp/<name>/.loom`, as in the synthetic ones; nothing reads it.
* `cyclic.json`, `synthetic.json` — hand-written looms for the states a healthy
  loom does not have and should not acquire to be tested against: a dependency
  cycle, a stitch parked both directly and by inheritance, a claimed stitch, a
  broken dependency edge.
* `rankings.golden.json` — generated, not a fixture. Go's rankings over every
  fixture, regenerated deliberately with `go test ./internal/unblock -update`.

Every `*.json` that is not `*.golden.json` is a fixture. Adding one here adds it
to the golden and to `internal/unblock`'s tests. `internal/tui` reads only
`easyloom.json` and the golden.

## The golden

There is one implementation of the ranking, in Go. The golden file was first
generated from the browser UI's JavaScript (`web/unblock.js`), which was retired
on 2026-11-25. It was kept byte for byte, so the test shows that Go still gives
the answers the JavaScript gave. `go test ./internal/unblock -update` then wrote
the file again from Go's own rankings and `git diff` showed no change, so the
two agreed and the writer's format matches the file.

From here the golden holds Go to its own past answers. A change that moves a
ranking fails `go test ./internal/unblock` and names the entries that moved. If
the move is the point of the change, regenerate and read the diff:

```sh
go test ./internal/unblock -update
git diff internal/unblock/testdata/rankings.golden.json
```

`go test ./internal/tui` reads the same file and checks that the impact pane
draws the same stitches the ranking names.

Regenerating is never part of a plain `go test`: a test that quietly rewrites
what it asserts asserts nothing.

## What a fixture is for

The real ones prove the rule agrees with a loom people actually worked. The
synthetic ones prove it on the branches the real ones never take, and the
absence of those branches is not theoretical: two deliberate breakages of the
Go package — reordering `promote` ahead of `resume`, and dropping the cycle
guard — were caught by nothing when only the real looms were fixtures, because
a healthy loom never has a stitch that is both directly waiting and waiting by
inheritance, and never has a cycle at all. `synthetic.json` and the third
stitch in `cyclic.json` exist to make those two go red.

The golden alone does not do it, since `-update` regenerates it from whatever
the code now says. The tests that name the rules do:
`TestAStitchParkedTwiceSaysPromoteAndNamesTheAncestor` (over `synthetic.json`)
for promote before resume, and `TestACycleMemberReleasesNothing` (over
`cyclic.json`) for the cycle guard. With the two cases in `actionFor` swapped,
the first fails after `-update` as well as before it.

A third breakage, dropping the `other != id` guard on `releases`, is caught by
nothing here and cannot be: the guard is unreachable, because a stitch with no
open children is resolved by its own hypothesis and one with open children
cannot become ready while they stay open. The line stays to state the rule.

## Capturing a new real one

They are captured output, and recapturing changes what the tests mean — the
hand arithmetic in `unblock_test.go` is about the loom as it was. Capture a new
fixture under a new name rather than refreshing one in place.

Set the capture's `loom_root` to `/tmp/<name>/.loom` before committing it, then
regenerate the golden.

```sh
path/to/.loom/loom.sh map --json > internal/unblock/testdata/<name>.json
go test ./internal/unblock -update
```
