package mobile

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"leaflet/locales"
)

func TestPanicReturnsAnError(t *testing.T) {
	c, _ := open(t)
	onQueue := func() (err error) {
		defer catch(&err)
		c.q.do(func() { panic("on the queue") })
		return nil
	}
	if err := onQueue(); err == nil || err.Error() != locales.AppErrorInternal("on the queue") {
		t.Fatalf("a panic on the queue returned %v", err)
	}
	if _, err := c.Status(); err != nil {
		t.Fatalf("the queue stopped: %v", err)
	}
	offQueue := func() (_ string, err error) {
		defer catch(&err)
		var m map[string]int
		m["x"] = 1 //nolint:staticcheck
		return "done", nil
	}
	if out, err := offQueue(); out != "" || err == nil || !strings.Contains(err.Error(), "nil map") {
		t.Fatalf("a panic off the queue returned %q, %v", out, err)
	}
}

func TestExportedFunctionsCatch(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || !fd.Name.IsExported() || !exportedRecv(fd) {
				continue
			}
			if !catches(fd) {
				t.Errorf("%s: %s does not begin with defer catch(…)", fset.Position(fd.Pos()), fd.Name.Name)
			}
		}
	}
}

func exportedRecv(fd *ast.FuncDecl) bool {
	if fd.Recv == nil {
		return true
	}
	typ := fd.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	id, ok := typ.(*ast.Ident)
	return ok && id.IsExported()
}

func catches(fd *ast.FuncDecl) bool {
	if len(fd.Body.List) == 0 {
		return false
	}
	ds, ok := fd.Body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}
	if fn, ok := ds.Call.Fun.(*ast.Ident); !ok || fn.Name != "catch" || len(ds.Call.Args) != 1 {
		return false
	}
	res := fd.Type.Results
	returnsErr := false
	if res != nil && len(res.List) > 0 {
		id, ok := res.List[len(res.List)-1].Type.(*ast.Ident)
		returnsErr = ok && id.Name == "error"
	}
	switch arg := ds.Call.Args[0].(type) {
	case *ast.Ident:
		return arg.Name == "nil" && !returnsErr
	case *ast.UnaryExpr:
		if !returnsErr || arg.Op != token.AND {
			return false
		}
		last := res.List[len(res.List)-1]
		x, ok := arg.X.(*ast.Ident)
		return ok && len(last.Names) > 0 && x.Name == last.Names[len(last.Names)-1].Name
	}
	return false
}

func TestClosedQueueRunsWhatWasPosted(t *testing.T) {
	q := newQueue()
	ran := make(chan int, 2)
	q.post(func() { ran <- 1 })
	q.post(func() { ran <- 2 })
	q.close()
	for want := range 2 {
		select {
		case got := <-ran:
			if got != want+1 {
				t.Fatalf("ran %d, want %d", got, want+1)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("work posted before the queue closed did not run")
		}
	}
}

func TestFailedOpenLeavesNoWorkers(t *testing.T) {
	file := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _ := json.Marshal(config{DataDir: file, Locale: "en"})
	p := &fakePlatform{t: t, secrets: map[string][]byte{}}
	p.logUntilEnd()
	before := runtime.NumGoroutine()
	for range 10 {
		if _, err := Open(string(cfg), p, &fakeEvents{}); err == nil {
			t.Fatal("a core opened in a file")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before+2 {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines after failed opens, %d before", runtime.NumGoroutine(), before)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
