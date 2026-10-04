// Command letsgo-env compiles values from the environment into the binary.
//
// letsgo's own ldflags are literal, deliberately: a release should be a
// function of its commit. A build-time value read from the environment is not,
// so letsgo does not interpolate one.
//
// This plugin does, and makes the consequence explicit rather than hiding it.
// Every value it injects is written into letsgo.json alongside the artifact, so
// verification replays the recorded value and reproduces the binary byte for
// byte without this plugin and without the environment it ran in.
//
// That recording is not a leak. A value passed to -X is compiled into the
// binary and recoverable from a published artifact with `strings`, so anything
// injected this way was already public the moment it shipped. letsgo warns
// about that at plan time. If a value must stay secret, it belongs in the
// environment the program runs in, not in the program.
//
// # Configuration
//
// letsgo.mod pins the plugin; what to inject lives in .letsgo/env.mod, so
// that letsgo's own directives stay a closed set. A legacy letsgo-env.mod
// beside letsgo.mod is still read if .letsgo/env.mod does not exist.
//
//	inject internal/telemetry.otelEndpoint  OTEL_ENDPOINT
//	inject internal/telemetry.otelAuthToken OTEL_AUTH_TOKEN
//
// The symbol may be written relative to the module, as above, or fully
// qualified. Every named variable must be set in the environment: injecting an
// empty string silently is how a release ships a binary that cannot phone home
// and does not say why.
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/danielriddell21/letsgo/plugin"
)

// injection is one variable to fill from one environment variable.
type injection struct {
	Symbol string
	EnvVar string
	Line   int
	File   string
}

func main() {
	plugin.Main(plugin.HookLDFlags, inject)
}

func inject(in plugin.LDFlagsInput) (plugin.LDFlagsOutput, error) {
	injections, err := readConfig(in.ConfigDir)
	if err != nil {
		return plugin.LDFlagsOutput{}, err
	}
	if len(injections) == 0 {
		return plugin.LDFlagsOutput{}, nil
	}

	var missing []string
	out := plugin.LDFlagsOutput{}

	for _, want := range injections {
		value, ok := os.LookupEnv(want.EnvVar)
		if !ok {
			missing = append(missing, want.EnvVar)
			continue
		}

		symbol, err := qualify(want.Symbol, in.Module)
		if err != nil {
			return plugin.LDFlagsOutput{}, fmt.Errorf("%s:%d: %w", want.File, want.Line, err)
		}
		out.LDFlags = append(out.LDFlags, "-X", symbol+"="+value)
	}

	// Reported together, and as a failure: a release that quietly injects
	// empty strings produces binaries that are wrong in a way nothing checks.
	if len(missing) > 0 {
		sort.Strings(missing)
		return plugin.LDFlagsOutput{}, fmt.Errorf("%s is not set in the environment",
			strings.Join(missing, ", "))
	}
	return out, nil
}

// qualify turns a module-relative symbol into the form the linker needs.
func qualify(symbol, module string) (string, error) {
	i := strings.LastIndex(symbol, ".")
	if i <= 0 || i == len(symbol)-1 {
		return "", fmt.Errorf("%q must name a package and a variable", symbol)
	}

	pkg, name := symbol[:i], symbol[i+1:]
	if module == "" || pkg == module || strings.HasPrefix(pkg, module+"/") {
		return symbol, nil
	}
	return module + "/" + pkg + "." + name, nil
}

// readConfig reads the plugin's own config, which core locates and parses with
// letsgo.mod's grammar: the config dir first, then the legacy file at the
// repository root. This function adds only the vocabulary, and the rule that a
// symbol is injected once.
//
// This file says which variables to fill from where, and nothing more: a
// config format that could say more than that would be a way to smuggle logic
// into a release.
func readConfig(configDir string) ([]injection, error) {
	cfg, err := plugin.LoadConfig(configDir, "env")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w; it is where this plugin reads what to inject", err)
	}
	if err != nil {
		return nil, err
	}

	var out []injection
	seen := map[string]int{}

	for _, l := range cfg.Lines {
		if l.Keyword != "inject" || len(l.Args) != 2 {
			return nil, cfg.Errorf(l, "expected `inject <symbol> <ENV_VAR>`, got `%s`", strings.Join(append([]string{l.Keyword}, l.Args...), " "))
		}

		symbol, envVar := l.Args[0], l.Args[1]
		if first, repeated := seen[symbol]; repeated {
			return nil, cfg.Errorf(l, "%s is already injected at line %d", symbol, first)
		}
		seen[symbol] = l.Pos.Line
		out = append(out, injection{Symbol: symbol, EnvVar: envVar, Line: l.Pos.Line, File: cfg.Path})
	}
	return out, nil
}
