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
// letsgo.mod pins the plugin; what to inject lives beside it in
// letsgo-env.mod, so that letsgo's own directives stay a closed set:
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
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/danielriddell21/letsgo-plugins/internal/hook"
)

// ConfigFile is read from the repository root, which is where letsgo runs a
// plugin from.
const ConfigFile = "letsgo-env.mod"

type input struct {
	Module string `json:"module"`
}

type output struct {
	LDFlags []string `json:"ldflags"`
}

// injection is one variable to fill from one environment variable.
type injection struct {
	Symbol string
	EnvVar string
	Line   int
}

func main() {
	hook.Main("ldflags", inject)
}

func inject(in input) (output, error) {
	injections, err := readConfig()
	if err != nil {
		return output{}, err
	}
	if len(injections) == 0 {
		return output{}, nil
	}

	var missing []string
	out := output{}

	for _, want := range injections {
		value, ok := os.LookupEnv(want.EnvVar)
		if !ok {
			missing = append(missing, want.EnvVar)
			continue
		}

		symbol, err := qualify(want.Symbol, in.Module)
		if err != nil {
			return output{}, fmt.Errorf("%s:%d: %w", ConfigFile, want.Line, err)
		}
		out.LDFlags = append(out.LDFlags, "-X", symbol+"="+value)
	}

	// Reported together, and as a failure: a release that quietly injects
	// empty strings produces binaries that are wrong in a way nothing checks.
	if len(missing) > 0 {
		sort.Strings(missing)
		return output{}, fmt.Errorf("%s is not set in the environment",
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

// readConfig parses ConfigFile.
//
// The same line-and-comment shape as letsgo.mod, and no more: this file says
// which variables to fill from where, and a config format that could say more
// than that would be a way to smuggle logic into a release.
func readConfig() ([]injection, error) {
	path := ConfigFile

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s: no such file; it is where this plugin reads what to inject", path)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var out []injection
	seen := map[string]int{}

	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if before, _, ok := strings.Cut(text, "//"); ok {
			text = strings.TrimSpace(before)
		}
		if text == "" {
			continue
		}

		fields := strings.Fields(text)
		if len(fields) != 3 || fields[0] != "inject" {
			return nil, fmt.Errorf("%s:%d: expected `inject <symbol> <ENV_VAR>`, got %q", path, line, text)
		}

		symbol, envVar := fields[1], fields[2]
		if first, repeated := seen[symbol]; repeated {
			return nil, fmt.Errorf("%s:%d: %s is already injected at line %d", path, line, symbol, first)
		}
		seen[symbol] = line
		out = append(out, injection{Symbol: symbol, EnvVar: envVar, Line: line})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}
