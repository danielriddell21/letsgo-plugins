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
// repository that wants a cask without pinning the hook:
//
//	letsgo-cask --repo you/gambit --variant gui dist/letsgo.json
//
// The cask goes to stdout, or to the file named by -o. It cannot publish to a
// tap by itself any more: that is the hook's job now, through core's own
// tap client and credential.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"sort"
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

// pluginConfig is letsgo-cask.mod, decoded.
type pluginConfig struct {
	Variant string
	Token   string
}

// answerTapFiles is this plugin's answer to the tap-files hook.
func answerTapFiles(in plugin.TapFilesInput) (plugin.TapFilesOutput, error) {
	cfg, err := readPluginConfig(in.ConfigDir)
	if err != nil {
		return plugin.TapFilesOutput{}, err
	}

	c, err := caskFromHookInput(in, cfg)
	if err != nil {
		return plugin.TapFilesOutput{}, err
	}

	return plugin.TapFilesOutput{Files: []plugin.TapFile{
		{Path: path.Join("Casks", c.Token+".rb"), Content: c.render()},
	}}, nil
}

// readPluginConfig parses the plugin's own config, which core locates: the
// config dir first, then the legacy file at the repository root. Missing
// entirely is not an error: a release with one darwin build and no variant
// needs neither directive.
func readPluginConfig(configDir string) (pluginConfig, error) {
	data, cfgPath, err := plugin.ReadConfig(configDir, "cask")
	if errors.Is(err, fs.ErrNotExist) {
		return pluginConfig{}, nil
	}
	if err != nil {
		return pluginConfig{}, fmt.Errorf("reading %s: %w", cfgPath, err)
	}

	var cfg pluginConfig
	seen := map[string]bool{}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if before, _, ok := strings.Cut(text, "//"); ok {
			text = strings.TrimSpace(before)
		}
		if text == "" {
			continue
		}

		fields := strings.Fields(text)
		if len(fields) != 2 {
			return pluginConfig{}, fmt.Errorf("%s:%d: expected `variant <name>` or `token <name>`, got %q",
				cfgPath, line, text)
		}

		directive, value := fields[0], fields[1]
		if seen[directive] {
			return pluginConfig{}, fmt.Errorf("%s:%d: %s is already set", cfgPath, line, directive)
		}
		seen[directive] = true

		switch directive {
		case "variant":
			cfg.Variant = value
		case "token":
			cfg.Token = value
		default:
			return pluginConfig{}, fmt.Errorf("%s:%d: unknown directive %q", cfgPath, line, directive)
		}
	}
	if err := scanner.Err(); err != nil {
		return pluginConfig{}, fmt.Errorf("%s: %w", cfgPath, err)
	}
	return cfg, nil
}

// caskFromHookInput builds a cask from the tap-files hook's own input: every
// digest and URL is already there, built by core, so nothing here recomputes
// one or asks the forge a question core has already answered.
func caskFromHookInput(in plugin.TapFilesInput, cfg pluginConfig) (*cask, error) {
	want := in.Project
	if cfg.Variant != "" {
		want = in.Project + "-" + cfg.Variant
	}

	c := &cask{
		Token:    cfg.Token,
		Version:  in.Version,
		Name:     in.Project,
		Desc:     in.Description,
		Homepage: in.Homepage,
		License:  in.License,
		Caveats:  in.Caveats,
	}

	for _, a := range in.Artifacts {
		if a.OS != "darwin" || a.Variant != cfg.Variant {
			continue
		}

		d := &download{URL: a.URL, SHA256: a.SHA256}
		switch a.Arch {
		case "arm64":
			c.ARM = d
		case "amd64":
			c.Intel = d
		default:
			continue
		}
		c.Binaries = merge(c.Binaries, a.Binaries)
	}

	if c.ARM == nil && c.Intel == nil {
		return nil, fmt.Errorf(
			"the release has no macOS build called %s;\n"+
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

// executableNames returns the archive's binaries, whichever way it spells them.
func executableNames(a coremanifest.Artifact) []string {
	bins := a.Executables()
	out := make([]string, 0, len(bins))
	for _, b := range bins {
		out = append(out, b.Name)
	}
	return out
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

func read(path string) (*coremanifest.Manifest, error) {
	m, err := coremanifest.Read(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if m.Schema != 1 {
		return nil, fmt.Errorf("%s has schema %d, which this letsgo-cask does not understand", path, m.Schema)
	}
	return m, nil
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

func build(m *coremanifest.Manifest, repo, variant string, f caskFields) (*cask, error) {
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
		if a.OS != "darwin" || coremanifest.BaseName(a.Name, m.Version, a.OS, a.Arch) != want {
			continue
		}

		d := &download{
			URL:    downloadURL(repo, tag, a.Name),
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
		c.Binaries = merge(c.Binaries, executableNames(a))
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

// downloadURL is where the forge serves a release asset from. A scoped tag
// keeps its slashes, which the forge reads as directory segments; every
// segment, and the asset's name, is escaped so a character the URL cannot
// carry does not change which file it names. It is the URL core builds for
// the hook path.
func downloadURL(repo, tag, name string) string {
	segments := strings.Split(tag, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/%s",
		repo, strings.Join(segments, "/"), url.PathEscape(name))
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
