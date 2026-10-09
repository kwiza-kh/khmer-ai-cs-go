package tenantscope

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"khmer-ai-cs-go/internal/sqlcheck"
)

// Analyze walks root (the module directory), extracts every complete SQL
// statement and classifies it against the rules in the package comment.
func Analyze(root string) (*Report, error) {
	stmts, err := sqlcheck.Extract(root)
	if err != nil {
		return nil, err
	}
	rep := &Report{}
	used := map[string]bool{}
	files := map[string]*fileInfo{}

	for _, s := range stmts {
		rep.Statements++
		if !s.Full {
			continue
		}
		tables := referencedTables(s.SQL)
		tenant := touchedTenantTables(tables)
		if len(tenant) == 0 {
			continue
		}
		rep.Tenant++

		if boundInStatement(s.SQL, tenant) || insertNamesTenant(s.SQL) {
			rep.Bound++
			continue
		}
		if crossTenantFile(s.File) != "" {
			// Whole-file exemption: cross-tenant by design. Not counted as a
			// gap, and not eligible for the exception list.
			rep.Excepted++
			continue
		}

		fi, err := loadFile(root, s.File, files)
		if err != nil {
			return nil, err
		}
		fn := fi.enclosingFunc(s.Line)
		if fi.hasMarker(s.Line) {
			rep.Marked++
			continue
		}
		if fi.hasFunnelBefore(s.Line) {
			rep.Funnelled++
			continue
		}
		key := exceptionKey(s.File, fn)
		if _, ok := exceptions[key]; ok {
			used[key] = true
			rep.Excepted++
			continue
		}
		rep.Gaps = append(rep.Gaps, Gap{
			File: s.File, Line: s.Line, Func: fn, Tables: tenant, SQL: s.SQL,
		})
	}

	for key := range exceptions {
		if !used[key] {
			rep.Stale = append(rep.Stale, key+": "+exceptions[key])
		}
	}
	sort.Strings(rep.Stale)
	return rep, nil
}

// touchedTenantTables returns the tenant tables a statement references and
// whose binding column it does not name.
func touchedTenantTables(tables []string) []string {
	var out []string
	for _, t := range tables {
		if _, ok := tenantKeyed[t]; ok {
			out = append(out, t)
			continue
		}
		if _, ok := tenantDerived[t]; ok {
			out = append(out, t)
		}
	}
	return out
}

// boundInStatement reports whether the SQL names the binding column of every
// tenant table it touches.
//
// The derived tables are the interesting case: chat_messages has no tenant
// column, so naming session_id satisfies the *join* half — the funnel check
// (or an exception) must then supply the tenant half. That split is the whole
// reason chat_messages and knowledge_chunks needed the 061 backstop.
func boundInStatement(sql string, tenant []string) bool {
	for _, t := range tenant {
		if col, ok := tenantKeyed[t]; ok {
			if !hasWord(sql, col) {
				return false
			}
			continue
		}
		if col, ok := tenantDerived[t]; ok {
			if !hasWord(sql, col) {
				return false
			}
			// A derived table alone is not a binding: require a tenant column
			// somewhere in the statement, which means the join's parent table
			// is scoped.
			scoped := false
			for _, c := range tenantColumns {
				if hasWord(sql, c) {
					scoped = true
					break
				}
			}
			if !scoped {
				return false
			}
		}
	}
	return true
}

// fileInfo caches one parsed Go file: source lines, function ranges, funnel
// call positions and marker comments.
type fileInfo struct {
	lines   []string
	funcs   []funcRange
	funnels map[string]bool // function name -> calls a funnel somewhere
	marked  map[string]bool // function name -> carries a tenantscope:ok marker
}

type funcRange struct {
	name       string
	start, end int
}

// loadFile reads and parses one Go file. rel is produced by walking root in
// sqlcheck.Extract (never by a request), so the join cannot escape the module;
// the containment check below keeps that true for any other caller too.
func loadFile(root, rel string, cache map[string]*fileInfo) (*fileInfo, error) {
	if fi, ok := cache[rel]; ok {
		return fi, nil
	}
	path := filepath.Join(root, rel)
	if !strings.HasPrefix(path, filepath.Clean(root)+string(os.PathSeparator)) {
		return nil, fmt.Errorf("tenantscope: %q escapes the module root", rel)
	}
	data, err := os.ReadFile(path) // pi-lens-ignore: go-path-traversal -- path checked above, rel comes from a walk of root
	if err != nil {
		return nil, err
	}
	src := string(data)
	fi := &fileInfo{
		lines:   strings.Split(src, "\n"),
		funnels: map[string]bool{},
		marked:  map[string]bool{},
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		// Unparsable Go is sqlcheck's problem, not this gate's; treat the file
		// as having no information rather than failing the isolation check.
		cache[rel] = fi
		return fi, nil
	}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		start := fset.Position(fd.Pos()).Line
		end := fset.Position(fd.End()).Line
		name := fd.Name.Name
		fi.funcs = append(fi.funcs, funcRange{name: name, start: start, end: end})
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee := callName(call)
			for _, fn := range funnels {
				if callee == fn {
					fi.funnels[name] = true
					return false
				}
			}
			return true
		})
	}
	// Markers: a line carrying tenantscope:ok covers the function it documents
	// (the review decision belongs to the code, not to a central list).
	for i, line := range fi.lines {
		if !strings.Contains(line, "tenantscope:ok") {
			continue
		}
		if name, ok := fi.funcAt(i + 1); ok {
			fi.marked[name] = true
		}
	}
	// Comments in Go may sit immediately above the statement; the enclosing
	// func check handles the rest. Attach the marker to the whole function by
	// recording the function's range too.
	cache[rel] = fi
	return fi, nil
}

func callName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// enclosing returns the innermost (last-starting) function containing line.
func (fi *fileInfo) enclosing(line int) (funcRange, bool) {
	best := funcRange{}
	found := false
	for _, fr := range fi.funcs {
		if fr.start <= line && line <= fr.end && (!found || fr.start > best.start) {
			best, found = fr, true
		}
	}
	return best, found
}

func (fi *fileInfo) enclosingFunc(line int) string {
	if fr, ok := fi.enclosing(line); ok {
		return fr.name
	}
	return "?"
}

// hasFunnelBefore reports whether the enclosing function calls a reviewed
// funnel (the funnel calls are collected per function; a funnel anywhere in
// the function authorises its later statements).
func (fi *fileInfo) hasFunnelBefore(line int) bool {
	fr, ok := fi.enclosing(line)
	return ok && fi.funnels[fr.name]
}

// hasMarker reports whether the statement's enclosing function carries a
// tenantscope:ok marker.
func (fi *fileInfo) hasMarker(line int) bool {
	fr, ok := fi.enclosing(line)
	return ok && fi.marked[fr.name]
}

// funcAt returns the function owning a line, or — for a marker written as a
// doc comment — the next function to start at or after it.
func (fi *fileInfo) funcAt(line int) (string, bool) {
	if fr, ok := fi.enclosing(line); ok {
		return fr.name, true
	}
	best := ""
	bestStart := 0
	for _, fr := range fi.funcs {
		if fr.start >= line && (best == "" || fr.start < bestStart) {
			best, bestStart = fr.name, fr.start
		}
	}
	return best, best != ""
}
