# Embedded services-flake wrapper consumed by the bough-plugin-mysql
# binary. The plugin extracts this file (and its lock) to
# `<worktree>/.local/bough-mysql-flake/` at Up time, then invokes
# `nix run --impure 'path:<extracted>#mysql' -- up --tui=false`.
#
# Three env vars steer per-worktree behaviour at flake-eval time:
#
#   BOUGH_MYSQL_PORT       — listen port (default 3306; main checkout
#                             never sets this so the brew mysqld keeps
#                             owning 3306)
#   BOUGH_MYSQL_SOCKET_DIR — directory for the Unix socket (default
#                             /tmp; lets the operator escape macOS's
#                             104-char sun_path limit even when the
#                             worktree path is very deep)
#   BOUGH_MYSQL_DATADIR    — mysql data directory (default
#                             ./.local/mysql-data relative to the
#                             flake's cwd)
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
              envPort = builtins.getEnv "BOUGH_MYSQL_PORT";
            in
            if envPort == "" then 3306 else lib.toInt envPort;

          socketDir =
            let
              s = builtins.getEnv "BOUGH_MYSQL_SOCKET_DIR";
            in
            if s == "" then "/tmp" else s;

          dataDir =
            let
              d = builtins.getEnv "BOUGH_MYSQL_DATADIR";
            in
            if d == "" then "./.local/mysql-data" else d;

          socketPath = "${socketDir}/bough-mysql-${toString dbPort}.sock";
        in
        {
          process-compose."mysql" = {
            imports = [
              inputs.services-flake.processComposeModules.default
            ];
            services.mysql."bough" = {
              enable = true;
              # MySQL 8.4 LTS — parity with prod / staging and SQLBoiler's
              # types.JSON mapping. MariaDB downgrades native JSON to
              # LONGTEXT and silently breaks generated apimodel.
              package = pkgs.mysql84;
              dataDir = dataDir;
              settings.mysqld = {
                port = dbPort;
                bind-address = "127.0.0.1";
                # Socket placed under /tmp (or operator-chosen socketDir)
                # so the path stays under macOS's 104-char sun_path limit
                # even when the worktree path is deep. The port suffix
                # keeps sibling worktrees collision-free on shared /tmp.
                socket = socketPath;
                # X Protocol disabled: bough only connects via TCP. Keeps
                # the 33060 default from clashing across sibling mysqld
                # instances and avoids mysqlx_socket inheriting a too-long
                # datadir path.
                mysqlx = "OFF";
              };
              initialDatabases = [
                { name = "bough"; }
              ];
            };

            # services-flake hardcodes its readiness probe to
            # `mysqladmin --socket=./.local/mysql-data/mysql.sock ping`.
            # That path ignores our settings.mysqld.socket override (which
            # moves the socket to /tmp), so the probe never finds the
            # socket and times out 4× in 40s — process-compose then
            # SIGTERM-s the otherwise-healthy mysqld. Force the probe to
            # use the same socket the server actually binds.
            settings.processes.bough.readiness_probe.exec.command =
              lib.mkForce "${pkgs.mysql84}/bin/mysqladmin --socket=${socketPath} ping -h localhost";
          };
        };
    };
}
