package memory

import (
	"fmt"
	"sync"
	"testing"
	"time"

	memapi "github.com/ikeikeikeike/bough/plugins/memory/api"
)

// runConcurrency asserts that parallel Store / Query calls do not
// produce "database is locked" / "transaction aborted" errors.
// Round 3 AI #3 made this conformance requirement explicit: the
// SQLite reference-fallback's WAL + busy_timeout settings are
// validated here, and any v0.6+ external backend should also pass.
func runConcurrency(t *testing.T, b memapi.MemoryBackend, cfg Config) {
	t.Helper()
	scope := memapi.Scope{Level: "worktree", WorktreeID: "concurrency", RepoName: "memory-conf"}

	const parallelStores = 25
	const parallelQueriesPerStore = 3

	var wg sync.WaitGroup
	errs := make(chan error, parallelStores*(1+parallelQueriesPerStore))

	for i := 0; i < parallelStores; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := cfg.ctx(t)
			defer cancel()
			rule := fmt.Sprintf("concurrency rule %02d", i)
			_, err := b.Store(ctx, &memapi.StoreReq{
				Instinct: memapi.Instinct{
					ID:         fmt.Sprintf("conc-%02d", i),
					Rule:       rule,
					Scope:      scope,
					Source:     "test_failure",
					Confidence: 0.6,
					State:      "active",
					CreatedAt:  time.Now().UTC(),
				},
				DedupeKey:     fmt.Sprintf("dk-conc-%02d", i),
				SourceEventID: fmt.Sprintf("evt-conc-%02d", i),
			})
			if err != nil {
				errs <- fmt.Errorf("store#%d: %w", i, err)
			}
		}(i)

		for j := 0; j < parallelQueriesPerStore; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ctx, cancel := cfg.ctx(t)
				defer cancel()
				_, err := b.Query(ctx, &memapi.QueryReq{
					Term:       "concurrency",
					Scope:      scope,
					MaxResults: 50,
					MaxTokens:  100000,
				})
				if err != nil {
					errs <- fmt.Errorf("query: %w", err)
				}
			}()
		}
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent op failed: %v", err)
	}
}
