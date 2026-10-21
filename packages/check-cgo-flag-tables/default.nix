# Verify the cgo flag lists match the upstream Go toolchain.
#
# Runs the check script that compares
# go/go2nix/pkg/compile/cgosecurity.go against
# $GOROOT/src/cmd/go/internal/work/security.go.
{ pkgs }:
let
  go = pkgs.go_1_26;
in
pkgs.runCommand "check-cgo-flag-tables"
  {
    nativeBuildInputs = [ go ];
    src = pkgs.lib.sources.cleanSource ../..;
  }
  ''
    export HOME=$(mktemp -d)
    export GOCACHE=$HOME/.cache/go-build

    cd $src
    go run scripts/check-cgo-flag-tables.go

    touch $out
  ''
