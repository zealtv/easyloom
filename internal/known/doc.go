// Package known remembers the looms this machine has opened.
//
// It is a convenience cache of paths, in most-recently-used order, persisted
// as registry.json in the user configuration directory. Nothing in it cannot
// be rebuilt by a scan, which is why a corrupt or unreadable file is a warning
// and an empty list rather than a failure to start.
package known
