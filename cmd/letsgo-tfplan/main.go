// Command letsgo-tfplan reads a letsgo plan file and writes it the way
// Terraform writes its own.
//
// letsgo's core speaks one plan format, its native one, and never emits
// Terraform's: that is a compatibility surface with its own release cadence,
// and apply reads only the native schema. This companion is the other
// direction. It only reads a plan, so it cannot change what apply does, and it
// is hookless: nothing in a release runs it.
//
//	letsgo-tfplan json release.plan > plan.tf.json
//	letsgo-tfplan md   release.plan
//
// # json
//
// The plan as "terraform show -json" shapes one, so a tool that already reads
// Terraform plans (tf-plan-summary-action, unum diff) renders it without
// knowing letsgo exists. Each action is a resource_changes entry:
//
//	kind     type:    letsgo_<kind> (letsgo_tap_file and letsgo_image_tag for
//	                  the tap and image kinds)
//	target   name:    verbatim, and address is <type>.<slug>
//	+ ~ - =  actions: create, update, delete, no-op
//
// A plan records fingerprints, not bytes, so the before and after attribute
// maps hold one fingerprint each under the name the kind calls it: sha256 for
// an asset, blob for a tap file or go.mod, digest for an image tag. A
// fingerprint that was not known when the plan was made is absent from after
// and marked true in after_unknown, which renders as "(known after apply)".
// A release has no fingerprint; it carries its tag, and in a yank plan whether
// it is retracted.
//
// # md
//
// The same plan rendered by unum's terraform package, in a diff block with the
// change markers in column 0, under a heading and above a footer.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/danielriddell21/letsgo/plan"
	"github.com/danielriddell21/unum/pkg/terraform"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "letsgo-tfplan:", err)
		os.Exit(1)
	}
}

const usage = "usage: letsgo-tfplan json|md <plan>"

func run(args []string, out io.Writer) error {
	if len(args) != 2 {
		return errors.New(usage)
	}

	var render func(*plan.File) (string, error)
	switch args[0] {
	case "json":
		render = renderJSON
	case "md":
		render = renderMarkdown
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}

	file, err := plan.Read(args[1])
	if err != nil {
		return fmt.Errorf("reading %s: %w", args[1], err)
	}
	text, err := render(file)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(out, text); err != nil {
		return fmt.Errorf("writing: %w", err)
	}
	return nil
}

// document is the part of "terraform show -json" a renderer reads.
type document struct {
	FormatVersion   string           `json:"format_version"`
	ResourceChanges []resourceChange `json:"resource_changes"`
}

type resourceChange struct {
	Address string `json:"address"`
	Mode    string `json:"mode"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Change  change `json:"change"`
}

type change struct {
	Actions      []string       `json:"actions"`
	Before       map[string]any `json:"before"`
	After        map[string]any `json:"after"`
	AfterUnknown map[string]any `json:"after_unknown"`
}

// terraformActions is what each op is called in a Terraform plan.
var terraformActions = map[plan.Op]string{
	plan.Add:    "create",
	plan.Change: "update",
	plan.Remove: "delete",
	plan.Keep:   "no-op",
}

func convert(f *plan.File) (*document, error) {
	doc := &document{FormatVersion: "1.2", ResourceChanges: make([]resourceChange, 0, len(f.Actions))}
	taken := map[string]bool{}

	for _, a := range f.Actions {
		verb, ok := terraformActions[a.Op]
		if !ok {
			return nil, fmt.Errorf("the plan has an action on %s with the unknown op %q", a.Target, a.Op)
		}

		typ := resourceType(a.Kind)
		ch := change{Actions: []string{verb}, AfterUnknown: map[string]any{}}
		if a.Op != plan.Add {
			ch.Before = state(f, a, a.Observed, false)
		}
		if a.Op != plan.Remove {
			ch.After = state(f, a, a.Planned, true)
			if unknown(a) {
				ch.AfterUnknown[attributeName(a.Kind)] = true
			}
		}

		doc.ResourceChanges = append(doc.ResourceChanges, resourceChange{
			Address: address(typ, a.Target, taken),
			Mode:    "managed",
			Type:    typ,
			Name:    a.Target,
			Change:  ch,
		})
	}
	return doc, nil
}

// resourceType names a kind the way Terraform names a resource type.
func resourceType(kind plan.Kind) string {
	switch kind {
	case plan.KindTap:
		return "letsgo_tap_file"
	case plan.KindImage:
		return "letsgo_image_tag"
	}
	return "letsgo_" + string(kind)
}

// address is <type>.<slug>. Two targets can slug alike ("a.b" and "a_b"), and
// an address names exactly one resource, so a later one is numbered.
func address(typ, target string, taken map[string]bool) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, target)

	candidate := typ + "." + slug
	for n := 2; taken[candidate]; n++ {
		candidate = fmt.Sprintf("%s.%s_%d", typ, slug, n)
	}
	taken[candidate] = true
	return candidate
}

// attributeName is what a kind calls its one fingerprint.
func attributeName(kind plan.Kind) string {
	switch kind {
	case plan.KindAsset:
		return "sha256"
	case plan.KindTap, plan.KindGoMod:
		return "blob"
	case plan.KindImage:
		return "digest"
	}
	return "fingerprint"
}

// unknown reports whether an action's planned fingerprint is one the plan
// could not know yet: something is to be written, the kind has a fingerprint,
// and none was recorded. A release has none to record.
func unknown(a plan.Action) bool {
	return a.Planned == "" && a.Kind != plan.KindRelease && (a.Op == plan.Add || a.Op == plan.Change)
}

// state is one side of an action as an attribute map. after says which side,
// because a yank plan's release is retracted only on the far side of it.
func state(f *plan.File, a plan.Action, fingerprint string, after bool) map[string]any {
	m := map[string]any{}

	if a.Kind == plan.KindRelease {
		m["tag"] = a.Target
		if f.Kind == plan.FileKindYank {
			m["retracted"] = after || a.Op == plan.Keep
		}
		return m
	}

	if fingerprint == "" {
		return m
	}
	name := attributeName(a.Kind)
	if algorithm, value, ok := strings.Cut(fingerprint, ":"); ok && algorithm == name {
		fingerprint = value
	}
	m[name] = fingerprint
	return m
}

func renderJSON(f *plan.File) (string, error) {
	doc, err := convert(f)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding: %w", err)
	}
	return string(data) + "\n", nil
}

// renderMarkdown draws the plan through unum, so that it reads the same here,
// in `unum diff` and in tf-plan-summary-action.
func renderMarkdown(f *plan.File) (string, error) {
	doc, err := convert(f)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("encoding: %w", err)
	}
	parsed, err := terraform.Parse(data)
	if err != nil {
		return "", fmt.Errorf("reading the converted plan: %w", err)
	}

	title := "letsgo plan"
	if f.Kind == plan.FileKindYank {
		title += ": yank " + f.Tag
	}

	var b strings.Builder
	b.WriteString("## " + title + "\n\n")
	if diff := parsed.RenderDiff(terraform.RenderOptions{MarkerFirst: true, BangUpdates: true}); diff != "" {
		b.WriteString("```diff\n" + diff + "```\n\n")
	}
	fmt.Fprintf(&b, "Plan: %d to add, %d to change, %d to remove.",
		parsed.AddCount(), parsed.ChangeCount(), parsed.DestroyCount())
	if kept := len(parsed.Changes) - len(parsed.Changed()); kept > 0 {
		fmt.Fprintf(&b, " %d unchanged.", kept)
	}
	b.WriteString("\n")
	return b.String(), nil
}
