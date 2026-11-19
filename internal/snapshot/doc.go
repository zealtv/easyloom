// Package snapshot executes `loom.sh map --json`, models the resulting
// projection, and caches it.
//
// The JSON projection is the only integration boundary: EasyLoom never walks
// .loom to derive state, and never writes to it directly.
package snapshot
