// Command letsgo-cask writes a Homebrew cask for a published release.
//
// letsgo writes formulas, not casks. A formula is the right shape for a
// command-line program, and letsgo's non-goals say a new publishing target
// needs a repository that actually wants one — which is exactly what a
// repository shipping a windowed build alongside its CLI is.
//
// # As a plugin
//
// Run with "tap-files" as its only argument, this answers letsgo's tap-files
// hook: letsgo asks what else belongs in the Homebrew tap, and this renders a
// cask from the artifacts, digests and URLs letsgo already built — the same
// facts a formula is written from. letsgo writes what comes back, through the
// same conditional write, the same author, and the same tap client and token
// split as the formula; this process never sees a token.
//
// Which variant the cask installs, and what token it is published under,
// live in .letsgo/cask.mod. A legacy letsgo-cask.mod beside letsgo.mod is
// still read if .letsgo/cask.mod does not exist:
//
//	variant gui
//	token gambit
//
// Both are optional. An unset variant means the release's own build, and an
// unset token defaults to the project's name, suffixed with the variant.
//
// # Standalone
//
// letsgo-cask also still reads a finished release on its own, for a
// repository that wants a cask without pinning the hook. It builds the same
// input the hook is handed, from the manifest, and renders it with the same
// code, so a variant is selected by the Variant letsgo recorded for each
// archive. A manifest from before letsgo v0.7.0 records none, so it is
// refused if that leaves two macOS builds for one architecture:
//
//	letsgo-cask --repo you/gambit --variant gui dist/letsgo.json
//
// The cask goes to stdout, or to the file named by -o. It cannot publish to a
// tap by itself any more: that is the hook's job now, through core's own
// tap client and credential.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	coremanifest "github.com/danielriddell21/letsgo/manifest"
	"github.com/danielriddell21/letsgo/plugin"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == string(plugin.HookTapFiles) {
		plugin.Main(plugin.HookTapFiles, answerTapFiles)
		return
	}
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "letsgo-cask:", err)
		os.Exit(1)
	}
}

func run(args []string, out *os.File) error {
	fs := flag.NewFlagSet("letsgo-cask", flag.ContinueOnError)
	repo := fs.String("repo", "", "owner/name the release was published under (required)")
	variant := fs.String("variant", "",
		"the variant whose archives the cask installs, as named in letsgo.mod")
	token := fs.String("token", "", "the cask's token; defaults to the archive's name")
	desc := fs.String("desc", "", "one-line description")
	homepage := fs.String("homepage", "", "defaults to the repository")
	license := fs.String("license", "", "SPDX licence identifier, as Homebrew spells it")
	caveats := fs.String("caveats", "", "text Homebrew prints after installing")
	output := fs.String("o", "", "write here instead of stdout")

	if err := fs.Parse(permute(fs, args)); err != nil {
		return fmt.Errorf("%s: %w", fs.Name(), err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("expected one argument, the path to letsgo.json")
	}
	if *repo == "" {
		return fmt.Errorf("--repo is required: letsgo.json does not record where it was published")
	}

	// Strict, because a cask names exact archives and digests: a schema this
	// build does not know is a reason to stop, not to render what decoded.
	m, err := coremanifest.ReadStrict(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("%s: %w", fs.Arg(0), err)
	}

	// The hook's question, asked about a release that already happened. What
	// the manifest cannot supply describes the program rather than its
	// archives, so it comes from the command line.
	in := plugin.TapFilesInputFromManifest(m, *repo, "", *caveats)
	in.Description = *desc
	in.License = *license
	if *homepage != "" {
		in.Homepage = *homepage
	}

	c, err := caskFromHookInput(in, pluginConfig{Variant: *variant, Token: *token})
	if err != nil {
		return err
	}

	rendered := c.render()

	if *output != "" {
		if err := os.WriteFile(*output, []byte(rendered), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", *output, err)
		}
		return nil
	}
	if _, err := out.WriteString(rendered); err != nil {
		return fmt.Errorf("writing the cask: %w", err)
	}
	return nil
}

// permute moves flags ahead of the file, because Go's flag package stops at
// the first operand and `letsgo-cask dist/letsgo.json --repo you/thing` is how
// anyone would type it.
func permute(fs *flag.FlagSet, args []string) []string {
	var flags, operands []string

	for i := 0; i < len(args); i++ {
		arg := args[i]

		// Everything after "--" is an operand by definition.
		if arg == "--" {
			return append(flags, append([]string{"--"}, append(operands, args[i+1:]...)...)...)
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			operands = append(operands, arg)
			continue
		}

		flags = append(flags, arg)

		// A flag that takes a value and was not written as -name=value
		// consumes the next argument.
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil && i+1 < len(args) {
			if _, isBool := f.Value.(interface{ IsBoolFlag() bool }); !isBool {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return append(flags, operands...)
}
