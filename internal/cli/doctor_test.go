package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestRenderContinuousLearningPosture_LimitsLine(t *testing.T) {
	var buf bytes.Buffer
	renderContinuousLearningPosture(&buf)
	out := buf.String()
	for _, want := range []string{
		"Continuous learning (v0.9):",
		"claude CLI",
		"Anthropic env",
		"Self-DoS caps",
		"calls/session",
		"calls/hour",
		"homunculus root",
		"policy gate",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("posture output missing %q:\n%s", want, out)
		}
	}
}

func TestPolicyGateLine(t *testing.T) {
	if _, msg := policyGateLine(false, 0, 0); !strings.Contains(msg, "OFF") {
		t.Errorf("disabled gate line = %q, want OFF", msg)
	}
	if _, msg := policyGateLine(true, 0, 0); !strings.Contains(msg, "0 held") {
		t.Errorf("enabled/empty gate line = %q, want 0 held", msg)
	}
	_, msg := policyGateLine(true, 3, 2)
	if !strings.Contains(msg, "3 held") || !strings.Contains(msg, "2 batch") {
		t.Errorf("held gate line = %q, want 3 held across 2 batches", msg)
	}
	if !strings.Contains(msg, "reversible") {
		t.Errorf("held gate line must advertise reversibility: %q", msg)
	}
}

func TestCountQuarantined(t *testing.T) {
	dir := t.TempDir()
	// One batch with two held instincts + a REPORT.md (not an instinct).
	batch := dir + "/20260623-120000"
	if err := os.MkdirAll(batch, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.md", "b.md", "REPORT.md"} {
		if err := os.WriteFile(batch+"/"+f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	held, batches := countQuarantined(dir)
	if held != 2 || batches != 1 {
		t.Errorf("countQuarantined = (%d held, %d batches), want (2, 1)", held, batches)
	}
	// A non-existent dir is a clean zero, not an error.
	if h, b := countQuarantined(dir + "/nope"); h != 0 || b != 0 {
		t.Errorf("missing dir = (%d, %d), want (0, 0)", h, b)
	}
}

func TestRenderContinuousLearningPosture_WarnsOnExportedAPIKey(t *testing.T) {
	prev := os.Getenv("ANTHROPIC_API_KEY")
	defer func() {
		if prev == "" {
			_ = os.Unsetenv("ANTHROPIC_API_KEY")
		} else {
			_ = os.Setenv("ANTHROPIC_API_KEY", prev)
		}
	}()
	_ = os.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")

	var buf bytes.Buffer
	renderContinuousLearningPosture(&buf)
	out := buf.String()
	if !strings.Contains(out, "exported API key vars detected") {
		t.Errorf("doctor did not warn on exported ANTHROPIC_API_KEY:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Errorf("doctor did not name the offending variable:\n%s", out)
	}
}

func TestRenderContinuousLearningPosture_CleanEnv(t *testing.T) {
	keys := []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"}
	saved := map[string]string{}
	for _, k := range keys {
		saved[k] = os.Getenv(k)
		_ = os.Unsetenv(k)
	}
	defer func() {
		for k, v := range saved {
			if v == "" {
				continue
			}
			_ = os.Setenv(k, v)
		}
	}()

	var buf bytes.Buffer
	renderContinuousLearningPosture(&buf)
	out := buf.String()
	if !strings.Contains(out, "subscription auth path is clean") {
		t.Errorf("doctor did not say subscription path is clean:\n%s", out)
	}
}
