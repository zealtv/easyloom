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

	"github.com/charmbracelet/x/term"

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
	version bool
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) error {
	opts, err := parseFlags(args, stderr)
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
		if !openable(opts, getenv, err) {
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

// scanLooms walks a tree for deployed looms and reports them. It prints and
// exits rather than opening the UI: --scan answers "what is on this
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

// isTerminal reports whether w is a terminal this program can draw on. A
// redirected stdout gets one plain frame instead of an interactive program
// with no keyboard.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(f.Fd())
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("easyloom", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.scan, "scan", "", "list the looms under a directory and exit")
	flags.BoolVar(&opts.version, "version", false, "print the version and exit")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "usage: easyloom [flags] [path]         open a loom in the terminal")
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
