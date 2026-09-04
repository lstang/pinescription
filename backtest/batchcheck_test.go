package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestBatchCheck re-preprocesses and compiles every debug dump (.pine) in
// <cache>/debug and <cache>/debug_exec, then writes a catalog of which dumps
// still fail to compile with the CURRENT engine. Exec-only failures are
// flagged as COMPILE_OK (they need runtime fixes / source hand-fixes, and
// full re-execution happens in the harness).
func TestBatchCheck(t *testing.T) {
	cache := `F:/pitrading/_bt_cache`
	dirs := []string{filepath.Join(cache, "debug"), filepath.Join(cache, "debug_exec")}
	files := map[string]string{} // name -> path
	for _, d := range dirs {
		ents, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, en := range ents {
			if strings.HasSuffix(en.Name(), ".pine") {
				files[en.Name()] = filepath.Join(d, en.Name())
			}
		}
	}
	type res struct {
		name, status, msg string
	}
	var results []res
	var fails []string
	for name, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		prepared := preprocess(string(src))
		sim := NewSim(nil, defDecl())
		runner := &Runner{sim: sim}
		engine, err := runner.buildEngine()
		if err != nil {
			results = append(results, res{name, "hook", err.Error()})
			fails = append(fails, name+"\tHOOK\t"+err.Error())
			continue
		}
		_, cerr := engine.Compile(prepared)
		if cerr != nil {
			msg := cerr.Error()
			results = append(results, res{name, "compile", msg})
			fails = append(fails, name+"\tCOMPILE\t"+msg)
			continue
		}
		results = append(results, res{name, "compile_ok", ""})
	}
	sort.Strings(fails)
	out := filepath.Join(cache, "report_dbg", "batchcheck.txt")
	os.MkdirAll(filepath.Dir(out), 0o755)
	os.WriteFile(out, []byte(strings.Join(fails, "\n")), 0o644)
	ok := 0
	for _, r := range results {
		if r.status == "compile_ok" {
			ok++
		}
	}
	fmt.Printf("batch check: %d dumps, %d compile_ok, %d fail\n", len(results), ok, len(results)-ok)
	_ = json.Marshal
}
