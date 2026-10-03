/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package common

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestShellEscape(t *testing.T) {
	fixtures := []struct {
		in   interface{}
		want string
	}{
		{"plain", "plain"},
		{"it's", `it'\''s`},
		{"'; reboot; '", `'\''; reboot; '\''`},
		{"$(id) `id`", "$(id) `id`"},
		{[]byte("a'b"), `a'\''b`},
		{42, "42"},
	}
	for _, f := range fixtures {
		if got := ShellEscape(f.in); got != f.want {
			t.Errorf("ShellEscape(%v) = %q, want %q", f.in, got, f.want)
		}
	}
}

// shellVerb is a formatting verb of a command template and the shell context it sits in
type shellVerb struct {
	char     byte
	quoted   bool // inside '...'
	heredoc  bool // inside a heredoc body
	argIndex int
}

func parseShellVerbs(format string) (verbs []shellVerb) {
	inQuote := false
	arg := 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c == '\'' {
			inQuote = !inQuote
			continue
		}
		if c != '%' {
			continue
		}
		if i+1 < len(format) && format[i+1] == '%' {
			i++
			continue
		}
		j := i + 1
		for j < len(format) && strings.IndexByte("+-# 0123456789.", format[j]) >= 0 {
			j++
		}
		if j >= len(format) {
			break
		}
		before := format[:i]
		heredoc := false
		if k := strings.LastIndex(before, "<<"); k >= 0 && strings.Contains(before[k:], "EOF") && !strings.Contains(before[k:], "\nEOF") {
			heredoc = true
		}
		verbs = append(verbs, shellVerb{char: format[j], quoted: inQuote, heredoc: heredoc, argIndex: arg})
		arg++
		i = j
	}
	return
}

// TestShellCommandArgsEscaped guards the commands sent to compute nodes, which run as root through bash -c:
// string values inside '...' must go through ShellEscape, and heredocs must be quoted (<<'EOF') so that
// $(...) in their content is not executed
func TestShellCommandArgsEscaped(t *testing.T) {
	var findings []string
	for _, dir := range []string{"../apis", "../services", "../rpcs", "."} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Sprintf" {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				format, err := strconv.Unquote(lit.Value)
				if err != nil || !(strings.Contains(format, ".sh") || strings.Contains(format, "<<")) {
					return true
				}
				pos := fset.Position(lit.Pos()).String()
				if strings.Contains(format, "<<EOF") {
					findings = append(findings, pos+": unquoted heredoc <<EOF, use <<'EOF'")
				}
				for _, v := range parseShellVerbs(format) {
					if !v.quoted || v.heredoc || (v.char != 's' && v.char != 'v' && v.char != 'q') || v.argIndex+1 >= len(call.Args) {
						continue
					}
					arg := call.Args[v.argIndex+1]
					escaped := false
					if c, ok := arg.(*ast.CallExpr); ok {
						switch fn := c.Fun.(type) {
						case *ast.Ident:
							escaped = fn.Name == "ShellEscape"
						case *ast.SelectorExpr:
							escaped = fn.Sel.Name == "ShellEscape"
						}
					}
					if !escaped {
						findings = append(findings, fmt.Sprintf("%s: argument %d is quoted in the command but not wrapped with ShellEscape", pos, v.argIndex+1))
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(findings) > 0 {
		t.Fatalf("unsafe shell command templates:\n%s", strings.Join(findings, "\n"))
	}
}
