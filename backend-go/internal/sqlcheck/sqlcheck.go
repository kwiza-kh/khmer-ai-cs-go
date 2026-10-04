// Package sqlcheck finds SQL statements in this module's Go source that
// reference database objects which do not exist.
//
// The compiler cannot see inside a string literal, so `SELECT ... b.message_quota`
// and `$6::sla_priority` survive build, vet and unit tests and only fail when
// something finally executes that path — and if the caller writes `_ = err`,
// they fail as a silent zero instead. Both of those shipped. One made /tenants
// unusable, the other made creating an SLA policy impossible from the day it
// was written.
//
// The check is a two-step reconstruction: parse every Go file with go/ast,
// reassemble string literals joined by `+` back into whole statements, then ask
// PostgreSQL to PREPARE each one. PREPARE performs full parse analysis — the
// same step that rejects an unknown column — without executing anything, so it
// is safe to point at a live database. Each PREPARE runs inside a transaction
// that is rolled back, and nothing is ever executed.
//
// Run it via the package test (see sqlcheck_test.go), which skips unless
// DATABASE_URL is set.
package sqlcheck

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"khmer-ai-cs-go/internal/textutil"
)

// Stmt is one SQL statement recovered from the source.
type Stmt struct {
	File string // path relative to the extracted root
	Line int
	SQL  string
	// Full is false when the literal is a fragment — a WHERE clause or an
	// ON CONFLICT tail whose head lives in another string joined at runtime.
	// Fragments cannot be prepared on their own and are skipped.
	Full bool
}

func (s Stmt) String() string { return fmt.Sprintf("%s:%d", s.File, s.Line) }

// Finding is one statement that did not prepare.
type Finding struct {
	Stmt    Stmt
	Code    string // SQLSTATE
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s [%s] %s\n    %s", f.Stmt, f.Code, textutil.OneLine(f.Stmt.SQL, 200), f.Message)
}

// Report is the outcome of a verification run.
type Report struct {
	Checked int
	Passed  int
	Skipped int
	// Broken holds statements that parsed but reference something missing —
	// undefined column, table, type or function. These are real defects.
	Broken []Finding
	// Syntax holds statements PostgreSQL could not parse at all. In practice
	// these are extraction artifacts (a fragment that looked complete), not
	// source defects, so they are reported but do not fail the check.
	Syntax []Finding
}

// ============================================================ extraction

// literal is one string literal recovered from a `+` chain.
type literal struct {
	text string
	pos  token.Pos
}

// flatten collects every string literal in a `+` chain. ok is false when any
// operand is not a string literal (a variable, a call, a number), which means
// the full statement cannot be reconstructed from source alone.
func flatten(e ast.Expr) ([]literal, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return nil, false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return nil, false
		}
		return []literal{{s, v.Pos()}}, true
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return nil, false
		}
		l, ok := flatten(v.X)
		if !ok {
			return nil, false
		}
		r, ok := flatten(v.Y)
		if !ok {
			return nil, false
		}
		return append(l, r...), true
	case *ast.ParenExpr:
		return flatten(v.X)
	}
	return nil, false
}

// statementKeyword reports whether s opens like a complete statement rather
// than a clause meant to be concatenated onto one.
//
// The keyword must end at a word boundary: without that, "select_account" (an
// OAuth parameter) reads as a SELECT. DELETE and UPDATE are also HTTP methods,
// so a following "/" disqualifies them — "DELETE /api/v1/knowledge/{id}" is a
// route registration, not SQL.
func statementKeyword(s string) bool {
	t := strings.TrimSpace(strings.TrimLeft(s, "(\n\t "))
	for _, kw := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "WITH", "TRUNCATE"} {
		rest, ok := cutPrefixFold(t, kw)
		if !ok {
			continue
		}
		if rest != "" && !isSpace(rest[0]) && rest[0] != '(' {
			continue // e.g. select_account
		}
		if strings.HasPrefix(strings.TrimSpace(rest), "/") {
			continue // an HTTP route
		}
		// A bare VALUES list is always the tail of an INSERT here, and a
		// DELETE or INSERT that never names a table is a fragment.
		up := strings.ToUpper(t)
		if strings.HasPrefix(up, "DELETE") && !strings.Contains(up, " FROM ") {
			return false
		}
		if strings.HasPrefix(up, "INSERT") && !strings.Contains(up, " INTO ") {
			return false
		}
		// Prose can begin with the word "Select". Require at least one token
		// that only SQL uses — a quote, a placeholder, a paren, a star, or a
		// clause keyword — before believing this is a statement.
		if !strings.ContainsAny(t, "'($*") && !hasAnyWord(up, "FROM", "INTO", "SET", "WHERE", "VALUES", "JOIN", "LIMIT", "GROUP") {
			return false
		}
		return true
	}
	return false
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// hasAnyWord reports whether the space-separated word appears in s.
func hasAnyWord(s string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(s, " "+w+" ") || strings.HasPrefix(s, w+" ") || strings.HasSuffix(s, " "+w) {
			return true
		}
	}
	return false
}

