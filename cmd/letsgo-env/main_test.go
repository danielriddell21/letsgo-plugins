package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielriddell21/letsgo/plugin"
)

// writeConfig puts an env.mod in a temporary repository's .letsgo directory and
// returns that directory, which is what core hands the plugin as ConfigDir. No
// test changes the working directory.
func writeConfig(t *testing.T, body string) string {
	t.Helper()

	root := t.TempDir()
	dir := filepath.Join(root, ".letsgo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env.mod"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing env.mod: %v", err)
	}
	return dir
}

func TestInjectQualifiesModuleRelativeSymbols(t *testing.T) {
	configDir := writeConfig(t, "inject internal/telemetry.otelEndpoint OTEL_ENDPOINT\n")
	t.Setenv("OTEL_ENDPOINT", "https://otel.example")

	out, err := inject(plugin.LDFlagsInput{ConfigDir: configDir, Module: "github.com/danielriddell21/unum"})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}

	want := "github.com/danielriddell21/unum/internal/telemetry.otelEndpoint=https://otel.example"
	if len(out.LDFlags) != 2 || out.LDFlags[0] != "-X" || out.LDFlags[1] != want {
		t.Errorf("got %v, want [-X %s]", out.LDFlags, want)
	}
}

func TestInjectLeavesAFullyQualifiedSymbolAlone(t *testing.T) {
	configDir := writeConfig(t, "inject github.com/danielriddell21/unum/internal/build.Channel CHANNEL\n")
	t.Setenv("CHANNEL", "edge")

	out, err := inject(plugin.LDFlagsInput{ConfigDir: configDir, Module: "github.com/danielriddell21/unum"})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	if want := "github.com/danielriddell21/unum/internal/build.Channel=edge"; out.LDFlags[1] != want {
		t.Errorf("got %q, want %q", out.LDFlags[1], want)
	}
}

// The whole point of the plugin: an unset variable fails the release rather
// than compiling an empty string into the binary.
func TestInjectFailsOnAnUnsetVariable(t *testing.T) {
	configDir := writeConfig(t, "inject internal/telemetry.otelEndpoint OTEL_ENDPOINT\n")

	_, err := inject(plugin.LDFlagsInput{ConfigDir: configDir, Module: "example.com/m"})
	if err == nil {
		t.Fatal("an unset variable must fail the release")
	}
	if !strings.Contains(err.Error(), "OTEL_ENDPOINT") {
		t.Errorf("the error should name the variable, got %v", err)
	}
}

func TestInjectReportsEveryUnsetVariableAtOnce(t *testing.T) {
	configDir := writeConfig(t, "inject pkg.A FIRST\ninject pkg.B SECOND\n")

	_, err := inject(plugin.LDFlagsInput{ConfigDir: configDir, Module: "example.com/m"})
	if err == nil {
		t.Fatal("expected a failure")
	}
	for _, name := range []string{"FIRST", "SECOND"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%s missing from %v; fixing them one run at a time is the slow way", name, err)
		}
	}
}

func TestInjectAcceptsAnEmptyValueThatIsSet(t *testing.T) {
	configDir := writeConfig(t, "inject pkg.A OPTIONAL\n")
	t.Setenv("OPTIONAL", "")

	out, err := inject(plugin.LDFlagsInput{ConfigDir: configDir, Module: "example.com/m"})
	if err != nil {
		t.Fatalf("set-but-empty is a choice, not an omission: %v", err)
	}
	if want := "example.com/m/pkg.A="; out.LDFlags[1] != want {
		t.Errorf("got %q, want %q", out.LDFlags[1], want)
	}
}

func TestReadConfigSkipsBlankLinesAndComments(t *testing.T) {
	configDir := writeConfig(t, "// what the binary phones home to\n\ninject pkg.A FIRST // trailing\n")

	got, err := readConfig(configDir)
	if err != nil {
		t.Fatalf("readConfig: %v", err)
	}
	if len(got) != 1 || got[0].Symbol != "pkg.A" || got[0].EnvVar != "FIRST" {
		t.Fatalf("got %+v", got)
	}
	if got[0].Line != 3 {
		t.Errorf("line number should count the skipped lines, got %d", got[0].Line)
	}
}

