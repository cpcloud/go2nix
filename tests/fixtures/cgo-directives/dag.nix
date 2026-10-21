# Test: default mode with cgo packages whose compiler and linker flags come
# from `#cgo` lines in the Go sources: CFLAGS and CPPFLAGS macros, a
# `-I${SRCDIR}/...` include path, LDFLAGS, CXXFLAGS, pkg-config Cflags that a
# C++ file needs, and lines with a GOOS condition; and a package that gets
# CGO_CPPFLAGS and CGO_LDFLAGS from its derivation's environment instead.
#
# `go tool cgo` drops these lines; compile-package has to do with them what
# cmd/go does. Without that, internal/define and internal/goos still build
# and return the wrong values, internal/srcdir and internal/pcxx fail to
# compile, internal/libm fails when the application is linked, and
# internal/envflags does not see its macro.
let
  pkgs = import <nixpkgs> { };
  inherit (pkgs) go;
  go2nix = import ../../../packages/go2nix { inherit pkgs; };
  goEnv = import ../../../nix/mk-go-env.nix {
    inherit go go2nix;
    inherit (pkgs) callPackage;
  };

  # A header-only library whose .pc file adds a directory below include/ and
  # a macro: cc-wrapper puts $out/include on the search path, not these.
  bar = pkgs.runCommand "bar-2.0" { } ''
    mkdir -p $out/include/bar-2 $out/lib/pkgconfig
    echo '#define BAR_HEADER 9' > $out/include/bar-2/bar.h
    cat > $out/lib/pkgconfig/bar.pc <<EOF
    Name: bar
    Description: header-only test library
    Version: 2.0
    Cflags: -I$out/include/bar-2 -DBAR_FROM_PC=5
    EOF
  '';
in
goEnv.buildGoApplication {
  pname = "cgo-directives";
  version = "0.0.1";
  src = ./.;
  goLock = ./go2nix.toml;
  packageOverrides = {
    "example.com/cgo-directives/internal/pcxx" = {
      nativeBuildInputs = [
        pkgs.pkg-config
        bar
      ];
    };
    "example.com/cgo-directives/internal/envflags".env = {
      CGO_CPPFLAGS = "-DFROM_ENV_CPPFLAGS=17";
      CGO_LDFLAGS = "-lm";
    };
  };
}
