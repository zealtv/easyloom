// Package userconfig resolves EasyLoom's per-user configuration directory.
//
// It is the first thing EasyLoom keeps outside a repository. The registry of
// known looms and the agent profiles both live there, and neither owns the
// directory, so resolving it is a job of its own.
//
// Resolving is not creating: asking where a file would be must not litter a
// directory on a machine that never saves one. Callers that are about to write
// call [Ensure] first.
package userconfig
