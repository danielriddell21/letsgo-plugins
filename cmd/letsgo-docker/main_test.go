package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/danielriddell21/letsgo/plugin"
)

// repoWith writes a repository root holding docker.mod and the given files,
// and returns its config dir, as letsgo would pass it.
func repoWith(t *testing.T, imagesMod string, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range append(files, ".letsgo/docker.mod") {
		body := ""
		if f == ".letsgo/docker.mod" {
			body = imagesMod
		}
		path := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, ".letsgo")
}

// recordDocker stands in for docker, recording each build's arguments and
// environment.
func recordDocker(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	orig := docker
	t.Setenv("LETSGO_DOCKER", fakeExecutable(t))
	docker = func(_ context.Context, _ string, env []string, args ...string) error {
		calls = append(calls, append(args, "ENV:"+lookup(env, "SOURCE_DATE_EPOCH")))
		return nil
	}
	t.Cleanup(func() { docker = orig })
	return &calls
}

func lookup(env []string, name string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

func TestReadConfigDefaultsTheDockerfileAndPlatform(t *testing.T) {
	dir := repoWith(t, "timeout 45m\n\nimage fixer (\n\tsuffix -fixer\n)\n", "Dockerfile")

	images, err := readConfig(dir, filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 {
		t.Fatalf("images = %+v", images)
	}
	img := images[0]
	if img.Dockerfile != "Dockerfile" || img.Suffix != "-fixer" || img.Target != "" {
		t.Errorf("image = %+v", img)
	}
	if want := []string{"linux/" + runtime.GOARCH}; !slices.Equal(img.Platforms, want) {
		t.Errorf("platforms = %v, want this machine's %v", img.Platforms, want)
	}
}

func TestReadConfigReadsEverySetting(t *testing.T) {
	dir := repoWith(t, `image fixer (
	dockerfile build/fixer.Dockerfile
	target     fixer
	suffix     -fixer
	platform   linux/amd64 linux/arm64
)

image debug suffix -debug
`, "build/fixer.Dockerfile", "Dockerfile")

	images, err := readConfig(dir, filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || images[0].Name != "fixer" || images[1].Name != "debug" {
		t.Fatalf("images = %+v, want fixer then debug, as written", images)
	}
	fixer := images[0]
	if fixer.Dockerfile != "build/fixer.Dockerfile" || fixer.Target != "fixer" ||
		!slices.Equal(fixer.Platforms, []string{"linux/amd64", "linux/arm64"}) {
		t.Errorf("fixer = %+v", fixer)
	}
}

func TestReadConfigRefuses(t *testing.T) {
	for _, tc := range []struct{ name, mod, want string }{
		{"two release images", "image a (\n\ttarget a\n)\nimage b (\n\ttarget b\n)\n", "only one can be the release image"},
		{"a shared suffix", "image a suffix -x\nimage b suffix -x\n", "share the suffix"},
		{"a repeated setting", "image a (\n\tsuffix -a\n\tsuffix -b\n)\n", "already set"},
		{"an unknown setting", "image a (\n\tsuffix -a\n\tcache on\n)\n", "unknown setting"},
		{"an unknown directive", "build linux/amd64\n", "unknown directive"},
		{"a missing Dockerfile", "image a (\n\tsuffix -a\n\tdockerfile nope.Dockerfile\n)\n", "nope.Dockerfile"},
		{"a Dockerfile outside", "image a (\n\tsuffix -a\n\tdockerfile ../Dockerfile\n)\n", "inside the repository"},
		{"a bad name", "image ../x suffix -a\n", "image name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := repoWith(t, tc.mod, "Dockerfile")
			_, err := readConfig(dir, filepath.Dir(dir))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// With no docker.mod, ./Dockerfile is the release image.
func TestReadConfigWithNoFileBuildsTheDockerfile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Dockerfile"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	images, err := readConfig(filepath.Join(root, ".letsgo"), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Suffix != "" || images[0].Dockerfile != "Dockerfile" {
		t.Errorf("images = %+v, want ./Dockerfile as the release image", images)
	}
}

// A docker.mod that only sets the timeout still builds the default.
func TestReadConfigWithOnlyATimeoutBuildsTheDockerfile(t *testing.T) {
	dir := repoWith(t, "timeout 10m\n", "Dockerfile")
	images, err := readConfig(dir, filepath.Dir(dir))
	if err != nil || len(images) != 1 || images[0].Suffix != "" {
		t.Errorf("images = %+v, %v", images, err)
	}
}

func TestReadConfigWithNoFileNeedsADockerfile(t *testing.T) {
	root := t.TempDir()
	_, err := readConfig(filepath.Join(root, ".letsgo"), root)
	if err == nil || !strings.Contains(err.Error(), "builds ./Dockerfile") {
		t.Errorf("err = %v", err)
	}
}

// One image may leave its suffix off: that one is the release image.
func TestReadConfigAllowsOneReleaseImage(t *testing.T) {
	dir := repoWith(t, "image release (\n\ttarget talos\n)\nimage fixer (\n\ttarget fixer\n\tsuffix -fixer\n)\n",
		"Dockerfile")
	images, err := readConfig(dir, filepath.Dir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 2 || images[0].Suffix != "" || images[1].Suffix != "-fixer" {
		t.Errorf("images = %+v", images)
	}
}

// Each image is built once, from the repository root, with the released
// binaries as a named context, into a layout under the output letsgo gave.
func TestAnswerImagesBuildsEachImageIntoTheOutput(t *testing.T) {
	calls := recordDocker(t)
	dir := repoWith(t, "image fixer (\n\tsuffix -fixer\n\ttarget fixer\n\tplatform linux/arm64\n)\nimage debug suffix -debug\n",
		"Dockerfile")
	root, output, contextDir := filepath.Dir(dir), t.TempDir(), t.TempDir()

	out, err := answerImages(plugin.ImagesInput{
		ConfigDir: dir, Output: output, Context: contextDir, Date: "2026-10-13T12:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []plugin.OutputImage{
		{Suffix: "-fixer", Layout: filepath.Join(output, "fixer")},
		{Suffix: "-debug", Layout: filepath.Join(output, "debug")},
	}
	if !slices.Equal(out.Images, want) {
		t.Errorf("answer = %+v, want %+v", out.Images, want)
	}

	builds := buildsOf(*calls)
	if len(builds) != 2 {
		t.Fatalf("docker built %d times, want 2: %v", len(builds), *calls)
	}
	fixer := strings.Join(builds[0], " ")
	for _, part := range []string{
		"buildx build",
		"--builder letsgo-docker",
		"--file " + filepath.Join(root, "Dockerfile"),
		"--platform linux/arm64",
		"--build-context letsgo=" + contextDir,
		"--output type=oci,tar=false,dest=" + filepath.Join(output, "fixer"),
		"--target fixer",
		root + " ENV:1791892800",
	} {
		if !strings.Contains(fixer, part) {
			t.Errorf("build of fixer is missing %q:\n%s", part, fixer)
		}
	}
	if debug := strings.Join(builds[1], " "); strings.Contains(debug, "--target") {
		t.Errorf("an image with no target passed one: %s", debug)
	}
}

func TestAnswerImagesStopsAtTheFirstFailedBuild(t *testing.T) {
	var built int
	orig := docker
	t.Setenv("LETSGO_DOCKER", fakeExecutable(t))
	docker = func(_ context.Context, _ string, _ []string, args ...string) error {
		if args[1] != "build" {
			return nil
		}
		built++
		return os.ErrPermission
	}
	t.Cleanup(func() { docker = orig })

	dir := repoWith(t, "image a suffix -a\nimage b suffix -b\n", "Dockerfile")
	_, err := answerImages(plugin.ImagesInput{ConfigDir: dir, Output: t.TempDir(), Context: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "building image a") {
		t.Errorf("err = %v", err)
	}
	if built != 1 {
		t.Errorf("built %d images after a failure, want 1", built)
	}
}

// fakeExecutable is an executable file to name in LETSGO_DOCKER.
func fakeExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindDockerTakesAnAbsoluteOverride(t *testing.T) {
	want := fakeExecutable(t)
	t.Setenv("LETSGO_DOCKER", want)
	if got, err := findDocker(); err != nil || got != want {
		t.Errorf("findDocker = %q, %v, want %q", got, err, want)
	}
}

func TestFindDockerRefusesAnOverrideItCannotTrust(t *testing.T) {
	notExecutable := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(notExecutable, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]string{
		"docker":      "absolute path",
		notExecutable: "not an executable",
	} {
		t.Setenv("LETSGO_DOCKER", value)
		if _, err := findDocker(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("LETSGO_DOCKER=%q: err = %v, want one mentioning %q", value, err, want)
		}
	}
}

// Without the override only the system directories are searched; PATH,
// whatever it holds, is not.
func TestFindDockerIgnoresPath(t *testing.T) {
	t.Setenv("LETSGO_DOCKER", "")
	orig := systemDirs
	systemDirs = []string{t.TempDir()}
	t.Cleanup(func() { systemDirs = orig })
	t.Setenv("PATH", filepath.Dir(fakeExecutable(t)))

	if _, err := findDocker(); err == nil || !strings.Contains(err.Error(), "LETSGO_DOCKER") {
		t.Errorf("err = %v, want docker not found, naming the override", err)
	}
}

// The build runs with a PATH of the system directories and docker's own,
// not whatever PATH letsgo was started with.
func TestBuildEnvFixesPath(t *testing.T) {
	t.Setenv("PATH", "/home/someone/bin")
	env, err := buildEnv("", "/opt/docker/bin")
	if err != nil {
		t.Fatal(err)
	}
	path := lookup(env, "PATH")
	if strings.Contains(path, "/home/someone/bin") || !strings.HasSuffix(path, "/opt/docker/bin") {
		t.Errorf("PATH = %q", path)
	}
	if _, err := buildEnv("yesterday", "/usr/bin"); err == nil {
		t.Error("a commit date that is not RFC 3339 was accepted")
	}
}

// Without the override, the first system directory holding docker wins.
func TestFindDockerLooksInTheSystemDirectories(t *testing.T) {
	t.Setenv("LETSGO_DOCKER", "")
	want := fakeExecutable(t)
	orig := systemDirs
	systemDirs = []string{t.TempDir(), filepath.Dir(want)}
	t.Cleanup(func() { systemDirs = orig })

	if got, err := findDocker(); err != nil || got != want {
		t.Errorf("findDocker = %q, %v, want %q", got, err, want)
	}
}

// The real build runs the docker it was given, with the arguments and
// environment it was given, and keeps stdout for the hook's answer.
func TestDockerRunsTheGivenBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake docker is a shell script")
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho \"$@ $SOURCE_DATE_EPOCH\" > " + record + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := docker(context.Background(), bin, []string{"SOURCE_DATE_EPOCH=42"}, "buildx", "build"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(record)
	if err != nil || strings.TrimSpace(string(got)) != "buildx build 42" {
		t.Errorf("docker saw %q, %v", got, err)
	}

	failing := filepath.Join(dir, "failing")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := docker(context.Background(), failing, nil, "buildx"); err == nil {
		t.Error("a failed build reported success")
	}
}

func TestAnswerImagesStopsWithoutDocker(t *testing.T) {
	t.Setenv("LETSGO_DOCKER", "relative/docker")
	dir := repoWith(t, "", "Dockerfile")
	_, err := answerImages(plugin.ImagesInput{ConfigDir: dir, Output: t.TempDir(), Context: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Errorf("err = %v", err)
	}
}

func TestAnswerImagesRefusesABadCommitDate(t *testing.T) {
	recordDocker(t)
	dir := repoWith(t, "", "Dockerfile")
	_, err := answerImages(plugin.ImagesInput{ConfigDir: dir, Output: t.TempDir(), Context: t.TempDir(), Date: "soon"})
	if err == nil || !strings.Contains(err.Error(), "RFC 3339") {
		t.Errorf("err = %v", err)
	}
}

func TestReadConfigRefusesShortOrUnparseableLines(t *testing.T) {
	for mod, want := range map[string]string{
		"image fixer\n":            "expected `image <name> <setting> <value>`",
		"image fixer target a b\n": "expected one value for target",
		"image fixer (\n":          "docker config",
		"image fixer platform\n":   "expected `image <name> <setting> <value>`",
	} {
		dir := repoWith(t, mod, "Dockerfile")
		if _, err := readConfig(dir, filepath.Dir(dir)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want one mentioning %q", mod, err, want)
		}
	}
}

// buildsOf keeps the calls that built an image, leaving out the builder's.
func buildsOf(calls [][]string) [][]string {
	var out [][]string
	for _, c := range calls {
		if len(c) > 1 && c[1] == "build" {
			out = append(out, c)
		}
	}
	return out
}

// fakeBuilder stands in for docker's buildx builder commands: inspect finds
// the builder when it exists, and create records its arguments and fails when
// told to. It returns where the create call's arguments land.
func fakeBuilder(t *testing.T, exists, createFails bool) *[]string {
	t.Helper()
	var created []string
	orig := docker
	docker = func(_ context.Context, _ string, _ []string, args ...string) error {
		switch {
		case args[1] == "inspect" && !exists:
			return os.ErrNotExist
		case args[1] == "create":
			created = args
			if createFails {
				return os.ErrPermission
			}
		}
		return nil
	}
	t.Cleanup(func() { docker = orig })
	return &created
}

// An existing builder is used as it is.
func TestEnsureBuilderUsesAnExistingBuilder(t *testing.T) {
	created := fakeBuilder(t, true, false)
	if err := ensureBuilder(context.Background(), "/usr/bin/docker", nil); err != nil {
		t.Fatal(err)
	}
	if *created != nil {
		t.Errorf("created %v over an existing builder", *created)
	}
}

// A missing builder is created once, as a docker-container builder.
func TestEnsureBuilderCreatesAMissingBuilder(t *testing.T) {
	created := fakeBuilder(t, false, false)
	if err := ensureBuilder(context.Background(), "/usr/bin/docker", nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*created, " "); got != "buildx create --name letsgo-docker --driver docker-container" {
		t.Errorf("create = %q", got)
	}
}

func TestEnsureBuilderReportsAFailedCreate(t *testing.T) {
	fakeBuilder(t, false, true)
	if err := ensureBuilder(context.Background(), "/usr/bin/docker", nil); err == nil {
		t.Error("a builder that could not be created was reported as ready")
	}
}

// A suffix that could spell another version's tag is refused before docker
// runs, by the same rule letsgo holds the answer to.
func TestReadConfigRefusesASuffixThatSpellsAVersion(t *testing.T) {
	for _, suffix := range []string{"-rc.1", ".4", "fixer"} {
		dir := repoWith(t, "image a suffix "+suffix+"\n", "Dockerfile")
		if _, err := readConfig(dir, filepath.Dir(dir)); err == nil || !strings.Contains(err.Error(), "another version's tag") {
			t.Errorf("suffix %q: err = %v", suffix, err)
		}
	}
}

// A builder that cannot be created stops the hook before any build.
func TestAnswerImagesStopsWithoutABuilder(t *testing.T) {
	t.Setenv("LETSGO_DOCKER", fakeExecutable(t))
	orig := docker
	var builds int
	docker = func(_ context.Context, _ string, _ []string, args ...string) error {
		if args[1] == "build" {
			builds++
			return nil
		}
		return os.ErrPermission
	}
	t.Cleanup(func() { docker = orig })

	dir := repoWith(t, "", "Dockerfile")
	_, err := answerImages(plugin.ImagesInput{ConfigDir: dir, Output: t.TempDir(), Context: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "letsgo-docker buildx builder") || builds != 0 {
		t.Errorf("err = %v, builds = %d", err, builds)
	}
}
