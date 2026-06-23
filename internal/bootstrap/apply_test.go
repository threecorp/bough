package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ikeikeikeike/bough/internal/evolve"
	"github.com/ikeikeikeike/bough/pkg/schema"
	api "github.com/ikeikeikeike/bough/plugins/capability/api"
)

func mkResult(verdict api.VerdictKind, label, body string) evolve.Result {
	clusterID := "abcdef01"
	return evolve.Result{
		Candidates: []schema.InstinctCandidate{
			{
				ID:         "cand_" + clusterID + "00",
				Rule:       body,
				HowToApply: label,
				Scope:      schema.Scope{Level: schema.ScopeRepo, RepoName: "bough"},
				State:      schema.InstinctStateCandidate,
				CreatedAt:  time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC),
			},
		},
		PerClusterAudit: []evolve.ClusterAudit{
			{
				ClusterID: clusterID,
				Size:      3,
				Verdict: api.JudgeVerdict{
					Verdict:    verdict,
					Confidence: 0.8,
					Reason:     "test verdict",
				},
			},
		},
	}
}

func TestApply_PASS_writesSkillFile(t *testing.T) {
	root := t.TempDir()
	res := mkResult(api.VerdictPass, "io-lives-in-data-layer", "I/O lives in data layer")
	r, err := Apply(context.Background(), res, ApplyOptions{
		MonorepoRoot: root,
		Now:          func() time.Time { return time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.Promoted != 1 {
		t.Errorf("Promoted = %d, want 1", r.Promoted)
	}
	if len(r.WrittenFiles) != 1 {
		t.Errorf("WrittenFiles = %d, want 1", len(r.WrittenFiles))
	}
	body, err := os.ReadFile(filepath.Join(root, ".claude", "skills", "io-lives-in-data-layer.md"))
	if err != nil {
		t.Fatalf("read skill file: %v", err)
	}
	if !strings.Contains(string(body), "verdict: PASS") {
		t.Errorf("rendered body missing verdict line:\n%s", body)
	}
}

func TestApply_DOUBT_skippedWithoutForce(t *testing.T) {
	root := t.TempDir()
	res := mkResult(api.VerdictDoubt, "uncertain-rule", "Some uncertain instinct")
	r, err := Apply(context.Background(), res, ApplyOptions{
		MonorepoRoot: root,
		Now:          func() time.Time { return time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.Promoted != 0 {
		t.Errorf("Promoted = %d, want 0", r.Promoted)
	}
	if r.Demoted != 1 {
		t.Errorf("Demoted = %d, want 1", r.Demoted)
	}
	if len(r.Skipped) != 1 {
		t.Errorf("Skipped = %d, want 1", len(r.Skipped))
	}
}

func TestApply_DOUBT_promotedWithForce(t *testing.T) {
	root := t.TempDir()
	res := mkResult(api.VerdictDoubt, "uncertain-rule", "Some uncertain instinct")
	r, err := Apply(context.Background(), res, ApplyOptions{
		MonorepoRoot: root,
		Force:        true,
		Now:          func() time.Time { return time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.Promoted != 1 {
		t.Errorf("Promoted = %d, want 1 with --force", r.Promoted)
	}
}

func TestApply_FAIL_alwaysSkipped(t *testing.T) {
	root := t.TempDir()
	res := mkResult(api.VerdictFail, "bad-rule", "Bad instinct")
	r, err := Apply(context.Background(), res, ApplyOptions{
		MonorepoRoot: root,
		Force:        true, // even with force, FAIL stays skipped
		Now:          func() time.Time { return time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.Promoted != 0 {
		t.Errorf("FAIL should never promote, got Promoted=%d", r.Promoted)
	}
}

func TestApply_EmptyRoot_errors(t *testing.T) {
	res := mkResult(api.VerdictPass, "x", "y")
	_, err := Apply(context.Background(), res, ApplyOptions{})
	if err == nil {
		t.Errorf("expected error for empty MonorepoRoot")
	}
}

func TestApply_atomicWrite_doesNotLeaveTmp(t *testing.T) {
	root := t.TempDir()
	res := mkResult(api.VerdictPass, "atomic-rule", "atomic write check")
	_, err := Apply(context.Background(), res, ApplyOptions{
		MonorepoRoot: root,
		Now:          func() time.Time { return time.Date(2026, 6, 23, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".claude", "skills"))
	if err != nil {
		t.Fatalf("read skills dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover .tmp file: %s", e.Name())
		}
	}
}
