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

Every plugin is pinned by digest, because a program that decides what gets
built is a build input exactly as the compiler is.

## Installing

```sh
go install github.com/danielriddell21/letsgo-plugins/cmd/letsgo-multi@latest
```

Then pin it, in the repository's `letsgo.mod`:

```
plugin archive-layout letsgo-multi v0.1.0 sha256:<digest of the executable>
```

letsgo hashes the executable before running it and refuses to continue if it is
not the one pinned.

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
letsgo-cask dist/letsgo.json --repo you/gambit --variant gui
```

`--variant` names the variant whose archives the cask installs, as spelled in
`letsgo.mod`; without it the release's own archives are used. The cask goes to
stdout, or to the file named by `-o`. Committing it to a tap is git's job.

It emits `binary`, not `app`: letsgo publishes an executable, and claiming an
`.app` would name something the archive does not contain.