func TestReadConfigRejectsADuplicateSymbol(t *testing.T) {
	configDir := writeConfig(t, "inject pkg.A FIRST\ninject pkg.A SECOND\n")

	if _, err := readConfig(configDir); err == nil {
		t.Error("two values for one symbol: the second silently wins, so say so instead")
	}
}

func TestReadConfigRejectsAMalformedLine(t *testing.T) {
	configDir := writeConfig(t, "set pkg.A FIRST\n")

	if _, err := readConfig(configDir); err == nil {
		t.Error("only `inject` is a directive here")
	}
}

func TestReadConfigSaysWhereItLooked(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), ".letsgo")

	_, err := readConfig(configDir)
	if err == nil {
		t.Fatal("expected a failure when the file is absent")
	}
	if !strings.Contains(err.Error(), "env.mod") {
		t.Errorf("the error should name the file, got %v", err)
	}
}

// .letsgo/env.mod is preferred over a legacy letsgo-env.mod when both exist,
// since that is where letsgo's own config-dir convention now points.
func TestReadConfigPrefersTheModernLocation(t *testing.T) {
	configDir := writeConfig(t, "inject pkg.A MODERN\n")
	legacy := filepath.Join(filepath.Dir(configDir), "letsgo-env.mod")
	if err := os.WriteFile(legacy, []byte("inject pkg.A LEGACY\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readConfig(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].EnvVar != "MODERN" {
		t.Errorf("got %+v, want the modern file's directive", got)
	}
}

// The legacy file beside letsgo.mod is still read when there is no .letsgo one.
func TestReadConfigFallsBackToTheLegacyFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "letsgo-env.mod"), []byte("inject pkg.A LEGACY\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readConfig(filepath.Join(root, ".letsgo"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].EnvVar != "LEGACY" {
		t.Errorf("got %+v, want the legacy file's directive", got)
	}
}

// Core's grammar is the plugin's grammar, so a block, a quoted argument and a
// syntax error behave as they do in letsgo.mod, with path:line:col errors.
func TestReadConfigSharesLetsgoModsGrammar(t *testing.T) {
	configDir := writeConfig(t, "inject (\n\tpkg.A FIRST\n\tpkg.B \"SECOND\"\n)\n")
	got, err := readConfig(configDir)
	if err != nil {
		t.Fatalf("a block of injections: %v", err)
	}
	if len(got) != 2 || got[0].EnvVar != "FIRST" || got[1].EnvVar != "SECOND" {
		t.Errorf("got %+v", got)
	}

	configDir = writeConfig(t, "inject (\n\tpkg.A FIRST\n")
	_, err = readConfig(configDir)
	if err == nil || !strings.Contains(err.Error(), "env.mod:1:") {
		t.Errorf("err = %v, want a path:line:col syntax error", err)
	}
}

func TestReadConfigPointsAtTheLineItRejects(t *testing.T) {
	configDir := writeConfig(t, "inject pkg.A FIRST\nset pkg.B SECOND\n")
	_, err := readConfig(configDir)
	if err == nil || !strings.Contains(err.Error(), "env.mod:2:1: expected `inject <symbol> <ENV_VAR>`") {
		t.Errorf("err = %v, want the second line named", err)
	}
}

// Core no longer falls back to the working directory: with no config dir given
// there is nowhere to look, and that is an error rather than a guess.
func TestReadConfigNeedsAConfigDir(t *testing.T) {
	if _, err := readConfig(""); err == nil {
		t.Error("an empty ConfigDir must be an error")
	}
}

func TestQualifyNeedsAPackageAndAVariable(t *testing.T) {
	for _, symbol := range []string{"bare", ".leading", "trailing."} {
		if _, err := qualify(symbol, "example.com/m"); err == nil {
			t.Errorf("%q is not a symbol", symbol)
		}
	}
}

func TestReadConfigReportsAnUnreadableFile(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), ".letsgo")
	// A directory where the file should be exists but cannot be read, which is
	// not the same as the file being absent.
	if err := os.MkdirAll(filepath.Join(configDir, "env.mod"), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := readConfig(configDir)
	if err == nil || !strings.Contains(err.Error(), "env.mod") {
		t.Errorf("readConfig() error = %v, want one naming env.mod", err)
	}
}
