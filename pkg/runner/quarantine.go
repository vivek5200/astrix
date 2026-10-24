// Package runner tracks test stability and quarantine decisions.
package runner

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

// QuarantineStore stores a bounded rolling history of test outcomes in SQLite.
type QuarantineStore struct {
	db     *sql.DB
	window int
}

// OpenQuarantine opens or creates the SQLite database at path.
func OpenQuarantine(path string, window int) (*QuarantineStore, error) {
	if window < 2 {
		window = 20
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS test_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, test_name TEXT NOT NULL, ast_hash TEXT NOT NULL DEFAULT '', passed INTEGER NOT NULL, ran_at INTEGER NOT NULL)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize quarantine history: %w", err)
	}
	var hasASTHash bool
	columns, err := db.Query(`PRAGMA table_info(test_runs)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	for columns.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue sql.NullString
		if err := columns.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			columns.Close()
			db.Close()
			return nil, err
		}
		if name == "ast_hash" {
			hasASTHash = true
		}
	}
	if err := columns.Close(); err != nil {
		db.Close()
		return nil, err
	}
	if !hasASTHash {
		if _, err = db.Exec(`ALTER TABLE test_runs ADD COLUMN ast_hash TEXT NOT NULL DEFAULT ''`); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate quarantine AST hashes: %w", err)
		}
	}
	if _, err = db.Exec(`CREATE INDEX IF NOT EXISTS test_runs_by_name ON test_runs(test_name, id DESC)`); err != nil {
		db.Close()
		return nil, err
	}
	return &QuarantineStore{db: db, window: window}, nil
}

// Close closes the backing database.
func (q *QuarantineStore) Close() error {
	if q == nil || q.db == nil {
		return nil
	}
	return q.db.Close()
}

// Record appends a test result and trims older history beyond the configured window.
func (q *QuarantineStore) Record(ctx context.Context, testName string, passed bool, at time.Time) error {
	return q.RecordForHash(ctx, testName, "", passed, at)
}

// RecordForHash records a result within a test's exact source/AST identity.
func (q *QuarantineStore) RecordForHash(ctx context.Context, testName, astHash string, passed bool, at time.Time) error {
	if testName == "" {
		return fmt.Errorf("test name cannot be empty")
	}
	value := 0
	if passed {
		value = 1
	}
	if at.IsZero() {
		at = time.Now()
	}
	if _, err := q.db.ExecContext(ctx, `INSERT INTO test_runs(test_name,ast_hash,passed,ran_at) VALUES(?,?,?,?)`, testName, astHash, value, at.UnixNano()); err != nil {
		return err
	}
	_, err := q.db.ExecContext(ctx, `DELETE FROM test_runs WHERE test_name=? AND ast_hash=? AND id NOT IN (SELECT id FROM test_runs WHERE test_name=? AND ast_hash=? ORDER BY id DESC LIMIT ?)`, testName, astHash, testName, astHash, q.window)
	return err
}

// Score returns the rolling transition rate between pass and fail outcomes.
func (q *QuarantineStore) Score(ctx context.Context, testName string) (float64, error) {
	return q.ScoreForHash(ctx, testName, "")
}

// ScoreForHash returns the rolling failure fraction for one exact AST identity.
// A uniformly failing test is not classified as flaky; both pass and fail
// observations must occur before the failure fraction is meaningful.
func (q *QuarantineStore) ScoreForHash(ctx context.Context, testName, astHash string) (float64, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT passed FROM test_runs WHERE test_name=? AND ast_hash=? ORDER BY id DESC LIMIT ?`, testName, astHash, q.window)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	passed, failed := 0, 0
	for rows.Next() {
		var current int
		if err := rows.Scan(&current); err != nil {
			return 0, err
		}
		if current == 1 {
			passed++
		} else {
			failed++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if passed == 0 || failed == 0 {
		return 0, nil
	}
	return float64(failed) / float64(passed+failed), nil
}

// Quarantined reports whether the rolling transition rate exceeds threshold.
func (q *QuarantineStore) Quarantined(ctx context.Context, testName string, threshold float64) (bool, float64, error) {
	score, err := q.Score(ctx, testName)
	return score > threshold, score, err
}

// QuarantinedForHash checks a test's score only for the supplied AST revision.
func (q *QuarantineStore) QuarantinedForHash(ctx context.Context, testName, astHash string, threshold float64) (bool, float64, error) {
	score, err := q.ScoreForHash(ctx, testName, astHash)
	return score > threshold, score, err
}

// QuarantinedTests lists names whose rolling transition rate exceeds threshold.
func (q *QuarantineStore) QuarantinedTests(ctx context.Context, threshold float64) ([]string, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT DISTINCT test_name, ast_hash FROM test_runs ORDER BY test_name, ast_hash`)
	if err != nil {
		return nil, err
	}
	type testRevision struct{ name, hash string }
	var revisions []testRevision
	for rows.Next() {
		var revision testRevision
		if err := rows.Scan(&revision.name, &revision.hash); err != nil {
			rows.Close()
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	quarantined := map[string]bool{}
	for _, revision := range revisions {
		isQuarantined, _, err := q.QuarantinedForHash(ctx, revision.name, revision.hash, threshold)
		if err != nil {
			return nil, err
		}
		if isQuarantined {
			quarantined[revision.name] = true
		}
	}
	quarantinedNames := make([]string, 0, len(quarantined))
	for name := range quarantined {
		quarantinedNames = append(quarantinedNames, name)
	}
	sort.Strings(quarantinedNames)
	return quarantinedNames, nil
}
