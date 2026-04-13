{
  description = "docserve — self-hosted MCP documentation server";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    claude-code.url = "github:sadjow/claude-code-nix";
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
    claude-code,
  }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ claude-code.overlays.default ];
          config.allowUnfreePredicate = pkg: builtins.elem (nixpkgs.lib.getName pkg) [
            "claude-code"
          ];
        };
      in
      {
        devShells.default = pkgs.mkShell {
          buildInputs = [
            pkgs.claude-code

            # Go
            pkgs.go_1_26
            pkgs.gopls
            pkgs.gotools
            pkgs.golangci-lint
            pkgs.goreleaser
            pkgs.cosign

            # SQLite (debug build)
            pkgs.sqlite

            # Node.js (for MCP Inspector via npx)
            pkgs.nodejs

            # System tools
            pkgs.git
            pkgs.jq
            pkgs.curl
          ];

          shellHook = ''
            echo "dev environment loaded"
          '';
        };
      }
    );
}
