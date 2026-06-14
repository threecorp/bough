# Embedded process-compose wrapper consumed by the
# bough-plugin-elasticsearch binary. Extracted to
# `<worktree>/.local/bough-elasticsearch-flake/` at Up time and invoked
# via `nix run --impure 'path:<extracted>#elasticsearch' -- up --tui=false`.
#
# services-flake does not ship a built-in Elasticsearch module (mysql /
# postgres / redis exist there; ES does not), so this flake drives
# process-compose-flake directly with `pkgs.elasticsearch7` and a custom
# readiness probe against the cluster-health endpoint.
#
# Three env vars steer per-worktree behaviour at flake-eval time:
#
#   BOUGH_ELASTICSEARCH_PORT    — HTTP listen port (default 9200)
#   BOUGH_ELASTICSEARCH_DATADIR — data directory (default
#                                  ./.local/elasticsearch-data)
#   BOUGH_ELASTICSEARCH_HEAP    — JVM heap size (default 1g — small
#                                  enough for laptop multi-worktree but
#                                  large enough to avoid GC thrash in
#                                  index-heavy integration tests)
#
# Linux operators may also need to bump `vm.max_map_count` (kernel
# default of 65530 is below Elasticsearch's required 262144):
#
#   sudo sysctl -w vm.max_map_count=262144
#
# macOS doesn't expose this knob; Elasticsearch ignores it there.
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
              envPort = builtins.getEnv "BOUGH_ELASTICSEARCH_PORT";
            in
            if envPort == "" then 9200 else lib.toInt envPort;

          dataDir =
            let
              d = builtins.getEnv "BOUGH_ELASTICSEARCH_DATADIR";
            in
            if d == "" then "./.local/elasticsearch-data" else d;

          heap =
            let
              h = builtins.getEnv "BOUGH_ELASTICSEARCH_HEAP";
            in
            if h == "" then "1g" else h;
        in
        {
          process-compose."elasticsearch" = {
            imports = [
              inputs.services-flake.processComposeModules.default
            ];
            settings.processes.es = {
              # `transport.port=0` lets Elasticsearch pick any free port
              # for inter-node traffic so siblings on the same machine
              # never collide on 9300. discovery.type=single-node skips
              # the cluster bootstrap wait so a fresh worktree comes up
              # in ~10-20s warm / 30-60s cold.
              command = ''
                mkdir -p ${dataDir}/logs
                ES_JAVA_OPTS="-Xms${heap} -Xmx${heap}" \
                ${pkgs.elasticsearch7}/bin/elasticsearch \
                  -E http.port=${toString dbPort} \
                  -E http.host=127.0.0.1 \
                  -E discovery.type=single-node \
                  -E path.data=${dataDir} \
                  -E path.logs=${dataDir}/logs \
                  -E cluster.name=bough-es \
                  -E node.name=bough-node-${toString dbPort} \
                  -E transport.port=0 \
                  -E xpack.security.enabled=false
              '';
              readiness_probe = {
                exec.command = ''
                  ${pkgs.curl}/bin/curl -sf 'http://127.0.0.1:${toString dbPort}/_cluster/health?wait_for_status=yellow&timeout=5s'
                '';
                initial_delay_seconds = 5;
                period_seconds = 2;
                timeout_seconds = 5;
                failure_threshold = 90;
              };
              shutdown.command = ''
                ${pkgs.curl}/bin/curl -sf -XPOST 'http://127.0.0.1:${toString dbPort}/_nodes/_local/_shutdown' >/dev/null 2>&1 || true
              '';
            };
          };
        };
    };
}
