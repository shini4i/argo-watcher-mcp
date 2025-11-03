{
  description = "Go development environment for argo-watcher-mcp";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
        };
      in
      {
        packages.argo-watcher-mcp = pkgs.buildGoModule {
          pname = "argo-watcher-mcp";
          version = "0.0.0-dev";
          src = ./.;
          vendorHash = pkgs.lib.fakeSha256;
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go_1_25
            golangci-lint
            gotestsum
            air
            tree
          ];

          env = {
            GO111MODULE = "on";
            CGO_ENABLED = "0";
          };

          shellHook = ''
            echo "Entering Go dev shell. Run 'go test ./...' or 'golangci-lint run'."
          '';
        };
      }
    );
}
