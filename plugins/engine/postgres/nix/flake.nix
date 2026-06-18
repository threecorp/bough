# Embedded services-flake wrapper consumed by the bough-plugin-postgres
# binary. The plugin extracts this file (and its lock) to
# `<worktree>/.local/bough-postgres-flake/` at Up time, then invokes
# `nix run --impure 'path:<extracted>#postgres' -- up --tui=false`.
#
# Three env vars steer per-worktree behaviour at flake-eval time:
#
#   BOUGH_POSTGRES_PORT       — listen port (default 5432)
#   BOUGH_POSTGRES_SOCKET_DIR — directory for the Unix socket (default
#                                /tmp; lets the operator escape macOS's
#                                104-char sun_path limit even when the
#                                worktree path is very deep)
#   BOUGH_POSTGRES_DATADIR    — postgres data directory (default
#                                ./.local/postgres-data relative to the
#                                flake's cwd)
#
# `--impure` is required because builtins.getEnv only resolves when
# Nix is told the evaluation may read process env.
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
              envPort = builtins.getEnv "BOUGH_POSTGRES_PORT";
            in
            if envPort == "" then 5432 else lib.toInt envPort;

          socketDir =
            let
              s = builtins.getEnv "BOUGH_POSTGRES_SOCKET_DIR";
            in
            if s == "" then "/tmp" else s;

          dataDir =
            let
              d = builtins.getEnv "BOUGH_POSTGRES_DATADIR";
            in
            if d == "" then "./.local/postgres-data" else d;
        in
        {
          process-compose."postgres" = {
            imports = [
              inputs.services-flake.processComposeModules.default
            ];
            services.postgres."bough" = {
              enable = true;
              # PostgreSQL 16 — current widely deployed major; pin here
              # so a `nix flake update` upstream doesn't silently bump
              # the major version under a long-lived worktree.
              package = pkgs.postgresql_16;
              dataDir = dataDir;
              listen_addresses = "127.0.0.1";
              port = dbPort;
              # Move the Unix socket out of the worktree datadir to /tmp
              # (or operator-chosen socketDir). PostgreSQL constructs
              # the socket name as `.s.PGSQL.${port}` itself, so siblings
              # on shared /tmp stay collision-free via the port suffix.
              socketDir = socketDir;
              # `trust` for local dev — bough-managed mysqld follows the
              # same convention (no password). Production never sees
              # this flake.
              initialScript.before = ''
                -- placeholder for early bootstrap, executed before
                -- initialDatabases below.
              '';
              initialDatabases = [
                { name = "bough"; }
              ];
            };
          };
        };
    };
}
