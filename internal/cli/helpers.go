package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// exitSignal is the conventional shell exit code for death by SIGINT.
const exitSignal = 130

// usageErr reports a bad invocation: a message, the command usage, ExitUsage.
func usageErr(fs *flag.FlagSet, format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "callboard: "+format+"\n", a...)
	fs.Usage()
	return ExitUsage
}

// warn writes a diagnostic line to stderr without changing the exit code.
func warn(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", a...)
}

// stringList collects a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	if v == "" {
		return fmt.Errorf("empty value")
	}
	*l = append(*l, v)
	return nil
}

// readBodyArg returns the joined positional args as a message body. With no
// args it reads stdin, so agents can pipe multi-line text.
func readBodyArg(args []string) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	return readStdin()
}

// dashStdin resolves a flag value of "-" to the contents of stdin.
func dashStdin(v string) (string, error) {
	if v != "-" {
		return v, nil
	}
	return readStdin()
}

func readStdin() (string, error) {
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// plural renders a count with its noun: "1 message", "2 messages".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// shortDur prints whole hours and minutes as "10m" rather than "10m0s".
func shortDur(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	case d >= time.Minute && d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return d.String()
}

// firstLine returns everything before the first newline.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// truncate shortens s to at most n runes, marking the cut with "...".
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}
