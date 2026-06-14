package api

import "context"

// DBProvider is the Go-side surface the bough host calls and that
// plugin authors implement. Every error is wrapped at the gRPC
// boundary into a string in the wire response; the Go interface
// rematerialises plain errors so the host can `errors.Is` / wrap as
// usual.
//
// Lifecycle in the typical create-then-remove flow:
//
//	PortRangeDefault — host once per kind to size the allocation range
//	Up               — host launches mysqld / postgres / ... on `Port`
//	ReadyCheck       — host polls until the service accepts conns
//	EnvVars          — host renders .env.local snippets
//	Down             — host gracefully stops the instance
//	Cleanup          — host wipes the datadir after Down confirmed exit
type DBProvider interface {
	Up(ctx context.Context, req UpReq) error
	Down(ctx context.Context, req DownReq) error
	ReadyCheck(ctx context.Context, port, timeoutSec int) (bool, error)
	Cleanup(ctx context.Context, datadir string, port int) error
	PortRangeDefault(ctx context.Context) (low, high int, err error)
	EnvVars(ctx context.Context, req EnvVarsReq) (map[string]string, error)
}

// UpReq is the lifecycle-start request payload. `Extras` carries plugin-
// specific knobs (e.g. mysql `character_set_server`) lifted verbatim
// from the YAML `databases[].extras` map.
type UpReq struct {
	Port             int
	Datadir          string
	WorktreeRoot     string
	SocketDir        string
	InitialDatabases []string
	Extras           map[string]string
}

// DownReq is the lifecycle-stop request payload. GracefulTimeoutSec is
// upper-bound for the plugin's graceful path before it must SIGKILL.
type DownReq struct {
	Port               int
	WorktreeRoot       string
	GracefulTimeoutSec int
}

// EnvVarsReq drives `.env.local` rendering. SocketDir lets a plugin
// surface a `XXX_SOCKET=...` entry without the host needing to know
// the plugin's socket-path convention.
type EnvVarsReq struct {
	Port             int
	InitialDatabases []string
	SocketDir        string
}
