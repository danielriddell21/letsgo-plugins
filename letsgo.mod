// letsgo-plugins is itself a multi-command repository, so it is the first
// thing letsgo-multi is used on: it releases itself with the plugin it ships.
//
// The pin is deliberately absent: a release cannot record its own digest in
// the source it is built from.
build linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
