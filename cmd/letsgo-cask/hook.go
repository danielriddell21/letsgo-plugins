package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/danielriddell21/letsgo/plugin"
)

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
	file, err := plugin.LoadConfig(configDir, "cask")
	if errors.Is(err, fs.ErrNotExist) {
		return pluginConfig{}, nil
	}
	if err != nil {
		return pluginConfig{}, fmt.Errorf("reading the cask config: %w", err)
	}

	var cfg pluginConfig
	seen := map[string]bool{}

	for _, l := range file.Lines {
		if len(l.Args) != 1 {
			return pluginConfig{}, file.Errorf(l, "expected `variant <name>` or `token <name>`, got %q",
				strings.Join(append([]string{l.Keyword}, l.Args...), " "))
		}
		if seen[l.Keyword] {
			return pluginConfig{}, file.Errorf(l, "%s is already set", l.Keyword)
		}
		seen[l.Keyword] = true

		switch l.Keyword {
		case "variant":
			cfg.Variant = l.Args[0]
		case "token":
			cfg.Token = l.Args[0]
		default:
			return pluginConfig{}, file.Errorf(l, "unknown directive %q", l.Keyword)
		}
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

		var slot **download
		switch a.Arch {
		case "arm64":
			slot = &c.ARM
		case "amd64":
			slot = &c.Intel
		default:
			continue
		}

		// A second archive for one architecture would leave the cask installing
		// whichever came last. A manifest from before letsgo v0.7.0 records no
		// Variant, so a variant's archives read as the release's own; refuse
		// rather than pick one.
		if *slot != nil {
			return nil, fmt.Errorf(
				"the release has more than one macOS %s build called %s (%s is another);\n"+
					"  a manifest from before letsgo v0.7.0 does not record which archives belong to a variant",
				a.Arch, want, a.Archive)
		}
		*slot = &download{URL: a.URL, SHA256: a.SHA256}
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
