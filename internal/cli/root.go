// Package cli implements the callboard subcommands. Each command lives in its
// own file and registers itself in init() via register().
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/philband/callboard/internal/client"
	"github.com/philband/callboard/internal/config"
)

// Version is set by the linker at release time.
var Version = "dev"

// Exit codes shared by commands.
const (
	ExitOK      = 0
	ExitError   = 1
	ExitUsage   = 2
	ExitTimeout = 3 // wait: no message arrived
)

type command struct {
	name    string
	summary string
	run     func(args []string) int
}

var commands = map[string]*command{}

// register adds a subcommand. Call it from init().
func register(name, summary string, run func(args []string) int) {
	commands[name] = &command{name: name, summary: summary, run: run}
}

// Run dispatches os.Args[1:] and returns the exit code.
func Run(args []string) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(os.Stdout)
		return ExitOK
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "callboard: unknown command %q\n\n", args[0])
		usage(os.Stderr)
		return ExitUsage
	}
	return cmd.run(args[1:])
}

func usage(w io.Writer) {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "usage: callboard <command> [flags] [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands:")
	for _, n := range names {
		fmt.Fprintf(w, "  %-10s %s\n", n, commands[n].summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'callboard <command> -h' for flags. CALLBOARD_AS substitutes for --as.")
}

// newFlags returns a FlagSet that prints usage and returns ExitUsage on error.
func newFlags(name, synopsis string) *flag.FlagSet {
	fs := flag.NewFlagSet("callboard "+name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: callboard %s %s\n", name, synopsis)
		fs.PrintDefaults()
	}
	return fs
}

// parse runs fs.Parse and maps errors to an exit code. ok is false when the
// caller should return code immediately.
//
// Flags and positionals may be interleaved ("post --as a TITLE --body B"),
// which the standard library does not allow: parsing restarts after each
// positional. A bare "--" ends flag parsing; everything after it is
// positional. Afterwards fs.Args() holds the positionals in order.
func parse(fs *flag.FlagSet, args []string) (ok bool, code int) {
	var positionals, tail []string
	for i, a := range args {
		if a == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return false, ExitOK
			}
			return false, ExitUsage
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positionals = append(positionals, args[0])
		args = args[1:]
	}
	positionals = append(positionals, tail...)
	// Leave the positionals where commands expect them: fs.Args().
	_ = fs.Parse(append([]string{"--"}, positionals...))
	return true, ExitOK
}

// common holds flags every hub-facing command shares.
type common struct {
	json bool
	as   string
}

// bind registers -json and, when withAs is set, -as.
func (c *common) bind(fs *flag.FlagSet, withAs bool) {
	fs.BoolVar(&c.json, "json", false, "print JSON instead of text")
	if withAs {
		fs.StringVar(&c.as, "as", "", "your callsign (default $CALLBOARD_AS)")
	}
}

// callsign returns --as, falling back to CALLBOARD_AS.
func (c *common) callsign() (string, error) {
	if c.as != "" {
		return c.as, nil
	}
	if v := os.Getenv("CALLBOARD_AS"); v != "" {
		return v, nil
	}
	return "", errors.New("missing --as (or set CALLBOARD_AS); it is the callsign you got from checkin")
}

// connect returns a hub client, auto-spawning the hub when none is running.
func connect(ctx context.Context) (*client.Client, error) {
	return client.Connect(ctx, config.SocketPath())
}

// signalContext is cancelled on SIGINT/SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// fail prints err and returns the exit code for it.
func fail(err error) int {
	msg := err.Error()
	fmt.Fprintf(os.Stderr, "callboard: %s\n", strings.TrimSuffix(msg, "\n"))
	return ExitError
}
