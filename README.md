# letsgo-plugins

Plugins for [letsgo](https://github.com/danielriddell21/letsgo): the things
some repositories need at release time and most do not.

| plugin | hook | what it does |
| --- | --- | --- |
| `letsgo-multi` | `archive-layout` | ships every command in one archive per target, and one formula that installs them all |
| `letsgo-env` | `ldflags` | compiles values from the environment into the binary |
| `letsgo-cask` | none — reads the published release | writes a Homebrew cask for a macOS build |

```sh
letsgo plugin install letsgo-multi@v0.2.0
```

Usage, the plugin contract and why each one is pinned by digest are documented
in the [letsgo wiki](https://github.com/danielriddell21/letsgo/wiki/Plugins).
