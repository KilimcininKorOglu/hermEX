package migrate

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// LoadFS reads the numbered .sql migration files in dir of fsys (typically an
// embed.FS) into an ordered migration set. Each file is named NNNN_description.sql
// where the leading digits are the schema version; its statements are separated by
// semicolons. The result is sorted ascending by version; Run validates that the
// versions are unique and strictly ascending.
//
// Statement splitting is line-comment aware (-- ... lines are dropped) but
// otherwise naive: a statement must not contain a semicolon inside a string
// literal. The schema DDL has none, which keeps the loader small and dependency
// free.
func LoadFS(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: read %s: %w", dir, err)
	}
	var migs []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		ver, err := parseVersion(e.Name())
		if err != nil {
			return nil, fmt.Errorf("migrate: %s: %w", e.Name(), err)
		}
		content, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", e.Name(), err)
		}
		steps := splitStatements(string(content))
		if len(steps) == 0 {
			return nil, fmt.Errorf("migrate: %s: no statements", e.Name())
		}
		migs = append(migs, Migration{Version: ver, Steps: steps})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].Version < migs[j].Version })
	return migs, nil
}

// MustLoadFS is LoadFS for embedded migration sets known at build time: it
// panics on error, since a malformed or missing embedded file is a programmer
// error that should fail at package initialization rather than at runtime.
func MustLoadFS(fsys fs.FS, dir string) []Migration {
	migs, err := LoadFS(fsys, dir)
	if err != nil {
		panic(err)
	}
	return migs
}

// parseVersion reads the leading run of digits in a migration filename as its
// version, so 0025_baseline.sql is version 25.
func parseVersion(name string) (int, error) {
	base := strings.TrimSuffix(name, ".sql")
	i := 0
	for i < len(base) && base[i] >= '0' && base[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("filename must start with a version number")
	}
	return strconv.Atoi(base[:i])
}

// splitStatements breaks a .sql file into individual statements on semicolons.
// Whole-line -- comments and blank lines are stripped first, so a semicolon
// inside a comment never splits a statement; comments must therefore occupy their
// own lines (the documented schema files do).
func splitStatements(content string) []string {
	var sql strings.Builder
	for line := range strings.SplitSeq(content, "\n") {
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		sql.WriteString(line)
		sql.WriteByte('\n')
	}
	var out []string
	for chunk := range strings.SplitSeq(sql.String(), ";") {
		if stmt := strings.TrimSpace(chunk); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}
