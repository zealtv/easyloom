package target

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scanTree builds one fixture covering everything the scan has to get right:
//
//	root/
//	  alpha/.loom/loom.sh              a plain hit
//	  beta/.loom/loom                  the v1 script name
//	  mirror/.loom/loom.sh -> alpha's  the same loom by another path
//	  node_modules/pkg/.loom/loom.sh   pruned
//	  vendor/dep/.loom/loom.sh         pruned
//	  .git/worktree/.loom/loom.sh      pruned
//	  a/b/c/d/e/deep/.loom/loom.sh     past the depth cap
//	  notes.txt                        not a directory
//
// It returns the root and the two roots a default scan should report.
func scanTree(t *testing.T) (root string, want []string) {
	t.Helper()
	// The temp dir may itself sit under a symlink, and every reported root
	// comes back resolved.
	root = t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	root = resolved

	alpha := fixtureLoom(t, filepath.Join(root, "alpha", loomDirName), "loom.sh")
	fixtureLoom(t, filepath.Join(root, "beta", loomDirName), "loom")

	// A mirror whose script is a symlink to alpha's. Both paths reach one
	// loom, and one loom is one result.
	mirror := filepath.Join(root, "mirror", loomDirName)
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(alpha, filepath.Join(mirror, "loom.sh")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	for _, pruned := range []string{"node_modules/pkg", "vendor/dep", ".git/worktree"} {
		fixtureLoom(t, filepath.Join(root, filepath.FromSlash(pruned), loomDirName), "loom.sh")
	}

	fixtureLoom(t, filepath.Join(root, "a", "b", "c", "d", "e", "deep", loomDirName), "loom.sh")

	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	return root, []string{
		filepath.Join(root, "alpha", loomDirName),
		filepath.Join(root, "beta", loomDirName),
	}
}

func roots(found []Found) []string {
	out := make([]string, len(found))
	for i, f := range found {
		out[i] = f.Target.Root
	}
	return out
}

func TestScanFindsLoomsAndPrunes(t *testing.T) {
	root, want := scanTree(t)

	found, err := Scan(context.Background(), root, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}

	got := roots(found)
	if len(got) != len(want) {
		t.Fatalf("Scan found %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Scan[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The mirror's script is a symlink to alpha's, so both resolve to one key. A
// registry keyed by that key must never see it twice.
func TestScanDeduplicatesBySymlink(t *testing.T) {
	root, _ := scanTree(t)

	found, err := Scan(context.Background(), root, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if strings.Contains(f.Target.Root, "mirror") {
			t.Errorf("Scan reported the mirror separately: %s", f.Target.Root)
		}
	}

	keys := map[string]bool{}
	for _, f := range found {
		if keys[f.Target.Key()] {
			t.Errorf("Scan reported key %q twice", f.Target.Key())
		}
		keys[f.Target.Key()] = true
	}
}

// The depth cap is what makes scanning a home directory tolerable, so raising
// it has to actually reach further down.
func TestScanDepthCap(t *testing.T) {
	root, _ := scanTree(t)

	shallow, err := Scan(context.Background(), root, ScanOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(shallow); got != 2 {
		t.Errorf("depth 1 found %d looms (%v), want the two at the top", got, roots(shallow))
	}

	deep, err := Scan(context.Background(), root, ScanOptions{Depth: 8})
	if err != nil {
		t.Fatal(err)
	}
	var reachedDeep bool
	for _, f := range deep {
		if strings.Contains(f.Target.Root, string(filepath.Separator)+"deep"+string(filepath.Separator)) {
			reachedDeep = true
		}
	}
	if !reachedDeep {
		t.Errorf("depth 8 did not reach the buried loom: %v", roots(deep))
	}

	// And the default does not.
	def, err := Scan(context.Background(), root, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range def {
		if strings.Contains(f.Target.Root, string(filepath.Separator)+"deep"+string(filepath.Separator)) {
			t.Errorf("the default depth reached the buried loom at %s", f.Target.Root)
		}
	}
}

// A loom directly under the scan root is found: pointing --scan at one repo is
// a reasonable thing to do.
func TestScanFindsLoomAtTheRoot(t *testing.T) {
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	fixtureLoom(t, filepath.Join(resolved, loomDirName), "loom.sh")

	found, err := Scan(context.Background(), resolved, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Target.Root != filepath.Join(resolved, loomDirName) {
		t.Fatalf("Scan found %v, want the loom at the root", roots(found))
	}
}

// An unreadable directory is one directory that was not searched, not the end
// of the search.
func TestScanSkipsUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable directories")
	}
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	fixtureLoom(t, filepath.Join(resolved, "readable", loomDirName), "loom.sh")

	locked := filepath.Join(resolved, "locked")
	fixtureLoom(t, filepath.Join(locked, "inner", loomDirName), "loom.sh")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	found, err := Scan(context.Background(), resolved, ScanOptions{})
	if err != nil {
		t.Fatalf("Scan failed on an unreadable directory: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("Scan found %v, want only the readable loom", roots(found))
	}
}

// A non-executable script is not a deployed loom, and resolution is where that
// is decided — the scan does not get its own opinion.
func TestScanIgnoresNonExecutableScripts(t *testing.T) {
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(resolved, "broken", loomDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "loom.sh"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	found, err := Scan(context.Background(), resolved, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Errorf("Scan found %v, want nothing", roots(found))
	}
}

func TestScanRejectsNonDirectories(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Scan(context.Background(), file, ScanOptions{}); err == nil {
		t.Error("Scan of a file = nil error, want one")
	}
	if _, err := Scan(context.Background(), filepath.Join(root, "absent"), ScanOptions{}); err == nil {
		t.Error("Scan of a missing directory = nil error, want one")
	}
}

// With no Prober the scan reports paths and no tiers, which is the fast path.
func TestScanWithoutProberDoesNotProbe(t *testing.T) {
	root, _ := scanTree(t)

	found, err := Scan(context.Background(), root, ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if f.Version.Tier != "" {
			t.Errorf("%s got tier %q with no Prober", f.Target.Root, f.Version.Tier)
		}
	}
}

// With a Prober every hit carries a tier, because the picker shows one per
// entry. These fixture scripts are stubs that emit no projection, so the tier
// they earn is v1 — what matters is that each was probed exactly once.
func TestScanProbesEveryHit(t *testing.T) {
	root, want := scanTree(t)

	var prober Prober
	found, err := Scan(context.Background(), root, ScanOptions{Prober: &prober, Concurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != len(want) {
		t.Fatalf("Scan found %v, want %v", roots(found), want)
	}
	for _, f := range found {
		if f.Version.Tier == "" {
			t.Errorf("%s was not probed", f.Target.Root)
		}
		if f.Version.ProbedAt.IsZero() {
			t.Errorf("%s has no probe time", f.Target.Root)
		}
	}

	// The Prober remembers, so the scan's probes are reusable by whoever opens
	// one of these looms next.
	for _, f := range found {
		if _, ok := prober.probed[f.Target.Key()]; !ok {
			t.Errorf("%s was not remembered by the prober", f.Target.Root)
		}
	}
}

func TestScanHonoursContextCancellation(t *testing.T) {
	root, _ := scanTree(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Scan(ctx, root, ScanOptions{}); err == nil {
		t.Error("Scan with a cancelled context = nil error, want one")
	}
}
