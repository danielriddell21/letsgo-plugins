// Command letsgo-docker builds container images from Dockerfiles for letsgo
// to publish.
//
// letsgo builds images of its own from the binary, with no Dockerfile and no
// daemon, and can rebuild them to the byte. An image that needs more than the
// binary (packages, files, a shell) needs a Dockerfile, and this plugin
// answers letsgo's images hook by building one with docker buildx.
//
// It builds and nothing else. Each image is written as an OCI image layout
// into the directory letsgo hands it; letsgo pushes it with its own
// credential under every tag the release pushes, with the image's suffix
// appended, and records its digest in letsgo.json. This process never sees a
// token.
//
// # Configuration
//
// With no config at all it builds ./Dockerfile, for this machine's platform,
// as the release image itself. letsgo.mod then says so with `image plugin`,
// or has no image directive at all:
//
//	image plugin
//	plugin images letsgo-docker v0.1.0 sha256:…
//
// More images, or other settings, go in .letsgo/docker.mod:
//
//	timeout 45m
//
//	image release (
//		target talos
//	)
//
//	image fixer (
//		dockerfile build/fixer.Dockerfile
//		target     fixer
//		suffix     -fixer
//		platform   linux/amd64
//	)
//
// The image with no suffix is the release image, and there is at most one.
// Every other image is pushed with its suffix appended to every tag:
// 1.2.3-fixer, latest-fixer and so on. dockerfile defaults to Dockerfile at
// the repository root, as `docker build` does. target is the stage to build,
// the last one when unset. platform defaults to this machine's own, so
// nothing needs emulation; name more than one only on a builder that can
// build them. timeout is read by letsgo, not here: it bounds this whole run,
// 30m when unset and at most 2h.
//
// # The released binaries
//
// The release's Linux binaries are staged in a build context named letsgo,
// as <os>_<arch>/<binary>, so an image ships the bytes the release published
// rather than compiling its own:
//
//	ARG TARGETOS TARGETARCH
//	COPY --from=letsgo ${TARGETOS}_${TARGETARCH}/talos /usr/local/bin/talos
//
// SOURCE_DATE_EPOCH is set to the commit's time for every build, which buildx
// uses for the image's timestamps.
//
// # The builder
//
// Images are built with a buildx builder named letsgo-docker, using the
// docker-container driver, which is created on first use. docker's default
// builder cannot export an OCI layout unless the daemon uses the containerd
// image store, which a GitHub runner does not by default.
//
// # Finding docker
//
// docker is looked for in /usr/bin, /bin, /usr/sbin and /sbin, never on PATH.
// Elsewhere, such as Docker Desktop's /usr/local/bin/docker on macOS, name it
// with LETSGO_DOCKER=/usr/local/bin/docker; letsgo reads the same variable
// when it checks for docker while planning.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/danielriddell21/letsgo/plugin"
)

func main() {
	plugin.Main(plugin.HookImages, answerImages)
}

// image is one image block of images.mod.
type image struct {
	Name       string
	Dockerfile string
	Target     string
	Suffix     string
	Platforms  []string

	line plugin.ConfigLine
}

// namePattern keeps an image's name usable as a directory under the output.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// dockerEnv names docker's absolute path, for a machine where it is not in
// a system directory: Docker Desktop's /usr/local/bin/docker on macOS.
const dockerEnv = "LETSGO_DOCKER"

// systemDirs are the directories docker is looked for in without
// dockerEnv: ones an ordinary user cannot write to. PATH is never consulted,
// because whoever controls PATH would choose what runs, and letsgo makes the
// same choice when it probes for docker while planning.
var systemDirs = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}

