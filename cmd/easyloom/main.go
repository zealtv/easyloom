// Command easyloom views and interacts with a loom.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/zealtv/easyloom/internal/server"
	"github.com/zealtv/easyloom/internal/snapshot"
	"github.com/zealtv/easyloom/internal/target"
	"github.com/zealtv/easyloom/internal/tui"
	"github.com/zealtv/easyloom/internal/warm"
)

// version is overridable at link time; otherwise it comes from the build info
// the toolchain stamps in.
var version = ""

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv); err != nil {
		if !errors.Is(err, flag.ErrHelp) && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "easyloom: %v\n", err)
		}
		os.Exit(1)
	}
}

type options struct {
	path    string
	scan    string
	port    int
	noOpen  bool
	dev     bool
	version bool
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) error {
	command, rest := splitCommand(args)
	opts, err := parseFlags(command, rest, stderr)
	if err != nil {
		return err
	}
	if opts.version {
		fmt.Fprintln(stdout, buildVersion())
		return nil
	}
	if opts.scan != "" {
		return scanLooms(ctx, opts.scan, stdout)
	}

	set := warm.NewSet(warm.Options{
		Getenv: getenv,
		Warn:   func(err error) { fmt.Fprintf(stderr, "warning: %v\n", err) },
	})
	defer set.Close()

	loom, source, err := locate(opts.path, getenv)
	if err != nil {
		// Nothing here and nothing asked for: offer the looms this machine
		// already knows instead of an error. An explicit path or $LOOM_DIR
		// that does not resolve is still an error — the user named a loom,
		// and a picker would be answering a different question.
		if !openable(opts, getenv, err) || command == commandWeb {
			return err
		}
		return pickAndRun(ctx, set, stdout, getenv, err)
	}

	// easyloom <path> executes a bash script from that path. Say which one,
	// before running it, so there is never any doubt about what was run.
	fmt.Fprintf(stdout, "loom     %s\n", loom.Root)
	fmt.Fprintf(stdout, "script   %s (via %s)\n", loom.Script, source)

	open, probeErr := set.Open(ctx, loom)
	if open == nil {
		return probeErr
	}
	probed := open.Version
	fmt.Fprintf(stdout, "tier     %s\n", describeTier(probed))

	if !open.Full() {
		return reportDegraded(stdout, loom, probed, probeErr)
	}
	if probed.Drift != nil {
		fmt.Fprintf(stderr, "warning: %v\n", probed.Drift)
	}

	state := open.Cache.Current()
	if state.Snapshot == nil {
		if state, err = open.Cache.RefreshNow(ctx); err != nil {
			return err
		}
	}
	if state.Snapshot == nil {
		return state.Err
	}

	if command == commandWeb {
		summarise(stdout, state)
		return serveWeb(ctx, opts, stdout, server.Loom{
			Target: loom, Version: probed, Cache: open.Cache, Mutator: open.Mutator,
		})
	}

	interactive := isTerminal(stdout)
	return tui.Run(ctx, tui.Options{
		Target:      loom,
		Version:     probed,
		Cache:       open.Cache,
		Mutator:     open.Mutator,
		Set:         set,
		Getenv:      getenv,
		Output:      stdout,
		Input:       os.Stdin,
		Colour:      interactive && getenv("NO_COLOR") == "",
		Interactive: interactive,
	})
}

// openable reports whether a failure to resolve a loom should open the picker
// rather than being reported.
//
// Only the empty-handed case qualifies: no path, no $LOOM_DIR, and a walk that
// simply found nothing. A named loom that does not resolve is a mistake worth
// hearing about, and a permission error is not answered by a list.
func openable(opts options, getenv func(string) string, err error) bool {
	if opts.path != "" || getenv("LOOM_DIR") != "" {
		return false
	}
	var notFound *target.NotFoundError
	return errors.As(err, &notFound)
}

// pickAndRun starts the terminal frontend with no loom open, so the picker is
// the program until one is chosen.
//
// A non-interactive stdout gets the original error instead: a picker with no
// keyboard would draw a list nobody can choose from.
func pickAndRun(ctx context.Context, set *warm.Set, stdout io.Writer, getenv func(string) string, unresolved error) error {
	if !isTerminal(stdout) {
		return unresolved
	}
	return tui.Run(ctx, tui.Options{
		Set:         set,
		Getenv:      getenv,
		Output:      stdout,
		Input:       os.Stdin,
		Colour:      getenv("NO_COLOR") == "",
		Interactive: true,
	})
}

