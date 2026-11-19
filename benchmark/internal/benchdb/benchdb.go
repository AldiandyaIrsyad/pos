// Package benchdb gives a benchmark a throwaway copy of pos with a
// migration's indexes either dropped or applied, so a before/after comparison
// never depends on what state pos happens to be in, and pos is never written.
package benchdb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Name is the throwaway database. pos is its template, so pos must have no
// other open connections while a copy is made.
const Name = "pos_bench"

// RepoDir is the repository root, so tools find migrations/ and write into
// benchmark/ regardless of the working directory.
func RepoDir() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// Fresh replaces Name with a new copy of pos. Call Drop when done.
func Fresh(ctx context.Context, admin *pgx.Conn) error {
	if err := Drop(ctx, admin); err != nil {
		return err
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+Name+" TEMPLATE pos"); err != nil {
		return fmt.Errorf("copy pos (close the API and other sessions on pos): %w", err)
	}
	return nil
}

func Drop(ctx context.Context, admin *pgx.Conn) error {
	_, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+Name+" WITH (FORCE)")
	return err
}

// Migration is a migration file split into statements, with the indexes it creates.
type Migration struct {
	File       string // base name, e.g. 004_indexes2.sql
	Statements []string
	Indexes    []string
}

var createIndexRe = regexp.MustCompile(`(?i)^CREATE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)`)

// LoadMigration reads a migration, dropping comments and psql meta-commands
// such as \c. Statements are kept apart because VACUUM can't run inside the
// implicit transaction of a multi-statement Exec.
func LoadMigration(path string) (Migration, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Migration{}, err
	}
	var b strings.Builder
	for line := range strings.Lines(string(raw)) {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "--") || strings.HasPrefix(t, `\`) {
			continue
		}
		b.WriteString(line)
	}
	m := Migration{File: filepath.Base(path)}
	for stmt := range strings.SplitSeq(b.String(), ";") {
		if stmt = strings.TrimSpace(stmt); stmt == "" {
			continue
		}
		m.Statements = append(m.Statements, stmt)
		if match := createIndexRe.FindStringSubmatch(stmt); match != nil {
			m.Indexes = append(m.Indexes, match[1])
		}
	}
	if len(m.Indexes) == 0 {
		return Migration{}, fmt.Errorf("%s creates no indexes", path)
	}
	return m, nil
}

// Prepare drops the migration's indexes, then runs the migration with
// (apply) or without its CREATE INDEX statements. Its other statements
// (VACUUM ANALYZE) run either way, so the indexes are the only difference.
func (m Migration) Prepare(ctx context.Context, conn *pgx.Conn, apply bool) error {
	for _, idx := range m.Indexes {
		if _, err := conn.Exec(ctx, "DROP INDEX IF EXISTS "+idx); err != nil {
			return err
		}
	}
	for _, stmt := range m.Statements {
		if !apply && createIndexRe.MatchString(stmt) {
			continue
		}
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}
