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
      let
        goPackage = pkgs.buildGoModule {
          pname = "argo-watcher-mcp";
          version = "0.0.0-dev";
          src = ./.;
          vendorHash = pkgs.lib.fakeSha256;
          CGO_ENABLED = 0;
        };
      in {
        packages = {
          argo-watcher-mcp = goPackage;
          default = goPackage;
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go_1_26
            go-task
            goreleaser
            golangci-lint
            gotestsum
            govulncheck
            gosec
            air
            tree
          ];

          env = {
            GO111MODULE = "on";
            CGO_ENABLED = "0";
          };

          shellHook = ''
            echo "Go dev shell ready. Try 'task test', 'task release:snapshot', or 'golangci-lint run'."
          '';
        };
      }
    );
}
