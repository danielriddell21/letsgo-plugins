package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const published = `{
  "schema": 1,
  "project": "gambit",
  "version": "1.4.0",
  "tag": "v1.4.0",
  "artifacts": [
    {"name":"gambit_1.4.0_darwin_arm64.tar.gz","os":"darwin","arch":"arm64","sha256":"base-arm","binary":"gambit"},
    {"name":"gambit_1.4.0_linux_amd64.tar.gz","os":"linux","arch":"amd64","sha256":"base-linux","binary":"gambit"},
    {"name":"gambit-gui_1.4.0_darwin_arm64.tar.gz","os":"darwin","arch":"arm64","sha256":"gui-arm","binary":"gambit-gui"},
    {"name":"gambit-gui_1.4.0_darwin_amd64.tar.gz","os":"darwin","arch":"amd64","sha256":"gui-intel","binary":"gambit-gui"}
  ]
}`

func manifestFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "letsgo.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func generate(t *testing.T, args ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "cask.rb")

	if err := run(append(args, "-o", out), os.Stdout); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The variant's archives, not the release's own: both are macOS builds of the
// same project, and only the suffix tells them apart.
func TestCaskInstallsTheVariantsArchives(t *testing.T) {
	got := generate(t, "--repo", "you/gambit", "--variant", "gui", manifestFile(t, published))

	for _, want := range []string{
		`cask "gambit-gui" do`,
		`version "1.4.0"`,
		`gambit-gui_1.4.0_darwin_arm64.tar.gz`,
		`sha256 "gui-arm"`,
		`gambit-gui_1.4.0_darwin_amd64.tar.gz`,
		`sha256 "gui-intel"`,
		`binary "gambit-gui"`,
		`homepage "https://github.com/you/gambit"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the cask is missing %q:\n%s", want, got)
		}
	}

	// The release's own archives belong to the formula, not the cask.
	if strings.Contains(got, `sha256 "base-arm"`) {
		t.Errorf("the cask installs the release's own build:\n%s", got)
	}
}

func TestCaskWithoutAVariantUsesTheReleasesOwnArchives(t *testing.T) {
	got := generate(t, "--repo", "you/gambit", manifestFile(t, published))

	if !strings.Contains(got, `cask "gambit" do`) || !strings.Contains(got, `sha256 "base-arm"`) {
		t.Errorf("cask = %s", got)
	}
	// Linux is not a thing a cask installs.
	if strings.Contains(got, "linux") {
		t.Errorf("the cask names a linux archive:\n%s", got)
	}
}

func TestCaskRefusesWhatItCannotBuild(t *testing.T) {
	path := manifestFile(t, published)

	tests := []struct {
		name, want string
		args       []string
	}{
		{"no repository", "--repo is required", []string{path}},
		{
			"a variant with no macOS build",
			"no macOS archive",
			[]string{"--repo", "you/gambit", "--variant", "nope", path},
		},
		{"no manifest named", "expected one argument", []string{"--repo", "you/gambit"}},
	}

	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			err := run(c.args, os.Stdout)
			if err == nil {
				t.Fatal("this should have been refused")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to mention %q", err, c.want)
			}
		})
	}
}

// A release published by a later letsgo must still produce a cask, so unknown
// fields are ignored and only the schema is insisted on.
func TestCaskRejectsOnlyAnUnknownSchema(t *testing.T) {
	future := strings.Replace(published, `"schema": 1`, `"schema": 1, "invented_later": {"a": 1}`, 1)
	if got := generate(t, "--repo", "you/gambit", manifestFile(t, future)); !strings.Contains(got, "cask") {
		t.Errorf("a manifest with unknown fields did not produce a cask:\n%s", got)
	}

	old := strings.Replace(published, `"schema": 1`, `"schema": 99`, 1)
	if err := run([]string{"--repo", "you/gambit", manifestFile(t, old)}, os.Stdout); err == nil ||
		!strings.Contains(err.Error(), "schema") {
		t.Errorf("err = %v", err)
	}
}

func TestBaseStripsTheVersionAndPlatform(t *testing.T) {
	for archive, want := range map[string]string{
		"gambit_1.4.0_darwin_arm64.tar.gz":     "gambit",
		"gambit-gui_1.4.0_darwin_amd64.tar.gz": "gambit-gui",
		"tool_1.0.0_windows_amd64.zip":         "tool",
		"unrelated.tar.gz":                     "",
	} {
		version := "1.4.0"
		if strings.Contains(archive, "1.0.0") {
			version = "1.0.0"
		}
		if got := base(archive, version); got != want {
			t.Errorf("base(%q) = %q, want %q", archive, got, want)
		}
	}
}
