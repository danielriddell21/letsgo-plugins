// Command letsgo-cask writes a Homebrew cask for a published release.
//
// letsgo writes formulas, not casks. A formula is the right shape for a
// command-line program, and letsgo's non-goals say a new publishing target
// needs a repository that actually wants one — which is exactly what a
// repository shipping a windowed build alongside its CLI is.
//
// This needs nothing from letsgo. It reads the letsgo.json a release already
// published and writes a cask from it, so there is no hook, no pin, and
// nothing it can do to the bytes: by the time it runs, the release is over.
// That is the whole reason it lives outside.
//
// # Usage
//
//	letsgo-cask --repo you/gambit --variant gui dist/letsgo.json
//	letsgo-cask --repo you/gambit --tap you/tap dist/letsgo.json
//
// The cask goes to stdout, or to the file named by -o, or — with --tap — to
// the tap itself.
//
// Publishing used to be left to git, on the reasoning that a renderer should
// render. What that cost was a checkout of somebody else's repository to write
// one file, and a `git push` in place of a conditional write: the contents API
// takes the blob SHA of the file being replaced, so a racing write fails
// rather than clobbers, and no clone is needed to get one file in. Rendering
// is still deterministic and an unchanged cask is still not committed, so
// re-running a release does not churn the tap.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/danielriddell21/letsgo-plugins/internal/tap"
)

// tapTokenEnv is the environment variable letsgo reads for the same
// credential. Spelling it the same way means one token in the workflow reaches
// the formula and the cask alike.
const tapTokenEnv = "LETSGO_TAP_TOKEN"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "letsgo-cask:", err)
		os.Exit(1)
	}
}

// manifest is the part of letsgo.json this needs. Deliberately a subset:
// unknown fields are ignored, so a release published by a later letsgo still
// produces a cask.
type manifest struct {
	Schema    int        `json:"schema"`
	Project   string     `json:"project"`
	Version   string     `json:"version"`
	Tag       string     `json:"tag"`
	Artifacts []artifact `json:"artifacts"`
}

