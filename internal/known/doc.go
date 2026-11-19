// Package known remembers the looms this machine has opened.
//
// It is a convenience cache of paths, in most-recently-used order, persisted
// as registry.json in the user configuration directory. Nothing in it cannot
// be rebuilt by a scan, which is why a corrupt or unreadable file is a warning
// and an empty list rather than a failure to start.
//
// Deliberately not named Registry: internal/server has one of those, and it is
// a different thing — an in-process map of URL slugs to open looms, with no
// persistence and no ordering.
package known
