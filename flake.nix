{
  inputs = {
    nixpkgs.url = "https://flakehub.com/f/NixOS/nixpkgs/*.tar.gz";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      flake-utils,
      nixpkgs,
      ...
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        go = pkgs.go_1_25;
        formatter = pkgs.nixfmt-tree;

        # buildGoModule produces all 5 binaries (host + 4 plugin) into one
        # store path. `nix run github:threecorp/bough` invokes the host
        # binary; nix profile install drops every binary into the user's
        # profile so `bough-plugin-*` are discoverable on PATH.
        bough = pkgs.buildGoModule {
          pname = "bough";
          version = "0.1.1";
          src = ./.;
          vendorHash = "sha256-hsAAD7X1xt5l27ITiprPlhwdDY2NNoI0aJH0bVB29Bw=";
          subPackages = [
            "cmd/bough"
            "cmd/bough-plugin-mysql"
            "cmd/bough-plugin-postgres"
            "cmd/bough-plugin-redis"
            "cmd/bough-plugin-elasticsearch"
          ];
          # Mirror .goreleaser.yaml host flags so `nix run` reports the
          # same version string as the GitHub-Release tarball binaries.
          ldflags = [
            "-s"
            "-w"
            "-X main.version=0.1.1"
          ];
          meta = {
            description = "Per-worktree isolation orchestrator for monorepos";
            homepage = "https://github.com/threecorp/bough";
            license = pkgs.lib.licenses.mit;
            mainProgram = "bough";
          };
        };
      in
      {
        # Package + app entries make `nix run github:threecorp/bough`
        # and `nix profile install github:threecorp/bough` actually work.
        # Until v0.1.1 these output names were missing so both invocations
        # were no-ops on the alpha tag.
        packages.default = bough;
        apps.default = {
          type = "app";
          program = "${bough}/bin/bough";
        };

        # CI devShell — minimal toolset for go test / golangci-lint / nix flake check.
        # Kept lean so the GHA Nix cache restore is fast.
        devShells.ci = pkgs.mkShellNoCC {
          packages = [
            go
            pkgs.gnumake
            pkgs.git
            pkgs.protobuf
            pkgs.protoc-gen-go
            pkgs.protoc-gen-go-grpc
            pkgs.golangci-lint
            pkgs.actionlint
            formatter
          ];

          shellHook = ''
            export GOPATH=''${GOPATH:-$HOME/go}
            export PATH=$GOPATH/bin:$PATH
          '';
        };

        # Default devShell — CI toolchain plus the editor + LSP / debugger
        # / search utilities a `direnv allow` + neovim session needs. None
        # of these land in the release tarballs (GoReleaser builds outside
        # the devShell), so adding them here only costs the local
        # contributor's `nix develop` warmup.
        devShells.default = pkgs.mkShellNoCC {
          inputsFrom = [ (pkgs.mkShellNoCC { }) ];
          packages = [
            go
            pkgs.gnumake
            pkgs.git
            pkgs.protobuf
            pkgs.protoc-gen-go
            pkgs.protoc-gen-go-grpc
            pkgs.golangci-lint
            pkgs.actionlint
            pkgs.nil
            pkgs.goreleaser

            # Editor + Go LSP / debugger / search — pinned via nixpkgs
            # so a fresh checkout reproduces the same toolchain a long-
            # term contributor already runs locally.
            pkgs.neovim
            pkgs.gopls
            pkgs.delve
            pkgs.gotools         # goimports / godoc / stringer / etc.
            pkgs.ripgrep
            pkgs.fd

            formatter
          ];

          shellHook = ''
            export GOPATH=''${GOPATH:-$HOME/go}
            export PATH=$GOPATH/bin:$PATH
          '';
        };

        inherit formatter;
      }
    );
}
