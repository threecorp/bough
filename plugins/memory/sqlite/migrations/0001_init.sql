-- 0001_init.sql is the v0.5 schema for the bough memory SQLite
-- reference-fallback backend. Round 3 AI #2's metadata escape
-- hatch + round 3 AI #1's dedupe_key / source_event_id columns
-- are present from day one so v0.6+ migrations stay additive.
--
-- The PRAGMAs in the connection setup (see sqlite.go) are what
-- give the round 3 AI #3 concurrency contract its teeth — WAL
-- mode keeps reads from blocking writes, busy_timeout=5000 ms
-- gives writers room to back off without raising "database is
-- locked".

CREATE TABLE IF NOT EXISTS instincts (
  id              TEXT PRIMARY KEY,
  rule            TEXT NOT NULL,
  why             TEXT NOT NULL DEFAULT '',
  how_to_apply    TEXT NOT NULL DEFAULT '',
  domain_csv      TEXT NOT NULL DEFAULT '',
  scope_level     TEXT NOT NULL CHECK(scope_level IN ('worktree','repo','global')),
  scope_id        TEXT NOT NULL DEFAULT '',
  source          TEXT NOT NULL DEFAULT '',
  source_event_id TEXT NOT NULL DEFAULT '',
  dedupe_key      TEXT NOT NULL DEFAULT '',
  state           TEXT NOT NULL DEFAULT 'candidate'
                    CHECK(state IN ('candidate','active','archived','forgotten')),
  confidence      REAL NOT NULL DEFAULT 0.0,
  hits            INTEGER NOT NULL DEFAULT 0,
  last_hit_at     INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  evidence_refs   TEXT NOT NULL DEFAULT '',  -- json array
  source_traces   TEXT NOT NULL DEFAULT '',  -- json array
  metadata        TEXT NOT NULL DEFAULT ''   -- v0.6 escape hatch (round 3 AI #2)
);

CREATE INDEX IF NOT EXISTS idx_instincts_scope ON instincts(scope_level, scope_id);
CREATE INDEX IF NOT EXISTS idx_instincts_state ON instincts(state);
CREATE INDEX IF NOT EXISTS idx_instincts_dedupe
  ON instincts(dedupe_key) WHERE dedupe_key != '';
CREATE INDEX IF NOT EXISTS idx_instincts_source_event
  ON instincts(source_event_id) WHERE source_event_id != '';

-- FTS5 virtual table mirrors rule / why / how_to_apply / domain_csv
-- so the Query RPC can use MATCH for term search instead of LIKE.
-- The contentless contract (rowid='rowid' + content='instincts')
-- keeps the FTS rows in sync via trigger.
CREATE VIRTUAL TABLE IF NOT EXISTS instincts_fts USING fts5(
  rule, why, how_to_apply, domain_csv,
  content='instincts', content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS instincts_ai AFTER INSERT ON instincts BEGIN
  INSERT INTO instincts_fts(rowid, rule, why, how_to_apply, domain_csv)
  VALUES (new.rowid, new.rule, new.why, new.how_to_apply, new.domain_csv);
END;
CREATE TRIGGER IF NOT EXISTS instincts_ad AFTER DELETE ON instincts BEGIN
  INSERT INTO instincts_fts(instincts_fts, rowid, rule, why, how_to_apply, domain_csv)
  VALUES('delete', old.rowid, old.rule, old.why, old.how_to_apply, old.domain_csv);
END;
CREATE TRIGGER IF NOT EXISTS instincts_au AFTER UPDATE ON instincts BEGIN
  INSERT INTO instincts_fts(instincts_fts, rowid, rule, why, how_to_apply, domain_csv)
  VALUES('delete', old.rowid, old.rule, old.why, old.how_to_apply, old.domain_csv);
  INSERT INTO instincts_fts(rowid, rule, why, how_to_apply, domain_csv)
  VALUES (new.rowid, new.rule, new.why, new.how_to_apply, new.domain_csv);
END;
