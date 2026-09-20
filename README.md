# letsgo-plugins

Plugins for [letsgo](https://github.com/danielriddell21/letsgo).

letsgo does the things a Go release always needs. These do the things some
repositories need and most do not, and they live outside it so that letsgo's
own configuration stays a closed set.

| plugin | hook | what it does |
| --- | --- | --- |
| `letsgo-multi` | `archive-layout` | ships every command in one archive per target, and one formula that installs them all |
| `letsgo-env` | `ldflags` | compiles values from the environment into the binary |
| `letsgo-cask` | none — reads the published release | writes a Homebrew cask for a macOS build |

## The contract

A plugin may not change the released bytes unless its output is recorded.

letsgo runs a plugin with the hook's name as its only argument, writes one JSON
object to stdin and reads one from stdout. Whatever comes back is written into
`letsgo.json`, so `letsgo verify` replays the recorded answer and never runs a
plugin — a release stays reproducible on a machine that has none of these
installed.

A plugin that answers a hook is pinned by digest, because a program that
decides what gets built is a build input exactly as the compiler is. A plugin
that only reads a finished release is not, because there is nothing left for it
to decide.

## Installing

Download the plugin from a [letsgo-plugins release][releases] and put it on
`PATH`. In the workflow that publishes the release:

```yaml
- name: Install letsgo-multi
  run: |
    set -euo pipefail
    version=v0.1.0
    base=https://github.com/danielriddell21/letsgo-plugins/releases/download/$version
    curl -fsSL "$base/letsgo-multi_${version#v}_linux_amd64.tar.gz" \
      | sudo tar -xz -C /usr/local/bin letsgo-multi
```

## Pinning

A plugin that answers a hook decides what gets built, so it is a build input
exactly as the compiler is, and `letsgo.mod` pins it by the digest of the
executable:

```
plugin archive-layout letsgo-multi v0.1.0 sha256:<binary_sha256 from the release>
plugin ldflags        letsgo-env    v0.1.0 sha256:<binary_sha256 from the release>
```

The digest is in `letsgo.json`, published with every release: find the artifact
for the platform that runs your release and take its `binary_sha256` — the
digest of the executable inside the archive, not of the archive. letsgo hashes
the executable before running it and refuses to continue if it is not the one
pinned.

**`letsgo-cask` is not pinned**, because it answers no hook. It runs after the
release is over and reads what was published, so it cannot change a byte of it.
Install it and run it; there is nothing to declare in `letsgo.mod`.

### Not `go install`

The obvious recipe cannot satisfy a pin, and failing mysteriously later would
be worse than saying so here:

```sh
go install github.com/danielriddell21/letsgo-plugins/cmd/letsgo-env@v0.1.0  # not for a pinned plugin
```

`go install` does not pass `-trimpath`, so the build directory is compiled into
the binary. Two machines with different `GOPATH` values produce different bytes
from the same source at the same version, so there is no one digest to pin:

```
$ GOPATH=/tmp/a go install …/letsgo-env@latest && sha256sum /tmp/a/bin/letsgo-env
f0877414…
$ GOPATH=/tmp/b go install …/letsgo-env@latest && sha256sum /tmp/b/bin/letsgo-env
e3c3c34a…
```

The released binaries are built by letsgo with `-trimpath -buildvcs=false`, so
their digests are a function of the source and the Go version and nothing else.
That is what makes them pinnable, and it is the same property the plugins exist
to protect. `go install` is fine for `letsgo-cask`, which nothing pins.

### One pin, one platform

A `plugin` line carries a single digest, and a plugin binary differs per
platform, so a pin matches the platform that publishes the release — normally
`linux/amd64` on CI. That is enough for releasing and for verifying: `letsgo
verify` replays the answer recorded in `letsgo.json` and never runs a plugin,
so a release can be checked on a machine that has none of these installed.
Running `letsgo release` by hand on another platform needs that platform's pin.

[releases]: https://github.com/danielriddell21/letsgo-plugins/releases

## letsgo-multi

A repository whose product is a collection of small tools ships the collection.
Without this, eleven commands become fifty-five downloads and eleven Homebrew
formulas — and if any tool shares a name with a Homebrew core package, its
formula shadows the core one.

No configuration: every command goes into one archive named after the project.

## letsgo-env

letsgo's `ldflags` are literal on purpose, so that a release is a function of
its commit. This injects values from the environment instead, and records them.

Configure it in `letsgo-env.mod`, beside `letsgo.mod`:

```
inject internal/telemetry.otelEndpoint  OTEL_ENDPOINT
inject internal/telemetry.otelAuthToken OTEL_AUTH_TOKEN
```

Every named variable must be set, or the release fails — injecting an empty
string silently is how a release ships a binary that cannot phone home and does
not say why.

**Nothing injected this way is a secret.** A value passed to `-X` is compiled
into the binary and recoverable from a published artifact with `strings`, and
letsgo records it in `letsgo.json` so that the build can be reproduced. It was
already public the moment it shipped. letsgo says so at plan time:

```
! plugins  letsgo-env compiled 2 value(s) into the binary: …
             they are recoverable with `strings` and recorded in letsgo.json,
             so they are not secrets
```

A value that must stay secret belongs in the environment the program runs in,
not in the program.

## letsgo-cask

letsgo writes formulas, not casks. A formula is the right shape for a
command-line program; a repository shipping a windowed build alongside its CLI
wants both.

This is not a hook. It reads the `letsgo.json` a release already published and
writes a cask from it, so there is nothing to pin and nothing it can do to the
bytes — by the time it runs, the release is over.

```sh
letsgo-cask dist/letsgo.json --repo you/gambit \
  --desc "Watch two chess agents play in a native macOS window" \
  --license MIT \
  --caveats "The board opens a window and is macOS-only."
```

`--variant` names the variant whose archives the cask installs, as spelled in
`letsgo.mod`; without it the release's own archives are used. The cask goes to
stdout, or to the file named by `-o`. Committing it to a tap is git's job.

`--desc`, `--license` and `--caveats` describe the program rather than the
artifacts, so the release cannot supply them and they are passed in. Each is
omitted from the cask when empty rather than guessed at. `--caveats` renders as
a heredoc, so it can run to several lines.

It emits `binary`, not `app`: letsgo publishes an executable, and claiming an
`.app` would name something the archive does not contain.
