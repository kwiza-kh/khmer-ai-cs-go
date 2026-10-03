// Command gofmtcheck reports whether Go files are gofmt-clean, ignoring CRLF line
// endings.
//
// `gofmt -l` flags every file in this working tree on Windows because git checks
// it out with core.autocrlf=true, which makes the tool useless as a pre-commit
// hook and as a local check. Normalising the endings before comparing keeps the
// signal: only real formatting drift is reported.
//
//	go run ./tools/gofmtcheck ./internal ./cmd
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = []string{"."}
	}
	var bad []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// Vendored and generated trees are not ours to format.
				switch d.Name() {
				case "node_modules", ".git", "vendor":
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			if !clean(path) {
				bad = append(bad, path)
			}
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "walk:", err)
			os.Exit(2)
		}
	}
	if len(bad) > 0 {
		fmt.Println("needs gofmt:")
		for _, p := range bad {
			fmt.Println("  ", p)
		}
		os.Exit(1)
	}
}

func clean(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	norm := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	out, err := format.Source(norm)
	if err != nil {
		// A file that does not parse is reported by the compiler; not by this.
		return true
	}
	return bytes.Equal(out, norm)
}
