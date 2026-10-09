{
  description = "Doomer CLI development environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [
            (final: prev: {
              go_1_26 = prev.go_1_26.overrideAttrs {
                version = "1.26.6";
                src = prev.fetchurl {
                  url = "https://go.dev/dl/go1.26.6.src.tar.gz";
                  hash = "sha256-oHIcVMaIkBRI13rZs+x+p8R0cwdV/4kTgukuy5P/LLE=";
                };
              };
            })
          ];
        };
        go = pkgs.go_1_26;
      in
      {
        devShells.default = pkgs.mkShell {
          packages = [
            go
            pkgs.gnumake
            pkgs.git
            pkgs.nodejs_22
            pkgs.actionlint
            pkgs.shellcheck
            pkgs.gnutar
            pkgs.gzip
            pkgs.curl
            pkgs.xz
          ];

          shellHook = ''
            if [ "$(${go}/bin/go env GOVERSION)" != "go1.26.6" ]; then
              echo "expected Go 1.26.6, got $(${go}/bin/go env GOVERSION)" >&2
              return 1
            fi
          '';
        };
      });
}