type artifact struct {
	Name     string   `json:"name"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	SHA256   string   `json:"sha256"`
	Binary   string   `json:"binary"`
	Binaries []binary `json:"binaries"`
}

type binary struct {
	Name string `json:"name"`
}

// executables returns the archive's binaries, whichever way it spells them.
func (a artifact) executables() []string {
	if len(a.Binaries) > 0 {
		out := make([]string, len(a.Binaries))
		for i, b := range a.Binaries {
			out[i] = b.Name
		}
		return out
	}
	if a.Binary == "" {
		return nil
	}
	return []string{a.Binary}
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
	tapRepo := fs.String("tap", "", "publish to this Homebrew tap, as owner/repo")
	tapToken := fs.String("tap-token", "", "token the tap is written with (default: $"+tapTokenEnv+")")
	tapPath := fs.String("tap-path", "", "path within the tap (default: Casks/<token>.rb)")
	tapAPI := fs.String("tap-api", "", "forge API host (default: GitHub's; set it for GitHub Enterprise)")

	if err := fs.Parse(permute(fs, args)); err != nil {
		return fmt.Errorf("%s: %w", fs.Name(), err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("expected one argument, the path to letsgo.json")
	}
	if *repo == "" {
		return fmt.Errorf("--repo is required: letsgo.json does not record where it was published")
	}

	m, err := read(fs.Arg(0))
	if err != nil {
		return err
	}

	c, err := build(m, *repo, *variant, caskFields{
		Token:    *token,
		Desc:     *desc,
		Homepage: *homepage,
		License:  *license,
		Caveats:  *caveats,
	})
	if err != nil {
		return err
	}

	rendered := c.render()

	// -o and --tap are independent: writing the file locally as well as
	// publishing it is how a run is inspected after the fact.
	if *output != "" {
		if err := os.WriteFile(*output, []byte(rendered), 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", *output, err)
		}
	}
	if *tapRepo != "" {
		return publish(context.Background(), c, rendered, tapOptions{
			Repo: *tapRepo, Token: *tapToken, Path: *tapPath, API: *tapAPI,
		}, out)
	}
	if *output != "" {
		return nil
	}
	if _, err := out.WriteString(rendered); err != nil {
		return fmt.Errorf("writing the cask: %w", err)
	}
	return nil
}

// tapOptions is where the cask goes and what it is written with.
type tapOptions struct {
	Repo  string
	Token string
	Path  string
	API   string
}

// publish writes the rendered cask to the tap.
func publish(ctx context.Context, c *cask, rendered string, o tapOptions, out *os.File) error {
	target, err := tap.Parse(o.Repo)
	if err != nil {
		return err
	}

	token := o.Token
	if token == "" {
		token = os.Getenv(tapTokenEnv)
	}
	if token == "" {
		return fmt.Errorf("no token for %s; set $%s, or pass --tap-token", target, tapTokenEnv)
	}

	where := o.Path
	if where == "" {
		where = path.Join("Casks", c.Token+".rb")
	}

	client := tap.NewClient(token)
	if o.API != "" {
		client.SetEndpoint(o.API)
	}

	status, err := tap.Publish(ctx, client, target,
		where, fmt.Sprintf("%s %s", c.Token, c.Version), []byte(rendered))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s in %s\n", status, where, target)
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

func read(path string) (*manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if m.Schema != 1 {
		return nil, fmt.Errorf("%s has schema %d, which this letsgo-cask does not understand", path, m.Schema)
	}
	return &m, nil
}

// cask is a rendered Homebrew cask.
type cask struct {
	Token    string
	Version  string
	Name     string
	Desc     string
	Homepage string
	License  string
	Caveats  string

	// ARM and Intel are the two macOS archives. Homebrew installs one cask on
	// either architecture, so both live in one file under on_arm and on_intel.
	ARM   *download
	Intel *download

	Binaries []string
}

type download struct {
	URL    string
	SHA256 string
}

// caskFields are the values a release cannot supply: they describe the
// program rather than the artifacts, so they come from the command line.
type caskFields struct {
	Token    string
	Desc     string
	Homepage string
	License  string
	Caveats  string
}

func build(m *manifest, repo, variant string, f caskFields) (*cask, error) {
	tag := m.Tag
	if tag == "" {
		tag = "v" + m.Version
	}

	// A variant suffixes the archive's name, which is how its artifacts are
	// told apart from the release's own.
	want := m.Project
	if variant != "" {
		want = m.Project + "-" + variant
	}

	c := &cask{
		Token:    f.Token,
		Version:  m.Version,
		Name:     m.Project,
		Desc:     f.Desc,
		Homepage: f.Homepage,
		License:  f.License,
		Caveats:  f.Caveats,
	}
	if c.Homepage == "" {
		c.Homepage = "https://github.com/" + repo
	}

	for _, a := range m.Artifacts {
		if a.OS != "darwin" || base(a.Name, m.Version) != want {
			continue
		}

		d := &download{
			URL:    fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", repo, tag, a.Name),
			SHA256: a.SHA256,
		}
		switch a.Arch {
		case "arm64":
			c.ARM = d
		case "amd64":
			c.Intel = d
		default:
			continue
		}
		c.Binaries = merge(c.Binaries, a.executables())
	}

	if c.ARM == nil && c.Intel == nil {
		return nil, fmt.Errorf(
			"the release has no macOS archive called %s;\n"+
				"  a cask installs a macOS build, and this release published none under that name", want)
	}
	if len(c.Binaries) == 0 {
		return nil, fmt.Errorf("the archives name no binaries, so the cask has nothing to install")
	}
	if c.Token == "" {
		c.Token = want
	}
	return c, nil
}

// base strips the version and platform letsgo appends to every archive.
func base(archive, version string) string {
	name := archive
	for _, ext := range []string{".tar.gz", ".zip"} {
		if trimmed, ok := strings.CutSuffix(name, ext); ok {
			name = trimmed
			break
		}
	}
	// What is left is "<base>_<version>_<os>_<arch>".
	cut := "_" + version + "_"
	if i := strings.Index(name, cut); i >= 0 {
		return name[:i]
	}
	return ""
}

func merge(into, add []string) []string {
	seen := map[string]bool{}
	for _, name := range into {
		seen[name] = true
	}
	for _, name := range add {
		if !seen[name] {
			seen[name] = true
			into = append(into, name)
		}
	}
	sort.Strings(into)
	return into
}

// render writes the cask.
//
// Written by hand rather than through a template because a cask is small and
// the shape is worth reading in the source: an arch block each, then what to
// install.
func (c *cask) render() string {
	var b strings.Builder

	b.WriteString("# Generated by letsgo-cask. Do not edit; the next release will overwrite it.\n")
	fmt.Fprintf(&b, "cask %s do\n", quote(c.Token))
	fmt.Fprintf(&b, "  version %s\n\n", quote(c.Version))

	writeArch(&b, "on_arm", c.ARM)
	writeArch(&b, "on_intel", c.Intel)

	fmt.Fprintf(&b, "  name %s\n", quote(c.Name))
	if c.Desc != "" {
		fmt.Fprintf(&b, "  desc %s\n", quote(c.Desc))
	}
	fmt.Fprintf(&b, "  homepage %s\n", quote(c.Homepage))
	if c.License != "" {
		fmt.Fprintf(&b, "  license %s\n", quote(c.License))
	}
	b.WriteString("\n")

	// binary rather than app: letsgo publishes an executable, not a bundle, so
	// claiming an .app would name something the archive does not contain.
	for _, name := range c.Binaries {
		fmt.Fprintf(&b, "  binary %s\n", quote(name))
	}

	// Last, as Homebrew's own style orders it: caveats are what the user reads
	// after everything else has been decided.
	if c.Caveats != "" {
		b.WriteString("\n  caveats <<~EOS\n")
		for _, line := range strings.Split(strings.TrimRight(c.Caveats, "\n"), "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
		b.WriteString("  EOS\n")
	}

	b.WriteString("end\n")
	return b.String()
}

func writeArch(b *strings.Builder, block string, d *download) {
	if d == nil {
		return
	}
	fmt.Fprintf(b, "  %s do\n", block)
	fmt.Fprintf(b, "    url %s\n", quote(d.URL))
	fmt.Fprintf(b, "    sha256 %s\n", quote(d.SHA256))
	b.WriteString("  end\n\n")
}

// quote renders a Ruby double-quoted string. Only the two characters that can
// end or escape one need handling, and silently producing invalid Ruby is not
// an acceptable way to deal with them.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
