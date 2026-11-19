# loom.sh, pinned at zealtv/loom 641e0da

`loom.sh` here is an unmodified copy of `.loom/loom.sh` from
[zealtv/loom](https://github.com/zealtv/loom) at commit `641e0da`
(2026-11-11). The deployed-loom tests in `internal/loom/deployed_test.go` run
it, so they check EasyLoom against a real loom with no network and no loom
installed.

## Updating the pin

1. Copy `.loom/loom.sh` from the new loom commit into
   `internal/loom/testdata/upstream-<short-commit>/loom.sh`, and delete this
   directory.
2. Update this README in the new directory, and `pinnedLoom` in
   `internal/loom/deployed_test.go`.
3. Run `go test ./...`.
