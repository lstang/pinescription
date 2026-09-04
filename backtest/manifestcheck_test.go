package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestManifestCheck compiles the PRISTINE manifest code for every slug whose
// last recorded status is compile_error/exec_error, using the CURRENT engine.
// Writes:
//   <cache>/report_dbg/manifestcheck.txt   slug \t status \t error
//   <cache>/report_dbg/mpre/<slug>.pine    fresh preprocessed source
func TestManifestCheck(t *testing.T) {
	cache := `F:/pitrading/_bt_cache`

	// last status per slug from results.jsonl
	last := map[string]string{}
	if f, err := os.Open(filepath.Join(cache, "results.jsonl")); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
		for sc.Scan() {
			var r map[string]interface{}
			if json.Unmarshal(sc.Bytes(), &r) != nil {
				continue
			}
			slug, _ := r["slug"].(string)
			st, _ := r["status"].(string)
			if slug != "" && st != "" {
				last[slug] = st
			}
		}
		f.Close()
	}

	man := struct {
		Entries []struct {
			File  string `json:"file"`
			Slug  string `json:"slug"`
			Kind  string `json:"kind"`
			Code  string `json:"code"`
		} `json:"entries"`
	}{}
	data, err := os.ReadFile(filepath.Join(cache, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if err := json.Unmarshal(data, &man); err != nil {
		t.Fatalf("manifest json: %v", err)
	}

	outDir := filepath.Join(cache, "report_dbg", "mpre")
	os.MkdirAll(outDir, 0o755)

	type res struct{ slug, status, msg string }
	var results []res
	nDump := 0
	for _, e := range man.Entries {
		st, ok := last[e.Slug]
		if !ok || (st != "compile_error" && st != "exec_error") {
			continue
		}
		prepared := preprocess(e.Code)
		// dump fresh preprocessed source
		os.WriteFile(filepath.Join(outDir, e.Slug+".pine"), []byte(prepared), 0o644)
		nDump++
		// also dump pristine for diffing
		os.WriteFile(filepath.Join(outDir, e.Slug+".orig"), []byte(e.Code), 0o644)

		sim := NewSim(nil, defDecl())
		runner := &Runner{sim: sim}
		engine, herr := runner.buildEngine()
		if herr != nil {
			results = append(results, res{e.Slug, "hook", herr.Error()})
			continue
		}
		if _, cerr := engine.Compile(prepared); cerr != nil {
			results = append(results, res{e.Slug, "compile", cerr.Error()})
			continue
		}
		results = append(results, res{e.Slug, "compile_ok", ""})
	}

	sort.Slice(results, func(i, j int) bool { return results[i].slug < results[j].slug })
	var lines []string
	ok := 0
	for _, r := range results {
		if r.status == "compile_ok" {
			ok++
		}
		lines = append(lines, r.slug+"\t"+r.status+"\t"+r.msg)
	}
	out := filepath.Join(cache, "report_dbg", "manifestcheck.txt")
	os.WriteFile(out, []byte(strings.Join(lines, "\n")), 0o644)
	fmt.Printf("manifest check: %d failing slugs, %d dumped, %d compile_ok, %d still fail\n",
		len(results), nDump, ok, len(results)-ok)
}
