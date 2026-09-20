// Command letsgo-multi groups a module's commands into one archive per target.
//
// letsgo ships one archive per command, which is right for a repository whose
// product is a program. For one whose product is a collection of small tools,
// the collection is the product: eleven commands become fifty-five downloads
// and eleven Homebrew formulas, one per tool. If any tool shares a name with a
// Homebrew core package, its formula shadows the core one.
//
// This plugin answers the archive-layout hook with a single archive holding
// everything, which is one download per platform and one formula installing
// every tool.
package main

import (
	"fmt"

	"github.com/danielriddell21/letsgo-plugins/internal/hook"
)

// command is one binary the module builds, as letsgo describes it.
type command struct {
	Binary  string `json:"binary"`
	Package string `json:"package"`
}

type input struct {
	Project  string    `json:"project"`
	Commands []command `json:"commands"`
}

type archive struct {
	Name     string   `json:"name"`
	Binaries []string `json:"binaries"`
}

type output struct {
	Archives []archive `json:"archives"`
}

func main() {
	hook.Main("archive-layout", layout)
}

func layout(in input) (output, error) {
	if len(in.Commands) == 0 {
		return output{}, fmt.Errorf("the module builds no commands")
	}
	if in.Project == "" {
		return output{}, fmt.Errorf("the release has no project name to call the archive")
	}

	// Named after the project, because that is what the collection is called.
	// letsgo checks the answer covers every command exactly once, so there is
	// nothing to validate here beyond having something to say.
	all := archive{Name: in.Project}
	for _, cmd := range in.Commands {
		all.Binaries = append(all.Binaries, cmd.Binary)
	}

	return output{Archives: []archive{all}}, nil
}