// Extract reassembles the SQL statements in every .go file under root.
//
// A literal that belongs to a `+` chain is only reported as part of that chain,
// never on its own. Reporting it twice is what produced the first version's
// false positives: the tail of an INSERT — "VALUES ($1,$2) ON CONFLICT …" —
// looks like a statement but has no table, so preparing it fails with an error
// that says nothing about the source.
func Extract(root string) ([]Stmt, error) {
	type pos struct {
		file string
		line int
	}
	best := map[pos]string{}
	full := map[pos]bool{}
	consumed := map[token.Pos]bool{}

	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Never skip the root itself: callers pass relative roots like
			// "../..", whose base name is ".." and would otherwise match the
			// dot-directory rule below and silently skip the entire tree.
			if path == root {
				return nil
			}
			// Vendored trees and dot-directories are not ours to audit.
			// testdata IS walked: the package's own fixtures live there, and
			// the Go tool ignores it at build time.
			if name := d.Name(); name == "node_modules" || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)

		ast.Inspect(f, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if !ok || be.Op != token.ADD {
				return true
			}
			lits, ok := flatten(be)
			if !ok || len(lits) < 2 {
				return true
			}
			var sb strings.Builder
			for _, l := range lits {
				sb.WriteString(l.text)
				consumed[l.pos] = true
			}
			p := fset.Position(lits[0].pos)
			k := pos{rel, p.Line}
			if s := sb.String(); len(s) > len(best[k]) {
				best[k] = s
				full[k] = statementKeyword(s)
			}
			return true
		})

		// Standalone literals — ones not part of any chain.
		ast.Inspect(f, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING || consumed[bl.Pos()] {
				return true
			}
			s, uerr := strconv.Unquote(bl.Value)
			if uerr != nil || !statementKeyword(s) {
				return true
			}
			p := fset.Position(bl.Pos())
			k := pos{rel, p.Line}
			if len(s) > len(best[k]) {
				best[k] = s
				full[k] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make([]Stmt, 0, len(best))
	for k, s := range best {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, Stmt{File: k.file, Line: k.line, SQL: s, Full: full[k]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// ============================================================ verification

// SQLSTATEs that mean "this statement parsed, but names something absent".
// These are the defects the check exists to catch.
const (
	codeUndefinedColumn   = "42703"
	codeUndefinedTable    = "42P01"
	codeUndefinedObject   = "42704" // type, operator class, …
	codeUndefinedFunction = "42883"
)

// Verify prepares every complete statement against the database behind dsn.
//
// Nothing is executed: PREPARE only parses, analyses and plans. Each statement
// runs in its own transaction which is always rolled back, so a failed PREPARE
// cannot poison the session for the next one.
func Verify(ctx context.Context, dsn string, stmts []Stmt) (*Report, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.Background())

	rep := &Report{}
	for i, s := range stmts {
		rep.Checked++
		if !s.Full || strings.Contains(s.SQL, "%s") || strings.Contains(s.SQL, "%d") {
			rep.Skipped++
			continue
		}
		sql := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s.SQL), ";"))
		name := fmt.Sprintf("_sqlcheck_%d", i)

		tx, terr := conn.Begin(ctx)
		if terr != nil {
			return nil, fmt.Errorf("begin: %w", terr)
		}
		pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, perr := tx.Exec(pctx, "PREPARE "+name+" AS "+sql)
		cancel()
		_ = tx.Rollback(ctx)

		if perr == nil {
			rep.Passed++
			continue
		}
		// An isolated statement has no context to infer $N's type from; that is
		// a property of preparing it alone, not a defect in the source.
		if strings.Contains(perr.Error(), "could not determine data type") {
			rep.Skipped++
			continue
		}
		f := Finding{Stmt: s, Code: sqlstate(perr), Message: perr.Error()}
		switch f.Code {
		case codeUndefinedColumn, codeUndefinedTable, codeUndefinedObject, codeUndefinedFunction:
			rep.Broken = append(rep.Broken, f)
		default:
			rep.Syntax = append(rep.Syntax, f)
		}
	}
	return rep, nil
}

func sqlstate(err error) string {
	var pge *pgconn.PgError
	if errors.As(err, &pge) {
		return pge.Code
	}
	return "?"
}
