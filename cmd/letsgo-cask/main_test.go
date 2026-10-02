package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielriddell21/letsgo/plugin"
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

func TestCaskCarriesTheLicence(t *testing.T) {
	got := generate(t, "--repo", "you/gambit", "--license", "MIT", manifestFile(t, published))

	if !strings.Contains(got, "  license \"MIT\"\n") {
		t.Errorf("licence missing from:\n%s", got)
	}
}

func TestCaskOmitsAnEmptyLicence(t *testing.T) {
	got := generate(t, "--repo", "you/gambit", manifestFile(t, published))

	if strings.Contains(got, "license") {
		t.Errorf("an unset licence should say nothing rather than claim one:\n%s", got)
	}
}

// Caveats are the one field a user actually reads after installing, and they
// are routinely several lines, so they render as a heredoc.
func TestCaskCarriesMultiLineCaveats(t *testing.T) {
	got := generate(t, "--repo", "you/gambit",
		"--caveats", "gambit opens a window and is macOS-only.\nOn Linux, run it in the terminal.",
		manifestFile(t, published))

	want := "  caveats <<~EOS\n" +
		"    gambit opens a window and is macOS-only.\n" +
		"    On Linux, run it in the terminal.\n" +
		"  EOS\n"
	if !strings.Contains(got, want) {
		t.Errorf("caveats missing or misindented:\n%s", got)
	}
	if !strings.Contains(got, "  EOS\nend\n") {
		t.Errorf("caveats should be the last stanza:\n%s", got)
	}
}

func TestCaskOmitsEmptyCaveats(t *testing.T) {
	got := generate(t, "--repo", "you/gambit", manifestFile(t, published))

	if strings.Contains(got, "caveats") {
		t.Errorf("no caveats means no stanza:\n%s", got)
	}
}

// A malformed tap is no longer this program's problem: it never sees one.
func TestCaskWithoutATapStillRenders(t *testing.T) {
	got := generate(t, "--repo", "you/gambit", "--variant", "gui", manifestFile(t, published))
	if !strings.Contains(got, `cask "gambit-gui" do`) {
		t.Errorf("rendered:\n%s", got)
	}
}

