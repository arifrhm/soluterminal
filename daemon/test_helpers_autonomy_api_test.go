package main

import (
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"runtime"
	"testing"
)

//go:embed main.go
var autonomyCompiledMainSource string

func autonomyBootStepBody(t *testing.T, step func(*bootEnv)) (*ast.BlockStmt, *token.FileSet, string) {
	t.Helper()
	pc := reflect.ValueOf(step).Pointer()
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		t.Fatal("boot step has no function metadata")
	}
	file, line := fn.FileLine(pc)
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse boot step source %s: %v", file, err)
	}
	var body *ast.BlockStmt
	ast.Inspect(parsed, func(n ast.Node) bool {
		if body != nil {
			return false
		}
		switch f := n.(type) {
		case *ast.FuncLit:
			if fset.Position(f.Pos()).Line == line {
				body = f.Body
			}
		case *ast.FuncDecl:
			if f.Body != nil && fset.Position(f.Pos()).Line == line {
				body = f.Body
			}
		}
		return true
	})
	if body == nil {
		t.Fatalf("boot step %s: no function starts at %s:%d", fn.Name(), file, line)
	}
	return body, fset, file
}
