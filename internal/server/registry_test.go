package server

import (
	"path/filepath"
	"testing"

	"github.com/zealtv/easyloom/internal/target"
)

// stub builds a Loom for a script at <parent>/.loom/loom.sh without touching
// the file system: the registry only ever reads the resolved paths.
func stub(parent string) Loom {
	root := filepath.Join(parent, ".loom")
	return Loom{Target: target.Target{Script: filepath.Join(root, "loom.sh"), Root: root}}
}

// The name comes from the loom's parent directory, because every loom
// directory is called .loom and so names nothing.
func TestSlugComesFromTheParentDirectory(t *testing.T) {
	registry := NewRegistry()
	entry := registry.Add(stub("/home/someone/repos/easyloom"))

	if entry.Slug != "easyloom" {
		t.Errorf("slug %q, want %q", entry.Slug, "easyloom")
	}
	if entry.Display != "easyloom" {
		t.Errorf("display %q, want %q", entry.Display, "easyloom")
	}
}

func TestSlugIsNormalised(t *testing.T) {
	for _, tc := range []struct{ parent, want string }{
		{"/home/someone/bopOS", "bopos"},
		{"/home/someone/My Project", "my-project"},
		{"/home/someone/a..b", "a-b"},
		{"/home/someone/---", "loom"},
		{"/", "loom"},
	} {
		registry := NewRegistry()
		if got := registry.Add(stub(tc.parent)).Slug; got != tc.want {
			t.Errorf("Add(%q).Slug = %q, want %q", tc.parent, got, tc.want)
		}
	}
}

// Two checkouts of one repository in different parents is a real case, and the
// second has to be reachable as something.
func TestCollidingSlugsGetANumericSuffix(t *testing.T) {
	registry := NewRegistry()
	first := registry.Add(stub("/a/easyloom"))
	second := registry.Add(stub("/b/easyloom"))
	third := registry.Add(stub("/c/easyloom"))

	if first.Slug != "easyloom" || second.Slug != "easyloom-2" || third.Slug != "easyloom-3" {
		t.Fatalf("slugs %q, %q, %q", first.Slug, second.Slug, third.Slug)
	}
	if second.Display != "easyloom" {
		t.Errorf("display %q: the suffix is for the URL, not the name", second.Display)
	}
	for _, entry := range []*Entry{first, second, third} {
		found, ok := registry.Lookup(entry.Slug)
		if !ok || found != entry {
			t.Errorf("Lookup(%q) did not return its own entry", entry.Slug)
		}
	}
}

// One loom is one entry however many times it is registered: the script's
// realpath is the identity, so a re-add never mints a second slug for it.
func TestAddIsIdempotentPerLoom(t *testing.T) {
	registry := NewRegistry()
	first := registry.Add(stub("/a/easyloom"))
	again := registry.Add(stub("/a/easyloom"))

	if first != again {
		t.Fatalf("re-adding one loom made a second entry: %q and %q", first.Slug, again.Slug)
	}
	if len(registry.List()) != 1 {
		t.Fatalf("registry holds %d entries, want 1", len(registry.List()))
	}
}

// The primary is the loom the process was pointed at, fixed at startup.
func TestPrimaryIsTheFirstLoomAdded(t *testing.T) {
	registry := NewRegistry()
	if _, ok := registry.Primary(); ok {
		t.Fatal("an empty registry has a primary")
	}

	first := registry.Add(stub("/a/easyloom"))
	registry.Add(stub("/b/bopos"))

	primary, ok := registry.Primary()
	if !ok || primary != first {
		t.Errorf("Primary() = %v, want the first loom added", primary)
	}
}

func TestLookupOfAnUnknownSlug(t *testing.T) {
	registry := NewRegistry()
	registry.Add(stub("/a/easyloom"))
	if _, ok := registry.Lookup("nothing"); ok {
		t.Error("Lookup of an unregistered slug succeeded")
	}
}