// The hook path: every digest and URL already comes from core, so the
// release's own archives and a variant's are told apart by Variant alone,
// never by re-parsing the archive's name.
func TestAnswerTapFilesRendersTheVariantsArchives(t *testing.T) {
	in := plugin.TapFilesInput{
		Project: "gambit", Version: "1.4.0", Tag: "v1.4.0",
		Description: "a gambit", License: "MIT", Homepage: "https://example.com/gambit",
		Artifacts: []plugin.TapArtifact{
			{
				Archive: "gambit_1.4.0_darwin_arm64.tar.gz", OS: "darwin", Arch: "arm64",
				SHA256: "base-arm", URL: "https://dl.example.com/gambit_1.4.0_darwin_arm64.tar.gz",
				Binaries: []string{"gambit"},
			},
			{
				Archive: "gambit-gui_1.4.0_darwin_arm64.tar.gz", Variant: "gui", OS: "darwin", Arch: "arm64",
				SHA256: "gui-arm", URL: "https://dl.example.com/gambit-gui_1.4.0_darwin_arm64.tar.gz",
				Binaries: []string{"gambit-gui"},
			},
		},
	}

	out, err := answerTapFiles(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 1 || out.Files[0].Path != "Casks/gambit.rb" {
		t.Fatalf("files = %+v", out.Files)
	}

	rendered := out.Files[0].Content
	for _, want := range []string{
		`cask "gambit" do`, `sha256 "base-arm"`, `desc "a gambit"`,
		`license "MIT"`, `homepage "https://example.com/gambit"`, `binary "gambit"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered cask is missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "gui-arm") {
		t.Errorf("the release's own cask installed the variant's archive:\n%s", rendered)
	}
}

func TestAnswerTapFilesUsesTheConfiguredVariantAndToken(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("letsgo-cask.mod", []byte("variant gui\ntoken gambit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	in := plugin.TapFilesInput{
		Project: "gambit", Version: "1.4.0",
		Artifacts: []plugin.TapArtifact{
			{
				Archive: "gambit-gui_1.4.0_darwin_arm64.tar.gz", Variant: "gui", OS: "darwin", Arch: "arm64",
				SHA256: "gui-arm", URL: "https://dl.example.com/gambit-gui_1.4.0_darwin_arm64.tar.gz",
				Binaries: []string{"gambit-gui"},
			},
		},
	}

	out, err := answerTapFiles(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Files) != 1 || out.Files[0].Path != "Casks/gambit.rb" {
		t.Fatalf("files = %+v, want Casks/gambit.rb", out.Files)
	}
}

func TestAnswerTapFilesRefusesWhatItCannotBuild(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := answerTapFiles(plugin.TapFilesInput{Project: "gambit", Version: "1.4.0"})
	if err == nil || !strings.Contains(err.Error(), "no macOS build") {
		t.Errorf("err = %v, want a refusal naming the missing macOS build", err)
	}
}

func TestReadPluginConfigParsesVariantAndToken(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("letsgo-cask.mod", []byte("// a comment\nvariant gui\ntoken gambit\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := readPluginConfig(".letsgo")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Variant != "gui" || cfg.Token != "gambit" {
		t.Errorf("cfg = %+v", cfg)
	}
}

// No letsgo-cask.mod at all is a normal thing for a release with one darwin
// build and no variant, and must not fail.
func TestReadPluginConfigToleratesNoFile(t *testing.T) {
	t.Chdir(t.TempDir())

	cfg, err := readPluginConfig(".letsgo")
	if err != nil || cfg != (pluginConfig{}) {
		t.Errorf("readPluginConfig() = %+v, %v, want a zero config and no error", cfg, err)
	}
}

func TestReadPluginConfigRejectsAMalformedLine(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("letsgo-cask.mod", []byte("variant\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readPluginConfig(".letsgo"); err == nil {
		t.Error("a line missing its value should not silently parse")
	}
}

func TestReadPluginConfigRejectsAnUnknownDirective(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("letsgo-cask.mod", []byte("colour blue\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readPluginConfig(".letsgo"); err == nil || !strings.Contains(err.Error(), "colour") {
		t.Errorf("err = %v, want one naming the unknown directive", err)
	}
}

func TestReadPluginConfigRejectsADuplicateDirective(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("letsgo-cask.mod", []byte("variant gui\nvariant pro\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readPluginConfig(".letsgo"); err == nil || !strings.Contains(err.Error(), "already set") {
		t.Errorf("err = %v, want one naming the repeated directive", err)
	}
}

// .letsgo/cask.mod is preferred over a legacy letsgo-cask.mod when both
// exist, since that is where letsgo's own config-dir convention now points.
func TestReadPluginConfigPrefersTheModernLocation(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(".letsgo", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".letsgo/cask.mod", []byte("variant gui\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("letsgo-cask.mod", []byte("variant pro\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := readPluginConfig(".letsgo")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Variant != "gui" {
		t.Errorf("cfg = %+v, want the modern file's variant", cfg)
	}
}

// A nested module's tag carries its directory; the download URL keeps the
// slashes, which is how the forge names such a release.
func TestCaskKeepsAScopedTagInTheDownloadURL(t *testing.T) {
	scoped := strings.Replace(published, `"tag": "v1.4.0"`, `"tag": "services/api/v1.4.0"`, 1)
	got := generate(t, "--repo", "you/gambit", manifestFile(t, scoped))

	want := "https://github.com/you/gambit/releases/download/services/api/v1.4.0/gambit_1.4.0_darwin_arm64.tar.gz"
	if !strings.Contains(got, want) {
		t.Errorf("the cask is missing %q:\n%s", want, got)
	}
}

func TestDownloadURL(t *testing.T) {
	tests := []struct {
		name, tag, asset, want string
	}{
		{"root tag", "v1.4.0", "gambit_1.4.0_darwin_arm64.tar.gz", "https://github.com/you/gambit/releases/download/v1.4.0/gambit_1.4.0_darwin_arm64.tar.gz"},
		{"scoped tag keeps its slashes", "services/api/v1.4.0", "a.tar.gz", "https://github.com/you/gambit/releases/download/services/api/v1.4.0/a.tar.gz"},
		{"a character a URL cannot carry is escaped", "v1.4.0+build 1", "a b#c.tar.gz", "https://github.com/you/gambit/releases/download/v1.4.0+build%201/a%20b%23c.tar.gz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := downloadURL("you/gambit", tt.tag, tt.asset); got != tt.want {
				t.Errorf("downloadURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A project named after its own version must not be cut at the first
// "_<version>_": core strips exactly the suffix the builder appended.
func TestCaskReadsABaseNameThatContainsTheVersion(t *testing.T) {
	odd := strings.NewReplacer("gambit_1.4.0_", "gambit_1.4.0_x_1.4.0_", "\"project\": \"gambit\"", "\"project\": \"gambit_1.4.0_x\"").Replace(published)
	got := generate(t, "--repo", "you/gambit", manifestFile(t, odd))
	if !strings.Contains(got, "gambit_1.4.0_x_1.4.0_darwin_arm64.tar.gz") {
		t.Errorf("the cask lost the archive:\n%s", got)
	}
}

func TestReadPluginConfigReportsAnUnreadableFile(t *testing.T) {
	t.Chdir(t.TempDir())
	// A directory where the file should be exists but cannot be read, which is
	// not the same as the file being absent.
	if err := os.Mkdir("letsgo-cask.mod", 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := readPluginConfig(".letsgo")
	if err == nil || !strings.Contains(err.Error(), "letsgo-cask.mod") {
		t.Errorf("readPluginConfig() error = %v, want one naming letsgo-cask.mod", err)
	}
}