// serveWeb runs the browser frontend until the user interrupts it. The
// deferred cache close in the caller runs after this returns, which is what
// kills a map subprocess still in flight at shutdown.
//
// The registry is seeded with the one loom this process resolved, and the loom
// lives in the URL from there on: the server holds no notion of a current one.
func serveWeb(ctx context.Context, opts options, stdout io.Writer, loom server.Loom) error {
	registry := server.NewRegistry()
	entry := registry.Add(loom)
	fmt.Fprintf(stdout, "slug     %s\n", entry.Slug)

	assets, err := server.Assets(opts.dev)
	if err != nil {
		return err
	}
	if opts.dev {
		fmt.Fprintln(stdout, "assets   web/ on disk (--dev): reload the page to pick up a change")
	}

	srv, err := server.New(server.Options{
		Port:    opts.port,
		Path:    server.FrontierPath(entry.Slug),
		Handler: server.NewRouter(server.RouterOptions{Registry: registry, Assets: assets}),
		Output:  stdout,
		NoOpen:  opts.noOpen,
	})
	if err != nil {
		return err
	}
	return srv.Serve(ctx)
}

// scanLooms walks a tree for deployed looms and reports them. It prints and
// exits rather than opening a frontend: --scan answers "what is on this
// machine", which is a question, not a destination.
//
// The walk itself lives in internal/target and returns values, because the
// picker calls it too — with an empty registry there is nothing else to show.
func scanLooms(ctx context.Context, root string, stdout io.Writer) error {
	var prober target.Prober
	found, err := target.Scan(ctx, root, target.ScanOptions{Prober: &prober})
	if err != nil {
		return err
	}

	abs, absErr := filepath.Abs(root)
	if absErr != nil {
		abs = root
	}
	fmt.Fprintf(stdout, "scanned  %s (depth %d)\n", abs, target.DefaultScanDepth)
	if len(found) == 0 {
		fmt.Fprintln(stdout, "found    no looms")
		return nil
	}
	fmt.Fprintf(stdout, "found    %s\n\n", plural(len(found), "loom"))

	for _, f := range found {
		tier := string(f.Version.Tier)
		if tier == "" {
			tier = "unknown"
		}
		fmt.Fprintf(stdout, "%-12s %s\n", tier, f.Target.Root)
		// A loom that would not probe is still worth listing — it is often the
		// reason someone is scanning — but say why it is not usable.
		if f.Err != nil {
			fmt.Fprintf(stdout, "%-12s %v\n", "", f.Err)
		}
	}
	return nil
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// commandWeb is the only subcommand. No subcommand starts the terminal
// frontend, which is the thing a loom is usually looked at with.
const commandWeb = "web"

// splitCommand separates a subcommand from the flags and path. It is
// deliberately not a flag: "easyloom web ." reads as a mode, and a mode that
// changes the whole frontend should not look like an option.
func splitCommand(args []string) (string, []string) {
	if len(args) > 0 && args[0] == commandWeb {
		return commandWeb, args[1:]
	}
	return "", args
}

// isTerminal reports whether w is a terminal this program can draw on. A
// redirected stdout gets one plain frame instead of an interactive program
// with no keyboard.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

func parseFlags(command string, args []string, stderr io.Writer) (options, error) {
	var opts options
	name := "easyloom"
	if command != "" {
		name += " " + command
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.scan, "scan", "", "list the looms under a directory and exit")
	flags.IntVar(&opts.port, "port", 0, "http port; 0 picks a free one and reports it")
	flags.BoolVar(&opts.noOpen, "no-open", false, "do not launch a browser")
	flags.BoolVar(&opts.dev, "dev", false, "serve the frontend from web/ on disk instead of the embedded copy")
	flags.BoolVar(&opts.version, "version", false, "print the version and exit")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: easyloom [flags] [path]         terminal frontend")
		fmt.Fprintln(stderr, "       easyloom web [flags] [path]     browser frontend")
		fmt.Fprintln(stderr, "       easyloom --scan <directory>     list the looms under a directory")
		fmt.Fprintln(stderr, "\npath is a repository, a .loom directory, or anything inside one.")
		fmt.Fprintln(stderr, "With no path, $LOOM_DIR is used, else the search walks up from the")
		fmt.Fprintln(stderr, "working directory.")
		fmt.Fprintln(stderr, "\nflags:")
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	switch flags.NArg() {
	case 0:
	case 1:
		opts.path = flags.Arg(0)
	default:
		flags.Usage()
		return opts, fmt.Errorf("one loom at a time: got %d paths", flags.NArg())
	}
	if opts.scan != "" && opts.path != "" {
		return opts, fmt.Errorf("--scan lists looms and exits; it takes no path as well")
	}
	if opts.port < 0 || opts.port > 65535 {
		return opts, fmt.Errorf("--port %d is not a port", opts.port)
	}
	return opts, nil
}

// locate applies the resolution order, git-style, first match wins: an
// explicit argument, then $LOOM_DIR, then a walk up from the working
// directory. It reports which of them produced the target so the user can see
// why this loom and not another.
func locate(path string, getenv func(string) string) (target.Target, string, error) {
	if path != "" {
		loom, err := target.Resolve(path)
		return loom, "the command line", err
	}
	if dir := getenv("LOOM_DIR"); dir != "" {
		loom, err := target.Resolve(dir)
		if err != nil {
			return loom, "$LOOM_DIR", fmt.Errorf("$LOOM_DIR: %w", err)
		}
		return loom, "$LOOM_DIR", nil
	}
	loom, err := target.Resolve("")
	return loom, "the working directory", err
}

func describeTier(v target.Version) string {
	switch v.Tier {
	case target.TierV2:
		return fmt.Sprintf("v2 (format %d, map schema %d)", v.FormatVersion, v.SchemaVersion)
	case target.TierV1:
		return "v1 — read-only"
	default:
		return fmt.Sprintf("unsupported (map schema %d)", v.SchemaVersion)
	}
}

// reportDegraded explains a target EasyLoom cannot drive fully. A v1 loom is a
// normal state of the world rather than a failure, so it exits zero.
func reportDegraded(stdout io.Writer, loom target.Target, v target.Version, err error) error {
	if v.Tier != target.TierV1 {
		return err
	}
	if v.Refusal != "" {
		fmt.Fprintf(stdout, "refusal  %s\n", v.Refusal)
	}
	fmt.Fprintln(stdout, "\nA v1 loom has no map command, so there is no projection to read.")
	fmt.Fprintf(stdout, "Inspect the migration first: %s migrate-v2 --dry-run\n", loom.Script)
	return nil
}

func summarise(w io.Writer, state snapshot.CacheState) {
	snap := state.Snapshot
	fmt.Fprintf(w, "snapshot generation %d, %d stitches, %s old\n",
		state.Generation, len(snap.Stitches), state.Age().Round(time.Millisecond))
	if state.Stale {
		fmt.Fprintf(w, "         stale: the last refresh failed: %v\n", state.Err)
	}

	fmt.Fprintf(w, "frontier %s\n", list(snap.Frontier))
	fmt.Fprintf(w, "queued   %s\n", list(queued(snap)))

	var errorCount, warningCount int
	for _, d := range snap.Diagnostics {
		if d.Severity == snapshot.SeverityError {
			errorCount++
			continue
		}
		warningCount++
	}
	if errorCount+warningCount > 0 {
		fmt.Fprintf(w, "health   %d error(s), %d warning(s)\n", errorCount, warningCount)
	}
}

// queued lists the queue in its own order, which the projection carries as a
// one-based position per stitch rather than as a list.
func queued(snap *snapshot.Snapshot) []string {
	ids := map[int]string{}
	highest := 0
	for _, st := range snap.Stitches {
		if st.QueuePosition == nil {
			continue
		}
		ids[*st.QueuePosition] = st.ID
		if *st.QueuePosition > highest {
			highest = *st.QueuePosition
		}
	}
	var out []string
	for position := 1; position <= highest; position++ {
		if id, ok := ids[position]; ok {
			out = append(out, id)
		}
	}
	return out
}

func list(ids []string) string {
	if len(ids) == 0 {
		return "(none)"
	}
	return strings.Join(ids, ", ")
}

func buildVersion() string {
	if version != "" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return "dev+" + setting.Value
		}
	}
	return "dev"
}
