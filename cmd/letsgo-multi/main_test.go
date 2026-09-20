package main

import "testing"

func TestLayoutPutsEveryCommandInOneArchive(t *testing.T) {
	in := input{
		Project: "toolshed",
		Commands: []command{
			{Binary: "chip", Package: "./cmd/chip"},
			{Binary: "plane", Package: "./cmd/plane"},
		},
	}

	out, err := layout(in)
	if err != nil {
		t.Fatalf("layout: %v", err)
	}
	if len(out.Archives) != 1 {
		t.Fatalf("the collection is one archive, got %d", len(out.Archives))
	}

	got := out.Archives[0]
	if got.Name != "toolshed" {
		t.Errorf("archive should be named after the project, got %q", got.Name)
	}
	if len(got.Binaries) != 2 || got.Binaries[0] != "chip" || got.Binaries[1] != "plane" {
		t.Errorf("every command should be in it, got %v", got.Binaries)
	}
}

func TestLayoutRefusesAModuleWithNoCommands(t *testing.T) {
	if _, err := layout(input{Project: "toolshed"}); err == nil {
		t.Error("a module that builds nothing has no layout to describe")
	}
}

func TestLayoutRefusesAReleaseWithNoProjectName(t *testing.T) {
	in := input{Commands: []command{{Binary: "chip", Package: "./cmd/chip"}}}
	if _, err := layout(in); err == nil {
		t.Error("the archive is named after the project, so it needs one")
	}
}
