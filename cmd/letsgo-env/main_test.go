package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfig puts a letsgo-env.mod in a temporary directory and makes it the
// working directory, which is where letsgo runs a plugin from.
func writeConfig(t *testing.T, body string) {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", ConfigFile, err)
	}
	t.Chdir(dir)
}

func TestInjectQualifiesModuleRelativeSymbols(t *testing.T) {
	writeConfig(t, "inject internal/telemetry.otelEndpoint OTEL_ENDPOINT\n")
	t.Setenv("OTEL_ENDPOINT", "https://otel.example")

	out, err := inject(input{Module: "github.com/danielriddell21/unum"})
	if err != nil {
		t.Fatalf("inject: %v", err)
	}

	want := "github.com/danielriddell21/unum/internal/telemetry.otelEndpoint=https://otel.example"
	if len(out.LDFlags) != 2 || out.LDFlags[0] != "-X" || out.LDFlags[1] != want {
		t.Errorf("got %v, want [-X %s]", out.LDFlags, want)
	}
}

func TestInjectLeavesAFullyQualifiedSymbolAlone(t *testing.T) {
	writeConfig(t, "inject github.com/danielriddell21/unum/internal/build.Channel CHANNEL\n")
	t.Setenv("CHANNEL", "edge")

	out, err := inject(input{Module: "github.com/danielriddell21/unum"})
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
	writeConfig(t, "inject internal/telemetry.otelEndpoint OTEL_ENDPOINT\n")

	_, err := inject(input{Module: "example.com/m"})
	if err == nil {
		t.Fatal("an unset variable must fail the release")
	}
	if !strings.Contains(err.Error(), "OTEL_ENDPOINT") {
		t.Errorf("the error should name the variable, got %v", err)
	}
}

func TestInjectReportsEveryUnsetVariableAtOnce(t *testing.T) {
	writeConfig(t, "inject pkg.A FIRST\ninject pkg.B SECOND\n")

	_, err := inject(input{Module: "example.com/m"})
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
	writeConfig(t, "inject pkg.A OPTIONAL\n")
	t.Setenv("OPTIONAL", "")

	out, err := inject(input{Module: "example.com/m"})
	if err != nil {
		t.Fatalf("set-but-empty is a choice, not an omission: %v", err)
	}
	if want := "example.com/m/pkg.A="; out.LDFlags[1] != want {
		t.Errorf("got %q, want %q", out.LDFlags[1], want)
	}
}

func TestReadConfigSkipsBlankLinesAndComments(t *testing.T) {
	writeConfig(t, "// what the binary phones home to\n\ninject pkg.A FIRST // trailing\n")

	got, err := readConfig()
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
	writeConfig(t, "inject pkg.A FIRST\ninject pkg.A SECOND\n")

	if _, err := readConfig(); err == nil {
		t.Error("two values for one symbol: the second silently wins, so say so instead")
	}
}

func TestReadConfigRejectsAMalformedLine(t *testing.T) {
	writeConfig(t, "set pkg.A FIRST\n")

	if _, err := readConfig(); err == nil {
		t.Error("only `inject` is a directive here")
	}
}

func TestReadConfigSaysWhereItLooked(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := readConfig()
	if err == nil {
		t.Fatal("expected a failure when the file is absent")
	}
	if !strings.Contains(err.Error(), ConfigFile) {
		t.Errorf("the error should name the file, got %v", err)
	}
}

func TestQualifyNeedsAPackageAndAVariable(t *testing.T) {
	for _, symbol := range []string{"bare", ".leading", "trailing."} {
		if _, err := qualify(symbol, "example.com/m"); err == nil {
			t.Errorf("%q is not a symbol", symbol)
		}
	}
}
