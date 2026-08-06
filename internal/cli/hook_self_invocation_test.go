package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ikeikeikeike/bough/internal/observe"
)

// TestHookHandle_SelfInvocationRecordsNothing pins the fix for a
// self-poisoning loop: bough's own `claude --print` subprocesses (the
// observer mint, the gate judge) run with hooks installed, so their
// events were captured as observations — and the judge prompt's own
// vocabulary ("a wrong \"true\" quarantines a useful instinct") read as
// the OPERATOR saying "wrong". Measured 2026-08-07: 106 of 107
// correction-flagged sessions owed their flag to bough's own prompt
// text. A session carrying the self-invocation marker must record
// nothing and still exit 0, so the subprocess's tool calls work.
func TestHookHandle_SelfInvocationRecordsNothing(t *testing.T) {
	t.Setenv(observe.SelfInvocationEnv, "1")
	obs := filepath.Join(t.TempDir(), "obs.jsonl")

	cmd := newHookHandleCmd()
	cmd.SetArgs([]string{"--event", "UserPromptSubmit", "--out", obs})
	cmd.SetIn(strings.NewReader(`{"prompt":"judge this instinct; a wrong true quarantines a useful one","session_id":"self"}`))
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("self-invoked hook must exit 0, got: %v\nstderr:\n%s", err, errBuf.String())
	}
	if _, err := os.Stat(obs); !os.IsNotExist(err) {
		data, _ := os.ReadFile(obs)
		t.Errorf("self-invoked session must record nothing, but wrote:\n%s", data)
	}
}

// TestHookHandle_OperatorSessionStillRecords is the other direction —
// without the marker the capture path must keep working, or the fix for
// self-poisoning would silently kill the whole loop.
func TestHookHandle_OperatorSessionStillRecords(t *testing.T) {
	// The var may leak in from the developer's own environment if a bough
	// subprocess ever runs the tests; force the operator state.
	t.Setenv(observe.SelfInvocationEnv, "")
	obs := filepath.Join(t.TempDir(), "obs.jsonl")

	cmd := newHookHandleCmd()
	cmd.SetArgs([]string{"--event", "PreToolUse", "--out", obs})
	cmd.SetIn(strings.NewReader(`{"tool_name":"Bash","session_id":"op"}`))
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("hook handle: %v\nstderr:\n%s", err, errBuf.String())
	}
	data, err := os.ReadFile(obs)
	if err != nil || !bytes.Contains(data, []byte(`"PreToolUse"`)) {
		t.Errorf("operator session must still be recorded (err=%v):\n%s", err, data)
	}
}
