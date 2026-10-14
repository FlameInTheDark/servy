package database

// The generated query layer is maintained by hand when sqlc is unavailable
// (the sandbox cannot build it), so these tests execute the statements the
// backup-storage feature depends on and lint the query sources for the traps
// that hand editing introduces.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func newTestQueries(t *testing.T) (*Queries, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "panel.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewQueries(db), path
}

func createTestServer(t *testing.T, q *Queries, serverID string) {
	t.Helper()
	_, err := q.CreateServer(serverID, "now", "now", "srv", "/tmp", "run.sh", "/tmp/log", "run",
		0, 0, 0, "stop", nil, "", 0, 0, "custom", 1, nil, 0, nil, nil, 0, 0, "kill", "std",
		nil, nil, "auto", "native", nil, nil, nil, nil, nil, nil, 0, "bash")
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
}

func TestBackupStorageStatements(t *testing.T) {
	q, _ := newTestQueries(t)
	if _, err := q.CreateBackupStorage("st1", "now", "n", "s3", "p", `{"bucket":"b"}`, 1, 1, "c1"); err != nil {
		t.Fatalf("CreateBackupStorage: %v", err)
	}
	if row, err := q.GetBackupStorage("st1"); err != nil || row.StorageID != "st1" {
		t.Fatalf("GetBackupStorage: %v %v", row, err)
	}
	if row, err := q.GetDefaultBackupStorage(); err != nil || row.StorageID != "st1" {
		t.Fatalf("GetDefaultBackupStorage: %v %v", row, err)
	}
	if rows, err := q.ListBackupStorages(); err != nil || len(rows) != 1 {
		t.Fatalf("ListBackupStorages: %v %d", err, len(rows))
	}
	if err := q.UpdateBackupStorage("n2", "local", "p2", `{"path":"/tmp"}`, 1, 0, "c1", "st1"); err != nil {
		t.Fatalf("UpdateBackupStorage: %v", err)
	}
	if _, err := q.GetDefaultBackupStorage(); err == nil {
		t.Fatal("the default flag was not cleared")
	}
	if err := q.UnsetDefaultBackupStorages(); err != nil {
		t.Fatalf("UnsetDefaultBackupStorages: %v", err)
	}
	if _, err := q.CreateBackupStorage("st2", "now", "n", "local", "", `{"path":"/tmp"}`, 0, 0, "c1"); err != nil {
		t.Fatalf("CreateBackupStorage: %v", err)
	}
	if err := q.DeleteBackupStorage("st2"); err != nil {
		t.Fatalf("DeleteBackupStorage: %v", err)
	}
}

func TestBackupStatements(t *testing.T) {
	q, _ := newTestQueries(t)
	createTestServer(t, q, "srv-1")
	row, err := q.CreateBackup("b1", "now", "nightly", "/tmp/backups", nil, 5, "srv-1", 1, 1, nil, nil, 1, nil, 1, "zip_vault", "st1")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if row.StorageID != "st1" || row.Default != 1 {
		t.Fatalf("row = %+v", row)
	}
	if _, err := q.GetBackup("b1"); err != nil {
		t.Fatalf("GetBackup: %v", err)
	}
	if _, err := q.GetDefaultBackup("srv-1"); err != nil {
		t.Fatalf("GetDefaultBackup: %v", err)
	}
	if err := q.UpdateBackup("nightly", "/tmp/backups", nil, 3, 1, 1, nil, nil, 1, nil, 1, "zip_vault", "st2", "b1"); err != nil {
		t.Fatalf("UpdateBackup: %v", err)
	}
	status := "ok"
	if err := q.SetBackupStatus(&status, "b1"); err != nil {
		t.Fatalf("SetBackupStatus: %v", err)
	}
	if rows, err := q.ListBackupsForServer("srv-1"); err != nil || len(rows) != 1 {
		t.Fatalf("ListBackupsForServer: %v %d", err, len(rows))
	}
	if rows, err := q.ListBackupsForStorage("st2"); err != nil || len(rows) != 1 {
		t.Fatalf("ListBackupsForStorage: %v %d", err, len(rows))
	}
	if err := q.UnsetDefaultBackups("srv-1"); err != nil {
		t.Fatalf("UnsetDefaultBackups: %v", err)
	}
	if err := q.DeleteBackupsForServer("srv-1"); err != nil {
		t.Fatalf("DeleteBackupsForServer: %v", err)
	}
	if err := q.DeleteBackup("b1"); err != nil {
		t.Fatalf("DeleteBackup: %v", err)
	}
}

// TestQuerySourcesQuoteReservedWords guards the mistake that shipped once:
// "default" is a SQLite keyword and the backups table has a column of that
// name, so it must always be written as a quoted identifier — unquoted it dies
// at runtime with `near "default": syntax error`.
func TestQuerySourcesQuoteReservedWords(t *testing.T) {
	dir := filepath.Join("..", "..", "queries")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("query sources not available: %v", err)
	}
	// SQLite rejects these words as bare identifiers in the positions the
	// query layer uses them (measured against sqlite 3.40).
	reserved := regexp.MustCompile(`(?i)(^|[^\w"])default([^\w"]|$)`)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			code := strings.TrimSpace(strings.SplitN(line, "--", 2)[0])
			if code == "" {
				continue
			}
			if !reserved.MatchString(strings.ReplaceAll(code, `"default"`, "DEF")) {
				continue
			}
			t.Errorf("%s:%d uses the keyword default unquoted: %s", entry.Name(), i+1, code)
		}
	}
}

// TestGeneratedStatementArity compares the placeholder count of every
// generated statement with the number of arguments its call site passes. Hand
// editing the generated layer is how the "storage_prefix" column was dropped
// from the query source while its placeholder stayed in the Go, which only
// fails at runtime.
func TestGeneratedStatementArity(t *testing.T) {
	genDir := filepath.Join("..", "repo", "gen")
	files, err := os.ReadDir(genDir)
	if err != nil {
		t.Skipf("generated queries not available: %v", err)
	}
	constRe := regexp.MustCompile("(?s)const (\\w+) = `(.+?)`")
	checked := 0
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(genDir, f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for _, m := range constRe.FindAllStringSubmatchIndex(src, -1) {
			name := src[m[2]:m[3]]
			sql := src[m[4]:m[5]]
			if !strings.HasPrefix(sql, "-- name: ") {
				continue
			}
			placeholders := strings.Count(sql, "?")
			args, ok := callArgs(src[m[1]:], name)
			if !ok {
				t.Errorf("%s: %s: no call found", f.Name(), name)
				continue
			}
			checked++
			if placeholders != args {
				t.Errorf("%s: %s uses %d placeholders but passes %d arguments", f.Name(), name, placeholders, args)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("only %d statements checked, the parser probably stopped matching", checked)
	}
}

// callArgs returns the number of arguments the generated wrapper passes to the
// database for the statement called name.
func callArgs(src, name string) (int, bool) {
	idx := strings.Index(src, "q.db.ExecContext(ctx, "+name)
	if idx < 0 {
		idx = strings.Index(src, "q.db.QueryRowContext(ctx, "+name)
	}
	if idx < 0 {
		idx = strings.Index(src, "q.db.QueryContext(ctx, "+name)
	}
	if idx < 0 {
		return 0, false
	}
	rest := src[idx:]
	open := strings.Index(rest, "(")
	depth := 0
	end := -1
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return 0, false
	}
	parts := splitTopLevel(rest[open+1 : end])
	if len(parts) < 2 {
		return 0, false
	}
	// drop the ctx and the statement name
	return len(parts) - 2, true
}

func splitTopLevel(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(s[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}
