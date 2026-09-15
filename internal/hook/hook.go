// Package hook is the plugin side of letsgo's plugin contract.
//
// The contract is deliberately small: letsgo runs a plugin with the hook's
// name as its only argument, writes one JSON object to its stdin, and reads
// one JSON object from its stdout. Nothing here is required to implement it —
// a plugin could be a shell script — but every plugin in this repository does
// the same three things, and doing them in one place keeps them consistent.
package hook

import (
	"encoding/json"
	"fmt"
	"os"
)

// Main runs a hook and exits.
//
// It is the whole of a plugin's main function: name the hook this program
// answers, and hand it a function from input to output.
func Main[In, Out any](name string, answer func(In) (Out, error)) {
	if err := run(name, answer); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", os.Args[0], err)
		os.Exit(1)
	}
}

func run[In, Out any](name string, answer func(In) (Out, error)) error {
	// letsgo passes the hook's name, so one binary could answer several. A
	// plugin asked for a hook it does not implement says so rather than
	// guessing, because guessing would mean answering the wrong question with
	// a plausible-looking result.
	if len(os.Args) != 2 {
		return fmt.Errorf("expected one argument, the hook to answer")
	}
	if os.Args[1] != name {
		return fmt.Errorf("this plugin answers the %s hook, not %s", name, os.Args[1])
	}

	var in In
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		return fmt.Errorf("reading the hook's input: %w", err)
	}

	out, err := answer(in)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
