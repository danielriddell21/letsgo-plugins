package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielriddell21/letsgo/plan"
	"github.com/danielriddell21/unum/pkg/terraform"
)

func releasePlan(actions ...plan.Action) *plan.File {
	return &plan.File{
		Schema: plan.FileSchema, Kind: plan.FileKindRelease, Repo: "you/demo",
		Tag: "v1.2.0", Commit: "abc123", ManifestSHA256: "sha256:feed", Actions: actions,
	}
}

func yankPlan(actions ...plan.Action) *plan.File {
	return &plan.File{
		Schema: plan.FileSchema, Kind: plan.FileKindYank, Repo: "you/demo",
		Tag: "v1.1.0", Actions: actions,
	}
}

// export runs the command on a plan written to disk, as a user would.
func export(t *testing.T, command string, f *plan.File) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.plan")
	if err := f.Write(path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{command, path}, &out); err != nil {
		t.Fatalf("letsgo-tfplan %s: %v", command, err)
	}
	return out.String()
}

func changes(t *testing.T, f *plan.File) map[string]map[string]any {
	t.Helper()
	var doc struct {
		FormatVersion   string `json:"format_version"`
		ResourceChanges []struct {
			Address string         `json:"address"`
			Type    string         `json:"type"`
			Name    string         `json:"name"`
			Change  map[string]any `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal([]byte(export(t, "json", f)), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.FormatVersion == "" {
		t.Error("no format_version")
	}
	byAddress := map[string]map[string]any{}
	for _, rc := range doc.ResourceChanges {
		byAddress[rc.Address] = rc.Change
		byAddress[rc.Address]["type"], byAddress[rc.Address]["name"] = rc.Type, rc.Name
	}
	return byAddress
}

func mapOf(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok && v != nil {
		t.Fatalf("%v is not an object", v)
	}
	return m
}

func TestJSONMapsEachOpAndKind(t *testing.T) {
	got := changes(t, releasePlan(
		plan.Action{Op: plan.Add, Kind: plan.KindRelease, Target: "v1.2.0"},
		plan.Action{Op: plan.Add, Kind: plan.KindAsset, Target: "demo_1.2.0_linux_amd64.tar.gz", Planned: "sha256:aa"},
		plan.Action{Op: plan.Change, Kind: plan.KindAsset, Target: "demo.sbom", Observed: "sha256:bb", Planned: "sha256:cc"},
		plan.Action{Op: plan.Remove, Kind: plan.KindAsset, Target: "old.txt", Observed: "sha256:dd"},
		plan.Action{Op: plan.Keep, Kind: plan.KindAsset, Target: "same.txt", Observed: "sha256:ee", Planned: "sha256:ee"},
		plan.Action{Op: plan.Change, Kind: plan.KindTap, Target: "Formula/demo.rb", Observed: "blob:11", Planned: "blob:22"},
		plan.Action{Op: plan.Add, Kind: plan.KindImage, Target: "ghcr.io/you/demo:1.2.0", Planned: "sha256:ff"},
		plan.Action{Op: plan.Add, Kind: plan.KindProxy, Target: "demo@v1.2.0", Planned: "h1:xyz"},
	))

	cases := []struct {
		address, typ, action string
		before, after        map[string]any
	}{
		{"letsgo_release.v1_2_0", "letsgo_release", "create", nil, map[string]any{"tag": "v1.2.0"}},
		{"letsgo_asset.demo_1_2_0_linux_amd64_tar_gz", "letsgo_asset", "create", nil, map[string]any{"sha256": "aa"}},
		{"letsgo_asset.demo_sbom", "letsgo_asset", "update", map[string]any{"sha256": "bb"}, map[string]any{"sha256": "cc"}},
		{"letsgo_asset.old_txt", "letsgo_asset", "delete", map[string]any{"sha256": "dd"}, nil},
		{"letsgo_asset.same_txt", "letsgo_asset", "no-op", map[string]any{"sha256": "ee"}, map[string]any{"sha256": "ee"}},
		{"letsgo_tap_file.Formula_demo_rb", "letsgo_tap_file", "update", map[string]any{"blob": "11"}, map[string]any{"blob": "22"}},
		{"letsgo_image_tag.ghcr_io_you_demo_1_2_0", "letsgo_image_tag", "create", nil, map[string]any{"digest": "sha256:ff"}},
		{"letsgo_proxy.demo_v1_2_0", "letsgo_proxy", "create", nil, map[string]any{"fingerprint": "h1:xyz"}},
	}
	if len(got) != len(cases) {
		t.Fatalf("%d resource changes, want %d: %v", len(got), len(cases), got)
	}
	for _, c := range cases {
		ch, ok := got[c.address]
		if !ok {
			t.Errorf("no %s in %v", c.address, got)
			continue
		}
		if ch["type"] != c.typ {
			t.Errorf("%s: type %v, want %s", c.address, ch["type"], c.typ)
		}
		if actions, _ := ch["actions"].([]any); len(actions) != 1 || actions[0] != c.action {
			t.Errorf("%s: actions %v, want [%s]", c.address, ch["actions"], c.action)
		}
		if !sameObject(mapOf(t, ch["before"]), c.before) || !sameObject(mapOf(t, ch["after"]), c.after) {
			t.Errorf("%s: before %v after %v, want %v and %v", c.address, ch["before"], ch["after"], c.before, c.after)
		}
	}
}

func sameObject(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestJSONKeepsTheTargetVerbatimAsTheName(t *testing.T) {
	got := changes(t, releasePlan(plan.Action{Op: plan.Add, Kind: plan.KindAsset, Target: "a b/c.tgz", Planned: "sha256:aa"}))
	if name := got["letsgo_asset.a_b_c_tgz"]["name"]; name != "a b/c.tgz" {
		t.Errorf("name = %v", name)
	}
}

func TestJSONNumbersAddressesThatSlugAlike(t *testing.T) {
	got := changes(t, releasePlan(
		plan.Action{Op: plan.Add, Kind: plan.KindAsset, Target: "a.b", Planned: "sha256:01"},
		plan.Action{Op: plan.Add, Kind: plan.KindAsset, Target: "a_b", Planned: "sha256:02"},
	))
	if _, ok := got["letsgo_asset.a_b"]; !ok {
		t.Errorf("addresses = %v", got)
	}
	if _, ok := got["letsgo_asset.a_b_2"]; !ok {
		t.Errorf("the second a_b was not numbered: %v", got)
	}
}

// A digest the plan could not know is absent from after and flagged, which is
// how Terraform says "(known after apply)".
func TestJSONMarksAnUnknownDigestInAfterUnknown(t *testing.T) {
	f := releasePlan(
		plan.Action{Op: plan.Add, Kind: plan.KindAsset, Target: "demo.tgz"},
		plan.Action{Op: plan.Change, Kind: plan.KindTap, Target: "Formula/demo.rb", Observed: "blob:11"},
		plan.Action{Op: plan.Add, Kind: plan.KindImage, Target: "ghcr.io/you/demo:1", Planned: "sha256:ff"},
		plan.Action{Op: plan.Add, Kind: plan.KindRelease, Target: "v1.2.0"},
	)
	got := changes(t, f)

	if unk := mapOf(t, got["letsgo_asset.demo_tgz"]["after_unknown"]); unk["sha256"] != true {
		t.Errorf("asset after_unknown = %v", unk)
	}
	if after := mapOf(t, got["letsgo_asset.demo_tgz"]["after"]); len(after) != 0 {
		t.Errorf("an unknown digest appears in after: %v", after)
	}
	if unk := mapOf(t, got["letsgo_tap_file.Formula_demo_rb"]["after_unknown"]); unk["blob"] != true {
		t.Errorf("tap after_unknown = %v", unk)
	}
	for _, known := range []string{"letsgo_image_tag.ghcr_io_you_demo_1", "letsgo_release.v1_2_0"} {
		if unk := mapOf(t, got[known]["after_unknown"]); len(unk) != 0 {
			t.Errorf("%s after_unknown = %v, want none", known, unk)
		}
	}

	out := export(t, "md", f)
	if !strings.Contains(out, "(known after apply)") {
		t.Errorf("the summary does not say the digest is unknown:\n%s", out)
	}
}

// What the renderer reads back is what the plan said.
func TestJSONIsReadByUnum(t *testing.T) {
	parsed, err := terraform.Parse([]byte(export(t, "json", releasePlan(
		plan.Action{Op: plan.Add, Kind: plan.KindRelease, Target: "v1.2.0"},
		plan.Action{Op: plan.Change, Kind: plan.KindAsset, Target: "a", Observed: "sha256:01", Planned: "sha256:02"},
		plan.Action{Op: plan.Remove, Kind: plan.KindAsset, Target: "b", Observed: "sha256:03"},
		plan.Action{Op: plan.Keep, Kind: plan.KindAsset, Target: "c", Observed: "sha256:04", Planned: "sha256:04"},
	))))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.AddCount() != 1 || parsed.ChangeCount() != 1 || parsed.DestroyCount() != 1 {
		t.Errorf("add %d change %d destroy %d", parsed.AddCount(), parsed.ChangeCount(), parsed.DestroyCount())
	}
	if len(parsed.Changes) != 4 || len(parsed.Changed()) != 3 {
		t.Errorf("%d changes, %d of them changing", len(parsed.Changes), len(parsed.Changed()))
	}
}

func TestMarkdownPutsMarkersInColumnZeroAndCountsWhatItOmits(t *testing.T) {
	out := export(t, "md", releasePlan(
		plan.Action{Op: plan.Add, Kind: plan.KindRelease, Target: "v1.2.0"},
		plan.Action{Op: plan.Change, Kind: plan.KindAsset, Target: "a", Observed: "sha256:01", Planned: "sha256:02"},
		plan.Action{Op: plan.Remove, Kind: plan.KindAsset, Target: "b", Observed: "sha256:03"},
		plan.Action{Op: plan.Keep, Kind: plan.KindAsset, Target: "same", Observed: "sha256:04", Planned: "sha256:04"},
	))

	if !strings.HasPrefix(out, "## letsgo plan\n\n```diff\n") {
		t.Errorf("no heading and diff block:\n%s", out)
	}
	for _, want := range []string{
		"\n+   resource \"letsgo_release\" \"v1.2.0\" {",
		"\n!   resource \"letsgo_asset\" \"a\" {",
		"\n-   resource \"letsgo_asset\" \"b\" {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "same") {
		t.Errorf("a kept target is listed:\n%s", out)
	}
	if !strings.HasSuffix(out, "```\n\nPlan: 1 to add, 1 to change, 1 to remove. 1 unchanged.\n") {
		t.Errorf("the footer is not outside the block:\n%s", out)
	}
}

func TestMarkdownOmitsTheBlockWhenNothingChanges(t *testing.T) {
	out := export(t, "md", releasePlan(
		plan.Action{Op: plan.Keep, Kind: plan.KindAsset, Target: "same", Observed: "sha256:04", Planned: "sha256:04"},
	))
	if want := "## letsgo plan\n\nPlan: 0 to add, 0 to change, 0 to remove. 1 unchanged.\n"; out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}

func TestYankPlansExport(t *testing.T) {
	f := yankPlan(
		plan.Action{Op: plan.Change, Kind: plan.KindRelease, Target: "v1.1.0"},
		plan.Action{Op: plan.Change, Kind: plan.KindGoMod, Target: "go.mod", Observed: "blob:01", Planned: "blob:02"},
		plan.Action{Op: plan.Keep, Kind: plan.KindTap, Target: "Formula/demo.rb", Observed: "blob:03", Planned: "blob:03"},
	)

	got := changes(t, f)
	release := got["letsgo_release.v1_1_0"]
	if before := mapOf(t, release["before"]); before["retracted"] != false {
		t.Errorf("before = %v", before)
	}
	if after := mapOf(t, release["after"]); after["retracted"] != true {
		t.Errorf("after = %v", after)
	}
	if after := mapOf(t, got["letsgo_gomod.go_mod"]["after"]); after["blob"] != "02" {
		t.Errorf("gomod after = %v", after)
	}
	if kept := mapOf(t, got["letsgo_release.v1_1_0"]["after_unknown"]); len(kept) != 0 {
		t.Errorf("after_unknown = %v", kept)
	}

	out := export(t, "md", f)
	if !strings.HasPrefix(out, "## letsgo plan: yank v1.1.0\n\n```diff\n") || !strings.Contains(out, "\n!   resource \"letsgo_release\"") {
		t.Errorf("yank summary:\n%s", out)
	}
	if !strings.HasSuffix(out, "Plan: 0 to add, 2 to change, 0 to remove. 1 unchanged.\n") {
		t.Errorf("footer:\n%s", out)
	}
}

func TestAnAlreadyRetractedReleaseStaysRetracted(t *testing.T) {
	got := changes(t, yankPlan(plan.Action{Op: plan.Keep, Kind: plan.KindRelease, Target: "v1.1.0"}))
	release := got["letsgo_release.v1_1_0"]
	for _, side := range []string{"before", "after"} {
		if m := mapOf(t, release[side]); m["retracted"] != true {
			t.Errorf("%s = %v", side, m)
		}
	}
}

func TestRunRejectsWhatItCannotRead(t *testing.T) {
	dir := t.TempDir()
	save := func(name string, f *plan.File) string {
		path := filepath.Join(dir, name)
		if err := f.Write(path); err != nil {
			t.Fatal(err)
		}
		return path
	}
	badOp := releasePlan(plan.Action{Op: "?", Kind: plan.KindAsset, Target: "x"})
	otherSchema := releasePlan()
	otherSchema.Schema = 9

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no arguments", nil, "usage"},
		{"no plan", []string{"json"}, "usage"},
		{"extra argument", []string{"json", "a", "b"}, "usage"},
		{"unknown command", []string{"yaml", "a"}, `"yaml"`},
		{"missing file", []string{"json", filepath.Join(dir, "absent.plan")}, "absent.plan"},
		{"other schema", []string{"md", save("schema.plan", otherSchema)}, "schema 9"},
		{"unknown op", []string{"json", save("op.plan", badOp)}, `"?"`},
		{"unknown op in md", []string{"md", save("op.plan", badOp)}, `"?"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := run(c.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("run(%v) = %v, want an error naming %s", c.args, err, c.want)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestRunReportsAWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.plan")
	if err := releasePlan().Write(path); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"json", path}, failingWriter{}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("run = %v", err)
	}
}
