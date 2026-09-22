package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// tapServer answers the two contents calls publishing makes, recording the
// write. existing is the file already in the tap, or "" for none.
type tapServer struct {
	existing string

	path    string
	method  string
	auth    string
	message string
	sha     string
	content string
}

func (ts *tapServer) start(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.path, ts.auth = r.URL.Path, r.Header.Get("Authorization")
		if r.Method == http.MethodGet {
			if ts.existing == "" {
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"sha":      "existing-sha",
				"encoding": "base64",
				"content":  base64.StdEncoding.EncodeToString([]byte(ts.existing)),
			})
			return
		}

		ts.method = r.Method
		var body struct {
			Message string `json:"message"`
			Content string `json:"content"`
			SHA     string `json:"sha"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil {
			t.Fatal(err)
		}
		ts.message, ts.sha, ts.content = body.Message, body.SHA, string(decoded)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func publishCask(t *testing.T, ts *tapServer, args ...string) {
	t.Helper()
	full := append(args,
		"--repo", "you/gambit", "--variant", "gui",
		"--tap", "you/tap", "--tap-token", "tap-token", "--tap-api", ts.start(t),
		manifestFile(t, published))
	if err := run(full, os.Stdout); err != nil {
		t.Fatal(err)
	}
}

// The whole point of #22: the cask reaches the tap without a checkout and
// without a shell commit.
func TestCaskPublishesToTheTap(t *testing.T) {
	ts := &tapServer{}
	publishCask(t, ts)

	if want := "/repos/you/homebrew-tap/contents/Casks/gambit-gui.rb"; ts.path != want {
		t.Errorf("path = %q, want %q", ts.path, want)
	}
	if ts.method != http.MethodPut {
		t.Errorf("method = %q, want PUT", ts.method)
	}
	if ts.auth != "Bearer tap-token" {
		t.Errorf("Authorization = %q", ts.auth)
	}
	if ts.sha != "" {
		t.Errorf("SHA = %q, want empty for a file that was not there", ts.sha)
	}
	// Matches the formula's message, so a tap's history reads the same way
	// whichever letsgo wrote the entry.
	if want := "gambit-gui 1.4.0"; ts.message != want {
		t.Errorf("message = %q, want %q", ts.message, want)
	}
	if !strings.Contains(ts.content, `cask "gambit-gui" do`) {
		t.Errorf("published:\n%s", ts.content)
	}
}

// The conditional write: replacing a file carries the SHA that was read, so a
// racing change fails the write rather than being clobbered by it.
func TestCaskReplacesWithTheSHAItRead(t *testing.T) {
	ts := &tapServer{existing: "# an older cask\n"}
	publishCask(t, ts)

	if ts.sha != "existing-sha" {
		t.Errorf("SHA = %q, want existing-sha", ts.sha)
	}
}

// Re-running a release must not leave a commit in somebody else's repository
// saying nothing happened.
func TestCaskDoesNotRepublishAnIdenticalFile(t *testing.T) {
	rendered := generate(t, "--repo", "you/gambit", "--variant", "gui", manifestFile(t, published))

	ts := &tapServer{existing: rendered}
	publishCask(t, ts)

	if ts.method != "" {
		t.Errorf("wrote %q, want no write at all", ts.method)
	}
}

func TestCaskPathCanBeOverridden(t *testing.T) {
	ts := &tapServer{}
	publishCask(t, ts, "--tap-path", "Casks/g/gambit-gui.rb")

	if want := "/repos/you/homebrew-tap/contents/Casks/g/gambit-gui.rb"; ts.path != want {
		t.Errorf("path = %q, want %q", ts.path, want)
	}
}

func TestCaskRefusesToPublishWithoutAToken(t *testing.T) {
	t.Setenv(tapTokenEnv, "")

	err := run([]string{
		"--repo", "you/gambit", "--variant", "gui", "--tap", "you/tap",
		manifestFile(t, published),
	}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), tapTokenEnv) {
		t.Errorf("err = %v, want one naming %s", err, tapTokenEnv)
	}
}

// The token comes from the environment when the flag is absent, spelled the
// same way letsgo spells it.
func TestCaskReadsTheTokenFromTheEnvironment(t *testing.T) {
	ts := &tapServer{}
	t.Setenv(tapTokenEnv, "from-env")

	err := run([]string{
		"--repo", "you/gambit", "--variant", "gui", "--tap", "you/tap",
		"--tap-api", ts.start(t), manifestFile(t, published),
	}, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if ts.auth != "Bearer from-env" {
		t.Errorf("Authorization = %q", ts.auth)
	}
}

// Rendering to stdout stays the default, so the renderer is still usable on
// its own for inspection.
func TestCaskWithoutATapStillRenders(t *testing.T) {
	got := generate(t, "--repo", "you/gambit", "--variant", "gui", manifestFile(t, published))
	if !strings.Contains(got, `cask "gambit-gui" do`) {
		t.Errorf("rendered:\n%s", got)
	}
}
