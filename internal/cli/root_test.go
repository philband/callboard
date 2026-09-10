package cli

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func TestParseInterleaved(t *testing.T) {
	cases := []struct {
		args []string
		as   string
		body string
		json bool
		pos  []string
		ok   bool
		code int
	}{
		{[]string{"-as", "a", "title", "-body", "b"}, "a", "b", false, []string{"title"}, true, ExitOK},
		{[]string{"one", "two", "-json"}, "", "", true, []string{"one", "two"}, true, ExitOK},
		{[]string{"-as", "a", "--", "-not", "-a", "flag"}, "a", "", false, []string{"-not", "-a", "flag"}, true, ExitOK},
		{[]string{"x", "-bogus"}, "", "", false, nil, false, ExitUsage},
		{[]string{"-h"}, "", "", false, nil, false, ExitOK},
		{nil, "", "", false, []string{}, true, ExitOK},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		as := fs.String("as", "", "")
		body := fs.String("body", "", "")
		j := fs.Bool("json", false, "")
		ok, code := parse(fs, c.args)
		if ok != c.ok || code != c.code {
			t.Errorf("%v: ok=%v code=%d, want ok=%v code=%d", c.args, ok, code, c.ok, c.code)
			continue
		}
		if !ok {
			continue
		}
		if *as != c.as || *body != c.body || *j != c.json {
			t.Errorf("%v: as=%q body=%q json=%v", c.args, *as, *body, *j)
		}
		if got := fs.Args(); !reflect.DeepEqual(got, c.pos) {
			t.Errorf("%v: positionals %q, want %q", c.args, got, c.pos)
		}
	}
}
