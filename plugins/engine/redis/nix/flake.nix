# Embedded services-flake wrapper consumed by the bough-plugin-redis
# binary. The plugin extracts this file to
# `<worktree>/.local/bough-redis-flake/` at Up time, then invokes
# `nix run --impure 'path:<extracted>#redis' -- up --tui=false`.
#
# Two env vars steer per-worktree behaviour at flake-eval time:
#
#   BOUGH_REDIS_PORT    — listen port (default 6379)
#   BOUGH_REDIS_DATADIR — redis data directory (default
#                          ./.local/redis-data relative to the flake's cwd)
{
  inputs = {
    nixpkgs.url = "https://flakehub.com/f/NixOS/nixpkgs/*.tar.gz";
    flake-parts.url = "github:hercules-ci/flake-parts";
    services-flake.url = "github:juspay/services-flake";
    process-compose-flake.url = "github:Platonic-Systems/process-compose-flake";
  };

  outputs =
    inputs@{ flake-parts, ... }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      systems = [
        "aarch64-darwin"
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
      ];

      imports = [
        inputs.process-compose-flake.flakeModule
      ];

      perSystem =
        { pkgs, lib, ... }:
        let
          dbPort =
            let
              envPort = builtins.getEnv "BOUGH_REDIS_PORT";
            in
            if envPort == "" then 6379 else lib.toInt envPort;

          dataDir =
            let
              d = builtins.getEnv "BOUGH_REDIS_DATADIR";
            in
            if d == "" then "./.local/redis-data" else d;
        in
        {
          process-compose."redis" = {
            imports = [
              inputs.services-flake.processComposeModules.default
            ];
            services.redis."bough" = {
              enable = true;
              # Redis 7 — current stable line; pin the major so a
              # `nix flake update` upstream doesn't silently bump under
              # a long-lived worktree.
              package = pkgs.redis;
              dataDir = dataDir;
              bind = "127.0.0.1";
              port = dbPort;
              # No requirePass: local dev convention matches the
              # bough-managed mysql / postgres plugins (the operator
              # never exposes these to the network).
            };
          };
        };
    };
}
