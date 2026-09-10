package cli

import "fmt"

func init() {
	register("version", "print the callboard version", runVersion)
}

func runVersion(args []string) int {
	fs := newFlags("version", "")
	var c common
	c.bind(fs, false)
	if ok, code := parse(fs, args); !ok {
		return code
	}
	if c.json {
		printJSON(map[string]string{"version": Version})
		return ExitOK
	}
	fmt.Printf("callboard %s\n", Version)
	return ExitOK
}
