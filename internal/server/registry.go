package server

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/zealtv/easyloom/internal/loom"
	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
)

// Loom is one loom the server can serve, with everything needed to answer for
// it. The cache is per loom: two looms open in two tabs are two caches and
// never one shared "current" one.
type Loom struct {
	// Target is the resolved loom. Its Key — the realpath of the deployed
	// script — is the identity a slug maps to.
	Target target.Target
	// Version is the probe result for this loom.
	Version target.Version
	// Cache holds this loom's projection.
	Cache *snapshot.Cache
	// Mutator is the write half: it invokes the deployed loom.sh to change the
	// loom. A nil one is a read-only loom, and the endpoints that would mutate
	// say so rather than pretending.
	Mutator *loom.Runner
}

// Entry is a registered loom and the slug it is reachable at.
type Entry struct {
	Loom
	// Slug is the URL component identifying this loom.
	Slug string
	// Display is the human name — the basename of the loom's parent directory,
	// before slug normalisation and any collision suffix.
	Display string
}

// Registry maps slugs to looms.
//
// The loom lives in the URL and nowhere else. There is deliberately no
// "current loom" here: every request names its loom, so two tabs on two looms
// cannot make each other render the wrong one.
//
// It is in-memory and seeded with the resolved target. Persisting it across
// runs — and so serving looms that were never on the command line — is
// left for later.
type Registry struct {
	mu      sync.RWMutex
	bySlug  map[string]*Entry
	byKey   map[string]*Entry
	order   []string
	primary string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{bySlug: map[string]*Entry{}, byKey: map[string]*Entry{}}
}

// Add registers a loom and returns its entry. Registering the same loom twice
// — the same script realpath, however it was reached — returns the first
// entry rather than inventing a second slug for one loom.
//
// The first loom added is the primary: the one the process was pointed at, and
// where a request for the site root lands.
func (r *Registry) Add(loom Loom) *Entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.byKey[loom.Target.Key()]; ok {
		return existing
	}

	display := displayName(loom.Target)
	entry := &Entry{Loom: loom, Slug: r.uniqueSlugLocked(slugify(display)), Display: display}
	r.bySlug[entry.Slug] = entry
	r.byKey[loom.Target.Key()] = entry
	r.order = append(r.order, entry.Slug)
	if r.primary == "" {
		r.primary = entry.Slug
	}
	return entry
}

// Lookup returns the loom a slug names.
func (r *Registry) Lookup(slug string) (*Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.bySlug[slug]
	return entry, ok
}

// List returns the entries in registration order.
func (r *Registry) List() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.order))
	for _, slug := range r.order {
		out = append(out, r.bySlug[slug])
	}
	return out
}

// Primary is the slug of the loom the process was pointed at. It is where a
// bare request lands, and it is fixed at startup — nothing a request does can
// change which loom that is.
func (r *Registry) Primary() (*Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.bySlug[r.primary]
	return entry, ok
}

// uniqueSlugLocked resolves a collision with a numeric suffix. Two checkouts
// of the same repository in different parents are a real case, and the second
// one has to be reachable as something.
func (r *Registry) uniqueSlugLocked(base string) string {
	if _, taken := r.bySlug[base]; !taken {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if _, taken := r.bySlug[candidate]; !taken {
			return candidate
		}
	}
}

// displayName is the basename of the loom's *parent* directory — bopos,
// easyloom — because that is the name a person would type. Every loom
// directory is called .loom, so the loom directory's own name identifies
// nothing.
func displayName(loom target.Target) string {
	name := filepath.Base(filepath.Dir(loom.Root))
	if name == "." || name == string(filepath.Separator) || name == "" {
		// A loom at the filesystem root has no parent to be named after.
		return "loom"
	}
	return name
}

// slugify reduces a display name to something safe in a path segment, without
// escaping: lowercase, and anything outside [a-z0-9] collapsed to a single
// hyphen. A name that survives unchanged — the usual case — stays recognisable
// in the URL bar.
func slugify(name string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			hyphen = false
		default:
			if !hyphen && b.Len() > 0 {
				b.WriteByte('-')
				hyphen = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "loom"
	}
	return slug
}
