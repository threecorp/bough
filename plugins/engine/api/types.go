package api

// PortSpec ties a port number to its semantic role inside an engine.
// Single-port engines (mysql, postgres, redis, elasticsearch) emit
// one entry with Role "main" (or empty, treated identically). Multi-
// port engines emit one entry per role: rabbitmq AMQP + Management,
// kafka broker + controller, NATS client + monitor + cluster, etc.
type PortSpec struct {
	Role string
	Port int
}

// ResourceSpec is one piece of state the engine should provision at
// Up time. Type discriminates which kind of resource ("database",
// "topic", "bucket", "vhost", "kv", "index", ...) — the plugin
// silently drops types it does not implement. Params is engine-
// specific tuning (kafka topic partitions, postgres encoding, etc.).
type ResourceSpec struct {
	Type   string
	Name   string
	Params map[string]string
}

// PortRange is an inclusive [Low, High] window from which the host's
// allocator picks deterministic-per-worktree ports for one role.
type PortRange struct {
	Low  int
	High int
}

// PluginSpec is one plugin the host wants the engine to make available
// before it starts (e.g. an Elasticsearch analyzer). ID and Location
// mirror elasticsearch-plugins.yml's own field names 1:1. Location is
// required for unofficial/third-party plugins and empty for official
// ones the engine's own plugin registry already knows.
type PluginSpec struct {
	ID       string
	Location string
}

// UpReq is the lifecycle-start request payload.
type UpReq struct {
	Ports            []PortSpec
	Datadir          string
	WorktreeRoot     string
	SocketDir        string
	InitialResources []ResourceSpec
	Extras           map[string]string
	Plugins          []PluginSpec
}

// DownReq is the lifecycle-stop request payload. GracefulTimeoutSec is
// the upper bound on graceful shutdown before SIGKILL.
type DownReq struct {
	Ports              []int
	WorktreeRoot       string
	GracefulTimeoutSec int
}

// EnvVarsReq drives `.env.local` rendering. SocketDir lets a plugin
// surface a `XXX_SOCKET=...` entry without the host needing to know
// the plugin's socket-path convention.
type EnvVarsReq struct {
	Ports            []PortSpec
	InitialResources []ResourceSpec
	SocketDir        string
}
