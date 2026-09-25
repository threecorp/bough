package api

// PickMainPort returns the port whose Role is "main" (or empty,
// treated as "main"). Falls back to the first entry in `ports`, then
// to 0 if `ports` is empty.
//
// Used by single-port engine plugins (mysql / postgres / redis /
// elasticsearch) during the v0.4.x transition so their existing
// single-port Up internals can be wrapped without
// rewriting. Removed alongside the legacy YAML/handshake fallbacks
// in v0.5.0 if no plugin still needs it; until then it doubles as
// the canonical "extract main port" helper for any plugin author
// who wants to ignore multi-port machinery on a single-port engine.
func PickMainPort(ports []PortSpec) int {
	for _, p := range ports {
		if p.Role == "main" || p.Role == "" {
			return p.Port
		}
	}
	if len(ports) > 0 {
		return ports[0].Port
	}
	return 0
}

// PickFirstResourceName returns the Name of the first ResourceSpec
// whose Type matches wantType (or whose Type is empty, treated as a
// match). Returns "" if no resource matched.
//
// Same shim category as PickMainPort: single-resource engines
// (mysql's first initial database, kafka's first topic, etc.) call
// this to keep their internals signature-compatible with the v0.3.x
// `initial_databases []string` pattern.
func PickFirstResourceName(resources []ResourceSpec, wantType string) string {
	for _, r := range resources {
		if r.Type == wantType || r.Type == "" {
			return r.Name
		}
	}
	return ""
}
