/*
Copyright <holder> All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package dbs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sqlPlaceholderPattern matches format strings that quote a %s/%v value inside a SQL condition,
// e.g. "name like '%%%s%%'" or "type = '%s'": values have to be bound with ? instead
var sqlPlaceholderPattern = regexp.MustCompile(`((^|[\s(])[a-z_][a-z0-9_.]*\s*(=|!=|<>)\s*|\b(?i:like)\s+|\b(?i:in)\s*\(\s*)'(%%)?%[sv]`)

// TestNoQuotedSQLPlaceholders guards against building SQL conditions from formatted strings
func TestNoQuotedSQLPlaceholders(t *testing.T) {
	var findings []string
	for _, dir := range []string{"../apis", "../services", "../rpcs", "../common", "."} {
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
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err == nil && sqlPlaceholderPattern.MatchString(value) {
					findings = append(findings, fset.Position(lit.Pos()).String()+": "+value)
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
		t.Fatalf("SQL conditions must bind values with ? instead of quoting formatted values:\n%s", strings.Join(findings, "\n"))
	}
}

func TestSQLPlaceholderPattern(t *testing.T) {
	for _, s := range []string{"name like '%%%s%%'", "type = '%s'", "a and source!='%s'", "(pool_id='%v')", "x in ('%s')"} {
		if !sqlPlaceholderPattern.MatchString(s) {
			t.Errorf("pattern should match %q", s)
		}
	}
	for _, s := range []string{"name like ?", "volume_id = %d", "echo '%s'", "CLAND_PUBKEY='%s' ", `vol{mode='read',volName='%s'}`, "/opt/x.sh '%d' '%s'"} {
		if sqlPlaceholderPattern.MatchString(s) {
			t.Errorf("pattern should not match %q", s)
		}
	}
}
