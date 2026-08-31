package migrations

import (
	"io/fs"
	"strings"
	"testing"
)

func TestSQLContainsRequiredTables(t *testing.T) {
	entries, err := fs.ReadDir(SQL, ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) < 7 {
		t.Fatalf("expected at least 7 migration files, got %d", len(entries))
	}

	var combined strings.Builder
	for _, entry := range entries {
		body, err := SQL.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", entry.Name(), err)
		}
		combined.Write(body)
		combined.WriteByte('\n')
	}
	text := combined.String()
	for _, table := range []string{"users", "auth_tokens", "ai_cost_logs", "practice_sessions", "session_tickets", "utterances", "session_jobs", "phrase_blocks", "daily_reads"} {
		if !strings.Contains(text, "CREATE TABLE IF NOT EXISTS "+table) {
			t.Fatalf("migrations missing table %s", table)
		}
	}
	if !strings.Contains(text, "device_id") || !strings.Contains(text, "is_guest") {
		t.Fatal("users table must include guest identity columns")
	}
}
