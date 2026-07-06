//go:build darwin || linux

package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// upState is everything Down/Cleanup/EnvVars need to know about a
// service Up() previously brought up, beyond what their own RPC
// request carries. Persisted to a sidecar JSON file at Up time
// because DownReq/Cleanup's signatures have no Extras field — see the
// package doc for why this plugin can't just re-read req.Extras like
// the other four do.
type upState struct {
	File       string `json:"file"`
	Service    string `json:"service"`
	Project    string `json:"project"`
	TargetPort int    `json:"target_port"`
	HostPort   int    `json:"host_port"`
	EnvPrefix  string `json:"env_prefix"`
	ReadyProbe string `json:"ready_probe"`
}

// sidecarStatePath is port-scoped (not just worktree-scoped) so a
// worktree that somehow ends up with more than one compose-backed
// port across retries never has one Up overwrite another's state.
func sidecarStatePath(worktreeRoot string, port int) string {
	return filepath.Join(worktreeRoot, ".local", fmt.Sprintf("bough-compose-%d.json", port))
}

func writeSidecarState(worktreeRoot string, port int, st *upState) error {
	dir := filepath.Join(worktreeRoot, ".local")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(sidecarStatePath(worktreeRoot, port), data, 0o644)
}

// readSidecarState returns an error satisfying os.IsNotExist when no
// Up() ever ran for this (worktreeRoot, port) pair, so callers like
// Down can treat "nothing to tear down" as a distinct, non-fatal case.
func readSidecarState(worktreeRoot string, port int) (*upState, error) {
	data, err := os.ReadFile(sidecarStatePath(worktreeRoot, port))
	if err != nil {
		return nil, err
	}
	var st upState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse sidecar state: %w", err)
	}
	return &st, nil
}

// cacheState/cachedState hold the SAME data as the sidecar file but
// in-process, for EnvVars/ReadyCheck: both are called on the same
// live Provider instance moments after Up() within a single `bough
// create` invocation (host discovers one plugin subprocess per engine
// per command), so a filesystem round-trip isn't needed for them.
func (p *Provider) cacheState(port int, st *upState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cache == nil {
		p.cache = map[int]*upState{}
	}
	p.cache[port] = st
}

func (p *Provider) cachedState(port int) *upState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cache[port]
}