// findDocker is docker's absolute path: dockerEnv's, or the first in
// systemDirs.
func findDocker() (string, error) {
	if value := os.Getenv(dockerEnv); value != "" {
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("%s %q must be an absolute path", dockerEnv, value)
		}
		if !executable(value) {
			return "", fmt.Errorf("%s %q is not an executable file", dockerEnv, value)
		}
		return value, nil
	}
	for _, dir := range systemDirs {
		if candidate := filepath.Join(dir, "docker"); executable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("docker is not in %s; set %s to its absolute path",
		strings.Join(systemDirs, ", "), dockerEnv)
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// docker runs one buildx build with the docker at bin. A variable so tests
// can stand in for it.
var docker = func(ctx context.Context, bin string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	// buildx reports progress on stderr, and stdout is this hook's answer:
	// anything the build prints goes to stderr, which letsgo shows on failure.
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// answerImages builds every image images.mod names, one at a time, and
// answers with where each layout was written.
func answerImages(in plugin.ImagesInput) (plugin.ImagesOutput, error) {
	// letsgo stops a hook past its timeout by killing its process group; a
	// signal from anyone else, an interrupted terminal most often, stops the
	// build the same way rather than leaving it running.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := filepath.Dir(in.ConfigDir)
	images, err := readConfig(in.ConfigDir, root)
	if err != nil {
		return plugin.ImagesOutput{}, err
	}

	bin, err := findDocker()
	if err != nil {
		return plugin.ImagesOutput{}, err
	}
	env, err := buildEnv(in.Date, filepath.Dir(bin))
	if err != nil {
		return plugin.ImagesOutput{}, err
	}

	if err := ensureBuilder(ctx, bin, env); err != nil {
		return plugin.ImagesOutput{}, err
	}

	out := plugin.ImagesOutput{Images: make([]plugin.OutputImage, 0, len(images))}
	for _, img := range images {
		dest := filepath.Join(in.Output, img.Name)
		if err := docker(ctx, bin, env, buildArgs(img, root, in.Context, dest)...); err != nil {
			return plugin.ImagesOutput{}, fmt.Errorf("building image %s: %w", img.Name, err)
		}
		out.Images = append(out.Images, plugin.OutputImage{Suffix: img.Suffix, Layout: dest})
	}
	return out, nil
}

// builder is the buildx builder every image is built with.
//
// docker's own default builder, the "docker" driver, can export an OCI layout
// only when the daemon uses the containerd image store, which a GitHub runner
// does not by default. A docker-container builder always can. It is named
// rather than made the default, so a machine's own choice of builder is left
// as it was.
const builder = "letsgo-docker"

// ensureBuilder creates the builder if this machine does not have it yet.
func ensureBuilder(ctx context.Context, bin string, env []string) error {
	if docker(ctx, bin, env, "buildx", "inspect", builder) == nil {
		return nil
	}
	if err := docker(ctx, bin, env, "buildx", "create", "--name", builder, "--driver", "docker-container"); err != nil {
		return fmt.Errorf("creating the %s buildx builder: %w", builder, err)
	}
	return nil
}

// buildArgs is the buildx command line for one image.
func buildArgs(img image, root, contextDir, dest string) []string {
	args := []string{
		"buildx", "build",
		"--builder", builder,
		"--file", filepath.Join(root, img.Dockerfile),
		"--platform", strings.Join(img.Platforms, ","),
		"--build-context", "letsgo=" + contextDir,
		"--output", "type=oci,tar=false,dest=" + dest,
	}
	if img.Target != "" {
		args = append(args, "--target", img.Target)
	}
	return append(args, root)
}

// buildEnv is this process's environment with SOURCE_DATE_EPOCH set to the
// commit's time, so an image is stamped with when its source was written
// rather than when it happened to be built, and PATH fixed to the system
// directories and docker's own, which is where docker finds what it runs in
// turn, such as a credential helper.
func buildEnv(date, dockerDir string) ([]string, error) {
	env := make([]string, 0, len(os.Environ())+2)
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !strings.EqualFold(name, "PATH") {
			env = append(env, kv)
		}
	}
	env = append(env, "PATH="+strings.Join(append(append([]string{}, systemDirs...), dockerDir), string(os.PathListSeparator)))
	if date == "" {
		return env, nil
	}
	t, err := time.Parse(time.RFC3339, date)
	if err != nil {
		return nil, fmt.Errorf("the commit date %q is not RFC 3339: %w", date, err)
	}
	return append(env, "SOURCE_DATE_EPOCH="+strconv.FormatInt(t.Unix(), 10)), nil
}

// readConfig parses images.mod into the images to build, in the order they
// are written.
//
// With no docker.mod, the one image is ./Dockerfile as the release image.
func readConfig(configDir, root string) ([]image, error) {
	cfg, err := plugin.LoadConfig(configDir, "docker")
	if errors.Is(err, fs.ErrNotExist) {
		return defaults(root)
	}
	if err != nil {
		return nil, fmt.Errorf("reading the docker config: %w", err)
	}

	images, err := readImages(cfg)
	if err != nil {
		return nil, err
	}
	// A docker.mod that only sets a timeout still builds the default.
	if len(images) == 0 {
		return defaults(root)
	}

	suffixes := map[string]string{}
	out := make([]image, 0, len(images))
	for _, img := range images {
		if other, ok := suffixes[img.Suffix]; ok {
			if img.Suffix == "" {
				return nil, cfg.Errorf(img.line,
					"images %s and %s both have no suffix, and only one can be the release image", other, img.Name)
			}
			return nil, cfg.Errorf(img.line, "images %s and %s share the suffix %s", other, img.Name, img.Suffix)
		}
		suffixes[img.Suffix] = img.Name

		if err := settle(img, root); err != nil {
			return nil, cfg.Errorf(img.line, "%v", err)
		}
		out = append(out, *img)
	}
	return out, nil
}

// readImages gathers docker.mod's image lines into images, in the order
// they are first named.
func readImages(cfg *plugin.Config) ([]*image, error) {
	var images []*image
	byName := map[string]*image{}
	set := map[string]bool{}

	for _, l := range cfg.Lines {
		switch l.Keyword {
		case "timeout":
			// letsgo's to enforce, and it already has.
			continue
		case "image":
		default:
			return nil, cfg.Errorf(l, "unknown directive %q; docker.mod has timeout and image", l.Keyword)
		}

		if len(l.Args) < 3 {
			return nil, cfg.Errorf(l, "expected `image <name> <setting> <value>`")
		}
		name, setting := l.Args[0], l.Args[1]
		if !namePattern.MatchString(name) {
			return nil, cfg.Errorf(l, "image name %q must be letters, digits, '.', '-' or '_'", name)
		}

		img, ok := byName[name]
		if !ok {
			img = &image{Name: name, line: l}
			byName[name] = img
			images = append(images, img)
		}
		if set[name+"\x00"+setting] {
			return nil, cfg.Errorf(l, "image %s: %s is already set", name, setting)
		}
		set[name+"\x00"+setting] = true

		if err := img.set(setting, l.Args[2:]); err != nil {
			return nil, cfg.Errorf(l, "image %s: %v", name, err)
		}
	}
	return images, nil
}

// set applies one setting of an image block.
func (img *image) set(setting string, values []string) error {
	if setting != "platform" && len(values) != 1 {
		return fmt.Errorf("expected one value for %s", setting)
	}
	switch setting {
	case "dockerfile":
		img.Dockerfile = values[0]
	case "target":
		img.Target = values[0]
	case "suffix":
		img.Suffix = values[0]
	case "platform":
		img.Platforms = values
	default:
		return fmt.Errorf("unknown setting %q; an image has dockerfile, target, suffix and platform", setting)
	}
	return nil
}

// defaults is what a repository with no docker.mod gets: ./Dockerfile, built
// as the release image.
func defaults(root string) ([]image, error) {
	img := &image{Name: "release"}
	if err := settle(img, root); err != nil {
		return nil, fmt.Errorf("%w; with no .letsgo/docker.mod, letsgo-docker builds ./Dockerfile", err)
	}
	return []image{*img}, nil
}

// settle fills in an image's defaults and checks its Dockerfile is there.
func settle(img *image, root string) error {
	if img.Suffix != "" {
		if err := plugin.CheckSuffix(img.Suffix); err != nil {
			return fmt.Errorf("image %s: %w", img.Name, err)
		}
	}
	if img.Dockerfile == "" {
		img.Dockerfile = "Dockerfile"
	}
	if filepath.IsAbs(img.Dockerfile) || !filepath.IsLocal(img.Dockerfile) {
		return fmt.Errorf("image %s: dockerfile %s must be inside the repository", img.Name, img.Dockerfile)
	}
	if _, err := os.Stat(filepath.Join(root, img.Dockerfile)); err != nil {
		return fmt.Errorf("image %s: %w", img.Name, err)
	}
	if len(img.Platforms) == 0 {
		img.Platforms = []string{"linux/" + runtime.GOARCH}
	}
	return nil
}
