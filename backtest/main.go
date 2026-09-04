package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ResultRecord is the compact persistent record of one completed strategy
// run, written to <cache>/results.jsonl immediately after each strategy so
// progress survives interrupts.
type ResultRecord struct {
	Slug         string    `json:"slug"`
	Entry        EntryMeta `json:"entry"`
	Status       string    `json:"status"`
	Error        string    `json:"error"`
	Symbol       string    `json:"symbol"`
	SymbolSource string    `json:"symbol_source"`
	First        string    `json:"first"`
	Last         string    `json:"last"`
	Metrics      Metrics   `json:"metrics"`
	RunAt        time.Time `json:"run_at"`
	EngineMs     int64     `json:"engine_ms"`
}

func (rec ResultRecord) toResult() *Result {
	return &Result{
		Entry: rec.Entry, Status: rec.Status, Error: rec.Error,
		Symbol: rec.Symbol, SymbolSource: rec.SymbolSource,
		First: rec.First, Last: rec.Last, Metrics: rec.Metrics,
		RunAt: rec.RunAt, EngineMs: rec.EngineMs,
	}
}

func resultsStatePath(opt options) string {
	return filepath.Join(opt.cacheDir, "results.jsonl")
}

func loadRecords(path string) (map[string]ResultRecord, error) {
	recs := map[string]ResultRecord{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return recs, nil
		}
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var rec ResultRecord
		if err := dec.Decode(&rec); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		recs[rec.Slug] = rec
	}
	return recs, nil
}

var recordMu sync.Mutex

// sanitizeNaN replaces NaN/Inf float values with 0 so records can be JSON
// marshaled (encoding/json rejects non-finite floats).
func sanitizeNaN(m Metrics) Metrics {
	fix := func(v float64) float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		return v
	}
	m.InitCapital = fix(m.InitCapital)
	m.FinalEquity = fix(m.FinalEquity)
	m.NetProfit = fix(m.NetProfit)
	m.ReturnPct = fix(m.ReturnPct)
	m.CAGR = fix(m.CAGR)
	m.BuyHoldReturn = fix(m.BuyHoldReturn)
	m.MaxDrawdown = fix(m.MaxDrawdown)
	m.Sharpe = fix(m.Sharpe)
	m.Sortino = fix(m.Sortino)
	m.Volatility = fix(m.Volatility)
	m.WinRate = fix(m.WinRate)
	m.ProfitFactor = fix(m.ProfitFactor)
	m.Expectancy = fix(m.Expectancy)
	m.AvgWin = fix(m.AvgWin)
	m.AvgLoss = fix(m.AvgLoss)
	m.BestTrade = fix(m.BestTrade)
	m.WorstTrade = fix(m.WorstTrade)
	m.AvgBarsHeld = fix(m.AvgBarsHeld)
	m.ExposurePct = fix(m.ExposurePct)
	return m
}

// saveRecord appends the compact record for a finished run to the state file.
func saveRecord(path string, res *Result) {
	rec := ResultRecord{
		Slug: res.Entry.Slug, Entry: res.Entry, Status: res.Status, Error: res.Error,
		Symbol: res.Symbol, SymbolSource: res.SymbolSource,
		First: res.First, Last: res.Last, Metrics: sanitizeNaN(res.Metrics),
		RunAt: res.RunAt, EngineMs: res.EngineMs,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	recordMu.Lock()
	defer recordMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// Manifest is the extraction result.
type Manifest struct {
	StrategiesDir string      `json:"strategies_dir"`
	Entries      []EntryMeta  `json:"entries"`
	NeededSymbols map[string]string `json:"needed_symbols"`
	Counts       map[string]int `json:"counts"`
}

type options struct {
	strategiesDir string
	manifestPath  string
	cacheDir      string
	reportDir     string
	parquetPath   string
	limit         int
	workers       int
	only          string
	symbolOverride string
	extract        bool
	single         string
	skipExisting   bool
	retryFailed    bool
	verbose        bool
	profileSlug    string
	blacklist      string
	slugs          string
}

func main() {
	opt := parseFlags()

	if err := run(opt); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func parseFlags() options {
	var o options
	flag.StringVar(&o.strategiesDir, "strategies", "F:/dev/test/fmzquant-strategies", "strategy markdown directory")
	flag.StringVar(&o.cacheDir, "cache", "F:/pitrading/_bt_cache", "symbol CSV cache dir")
	flag.StringVar(&o.manifestPath, "manifest", "", "manifest json path (default <cache>/manifest.json)")
	flag.StringVar(&o.reportDir, "report", "", "report output dir (default <strategies>/report)")
	flag.StringVar(&o.parquetPath, "parquet", "F:/pitrading/topNFixed.parquet", "source parquet path")
	flag.IntVar(&o.limit, "limit", 0, "limit number of strategies to process (0 = all)")
	flag.IntVar(&o.workers, "workers", 4, "parallel workers")
	flag.StringVar(&o.only, "only", "", "only run slugs containing this substring")
	flag.StringVar(&o.slugs, "slugs", "", "file with exact slugs to run (one per line)")
	flag.StringVar(&o.symbolOverride, "symbol", "", "override symbol for all runs")
	flag.BoolVar(&o.extract, "extract", false, "rerun the python extractor first")
	flag.StringVar(&o.single, "single", "", "run a single strategy by slug (debug)")
	flag.BoolVar(&o.skipExisting, "skip-existing", false, "skip strategies that already have a report")
	flag.BoolVar(&o.retryFailed, "retry-failed", false, "only re-run strategies whose previous record is compile_error or exec_error")
	flag.BoolVar(&o.verbose, "verbose", false, "verbose logging")
	flag.StringVar(&o.profileSlug, "profile-slug", "", "run a single strategy and dump goroutine stacks every 2s (debug hangs)")
	flag.StringVar(&o.blacklist, "blacklist", "", "file with slugs to skip (one per line)")
	flag.Parse()

	if o.manifestPath == "" {
		o.manifestPath = filepath.Join(o.cacheDir, "manifest.json")
	}
	if o.reportDir == "" {
		o.reportDir = filepath.Join(o.strategiesDir, "report")
	}
	return o
}

func run(opt options) error {
	if opt.extract || !fileExists(opt.manifestPath) {
		if err := runExtractor(opt); err != nil {
			return err
		}
	}

	data, err := os.ReadFile(opt.manifestPath)
	if err != nil {
		return err
	}
	var man Manifest
	if err := json.Unmarshal(data, &man); err != nil {
		return err
	}

	if opt.profileSlug != "" {
		return runProfile(opt, man, opt.profileSlug)
	}

	statePath := resultsStatePath(opt)
	prev, err := loadRecords(statePath)
	if err != nil {
		return fmt.Errorf("load %s: %w", statePath, err)
	}

	// explicit slug whitelist (exact slugs, one per line)
	var want map[string]bool
	if opt.slugs != "" {
		want = map[string]bool{}
		if data, err := os.ReadFile(opt.slugs); err == nil {
			for _, ln := range strings.Split(string(data), "\n") {
				ln = strings.TrimSpace(ln)
				if ln != "" {
					want[ln] = true
				}
			}
		}
	}

	// blacklist of slugs that crash the engine (stack overflow etc.) and are
	// skipped instead of run
	black := map[string]bool{}
	if opt.blacklist != "" {
		if data, err := os.ReadFile(opt.blacklist); err == nil {
			for _, ln := range strings.Split(string(data), "\n") {
				ln = strings.TrimSpace(ln)
				if ln != "" {
					black[ln] = true
				}
			}
		}
	}

	// select entries
	var entries []EntryMeta
	for _, e := range man.Entries {
		if e.Kind == "skipped" {
			continue
		}
		if black[e.Slug] {
			continue
		}
		if opt.only != "" && !strings.Contains(e.Slug, opt.only) {
			continue
		}
		if opt.single != "" && e.Slug != opt.single {
			continue
		}
		if want != nil && !want[e.Slug] {
			continue
		}
		if opt.skipExisting {
			if _, ok := prev[e.Slug]; ok {
				continue
			}
		}
		if opt.retryFailed {
			if rec, ok := prev[e.Slug]; ok {
				if rec.Status != "compile_error" && rec.Status != "exec_error" {
					continue
				}
			} else {
				continue
			}
		}
		if e.Kind == "indicator" && opt.retryFailed {
			continue
		}
		entries = append(entries, e)
	}
	if opt.limit > 0 && len(entries) > opt.limit {
		entries = entries[:opt.limit]
	}

	mode := "full"
	if opt.retryFailed {
		mode = "retry-failed"
	}
	fmt.Printf("manifest: %d pine entries total, %d selected (%s), %d previously completed, %d blacklisted\n", len(man.Entries), len(entries), mode, len(prev), len(black))

	// ensure data for all needed symbols
	needed := map[string]string{}
	for _, e := range entries {
		sym := e.Symbol
		if opt.symbolOverride != "" {
			sym = opt.symbolOverride
		}
		needed[sym] = e.SymbolSource
	}
	if err := ensureData(opt, needed); err != nil {
		return err
	}

	// run in parallel
	start := time.Now()
	results := make([]*Result, len(entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, opt.workers)
	for i := range entries {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			e := entries[i]
			sym := e.Symbol
			if opt.symbolOverride != "" {
				sym = opt.symbolOverride
			}
			if opt.verbose {
				fmt.Printf("[start] %s\n", e.Slug)
			}
			res := runOneSafe(opt, e, sym)
			results[i] = res
			// persist immediately so an interrupt loses nothing
			saveRecord(statePath, res)
			if err := writeReport(opt.reportDir, res); err != nil {
				fmt.Fprintln(os.Stderr, "report write error:", err)
			}
			// drop the per-bar payloads after the report is written; the final
			// index only needs Entry/Status/Symbol/Metrics, and thousands of
			// retained series otherwise balloon memory to tens of GB
			res.Bars = nil
			res.Dates = nil
			res.Equity = nil
			res.Trades = nil
			res.Signals = nil
			res.Prepared = ""
			if opt.verbose || res.Status != "ok" {
				fmt.Printf("[%s] %s (%s)\n", res.Status, res.Entry.Slug, firstLine(res.Error))
			} else {
				fmt.Printf("[%s] %s return=%s\n", res.Status, res.Entry.Slug, pct1(res.Metrics.ReturnPct))
			}
		}(i)
	}
	wg.Wait()

	// merge this run's results with previously completed records
	all := make([]*Result, 0, len(prev)+len(entries))
	seen := map[string]bool{}
	for _, r := range results {
		if r == nil {
			continue
		}
		seen[r.Entry.Slug] = true
		all = append(all, r)
	}
	for slug, rec := range prev {
		if !seen[slug] {
			all = append(all, rec.toResult())
		}
	}

	counts := map[string]int{}
	for _, r := range all {
		counts[r.Status]++
	}
	counts["pine"] = len(man.Entries)
	counts["skipped"] = man.Counts["skipped"]

	meta := RunMeta{
		GeneratedAt:   time.Now(),
		StrategiesDir: opt.strategiesDir,
		DataCache:     opt.cacheDir,
		Parquet:       opt.parquetPath,
	}
	if err := writeIndex(opt.reportDir, all, counts, meta); err != nil {
		return err
	}

	fmt.Printf("done: %d runs in %s\n", len(entries), time.Since(start).Round(time.Millisecond))
	return nil
}

func dumpCompileFailure(opt options, e EntryMeta, prepared string, err error) {
	dir := filepath.Join(opt.cacheDir, "debug")
	_ = os.MkdirAll(dir, 0o755)
	name := sanitizeName(e.Slug)
	_ = os.WriteFile(filepath.Join(dir, name+".pine"), []byte(prepared), 0o644)
	_ = os.WriteFile(filepath.Join(dir, name+".err"), []byte(err.Error()), 0o644)
}

func dumpExecFailure(opt options, e EntryMeta, prepared string, err error) {
	dir := filepath.Join(opt.cacheDir, "debug_exec")
	_ = os.MkdirAll(dir, 0o755)
	name := sanitizeName(e.Slug)
	_ = os.WriteFile(filepath.Join(dir, name+".pine"), []byte(prepared), 0o644)
	_ = os.WriteFile(filepath.Join(dir, name+".err"), []byte(err.Error()), 0o644)
}

func firstLine(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "\n", " | ")
	if len(s) > 160 {
		return s[:160]
	}
	return s
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func runExtractor(opt options) error {
	scriptPath := filepath.Join(mustGetwd(), "backtest", "extract.py")
	cmd := exec.Command("python", scriptPath, "--strategies", opt.strategiesDir, "--manifest", opt.manifestPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ensureData makes sure each symbol has a CSV in the cache (parquet or yfinance).
func ensureData(opt options, symbols map[string]string) error {
	var missing []string
	for sym := range symbols {
		if !fileExists(filepath.Join(opt.cacheDir, sym+".csv")) {
			missing = append(missing, sym)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	fmt.Printf("fetching data for %d symbols: %s\n", len(missing), strings.Join(missing, ", "))
	scriptPath := filepath.Join(mustGetwd(), "dataprep.py")
	if !fileExists(scriptPath) {
		// allow running from the backtest/ dir itself
		scriptPath = filepath.Join(mustGetwd(), "backtest", "dataprep.py")
	}
	cmd := exec.Command("python", scriptPath,
		"--cache", opt.cacheDir,
		"--parquet", opt.parquetPath,
		"--symbols", strings.Join(missing, ","))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dataprep failed for %v: %w", missing, err)
	}
	return nil
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// runProfile runs a single strategy through preprocess -> compile -> execute
// while dumping goroutine stacks every 2s, for diagnosing hangs.
func runProfile(opt options, man Manifest, slug string) error {
	var entry *EntryMeta
	for i := range man.Entries {
		if man.Entries[i].Slug == slug {
			entry = &man.Entries[i]
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("slug not found in manifest: %s", slug)
	}
	sym := entry.Symbol
	provider, err := LoadCSVProvider(sym, filepath.Join(opt.cacheDir, sym+".csv"))
	if err != nil {
		return fmt.Errorf("data load: %w", err)
	}
	fmt.Printf("bars: %d\n", len(provider.bars))

	prepared := preprocess(entry.Code)
	fmt.Printf("preprocess done, prepared len: %d\n", len(prepared))

	sim := NewSim(provider.bars, defDecl())
	runner := &Runner{sim: sim}
	engine, err := runner.buildEngine()
	if err != nil {
		return fmt.Errorf("hook registration: %w", err)
	}
	engine.RegisterMarketDataProvider(provider)
	engine.SetDefaultSymbol(sym)
	engine.SetDefaultValueType("close")
	engine.SetTimeframe("1D")
	if len(provider.bars) > 0 {
		engine.SetStartTime(provider.bars[0].Date)
	}

	bc, err := engine.Compile(prepared)
	if err != nil {
		fmt.Printf("compile err: %v\n", err)
		return nil
	}
	fmt.Println("compiled ok")

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(2 * time.Second):
				buf := make([]byte, 1<<20)
				n := runtime.Stack(buf, true)
				fmt.Println("==== STACK DUMP ====")
				fmt.Println(string(buf[:min(n, 4000)]))
			}
		}
	}()

	start := time.Now()
	_, _, err = engine.ExecuteStepped(bc, nil)
	elapsed := time.Since(start)
	close(done)
	fmt.Printf("execute done in %s, err: %v\n", elapsed, err)
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// runOne compiles and executes one strategy, returning the full result.
// runOneSafe wraps runOne with a panic recover: a fatal panic in one
// strategy (deep recursion -> stack overflow, nil deref) must not abort the
// whole batch. A recovered panic is recorded as an exec_error so the slug
// shows up in the report and can be retried after a fix.
func runOneSafe(opt options, e EntryMeta, sym string) (res *Result) {
	defer func() {
		if r := recover(); r != nil {
			res = &Result{
				Entry: e, Symbol: sym, SymbolSource: e.SymbolSource,
				Status: "exec_error",
				Error: fmt.Sprintf("panic: %v", r),
				RunAt: time.Now(),
			}
		}
	}()
	return runOne(opt, e, sym)
}

func runOne(opt options, e EntryMeta, symbol string) *Result {
	res := &Result{
		Entry:        e,
		Status:       "ok",
		Symbol:       symbol,
		SymbolSource: e.SymbolSource,
		RunAt:        time.Now(),
	}
	start := time.Now()
	defer func() { res.EngineMs = time.Since(start).Milliseconds() }()

	if e.Kind == "indicator" {
		res.Status = "indicator"
	}

	provider, err := LoadCSVProvider(symbol, filepath.Join(opt.cacheDir, symbol+".csv"))
	if err != nil {
		res.Status = "exec_error"
		res.Error = fmt.Sprintf("data load: %v", err)
		return res
	}
	res.Bars = provider.bars
	res.Dates = make([]string, len(provider.bars))
	for i, b := range provider.bars {
		res.Dates[i] = b.Date.Format("2006-01-02")
	}
	if len(provider.bars) > 0 {
		res.First = res.Dates[0]
		res.Last = res.Dates[len(res.Dates)-1]
	}

	prepared := preprocess(e.Code)
	res.Prepared = prepared

	sim := NewSim(provider.bars, defDecl())
	runner := &Runner{sim: sim}
	engine, err := runner.buildEngine()
	if err != nil {
		res.Status = "compile_error"
		res.Error = fmt.Sprintf("hook registration: %v", err)
		return res
	}
	engine.RegisterMarketDataProvider(provider)
	engine.SetDefaultSymbol(symbol)
	engine.SetDefaultValueType("close")
	engine.SetTimeframe("1D")
	// Bound pathological scripts (quadratic per-bar work, runaway loops) that
	// would otherwise hang a worker forever. 2 minutes is ~10x the p95 runtime
	// of a healthy strategy at these bar counts.
	engine.SetExecBudget(2 * time.Minute)
	if len(provider.bars) > 0 {
		engine.SetStartTime(provider.bars[0].Date)
	}

	bc, err := engine.Compile(prepared)
	if err != nil {
		res.Status = "compile_error"
		res.Error = fmt.Sprintf("compile: %v", err)
		dumpCompileFailure(opt, e, prepared, err)
		return res
	}

	_, _, err = engine.ExecuteStepped(bc, func(barIdx int) error {
		runner.currentBar = barIdx
		sim.Step(barIdx)
		if runner.err != nil {
			e := runner.err
			runner.err = nil
			return e
		}
		return nil
	})
	if err != nil {
		res.Status = "exec_error"
		res.Error = fmt.Sprintf("execute: %v", err)
		dumpExecFailure(opt, e, prepared, err)
		return res
	}
	if runner.err != nil {
		res.Status = "exec_error"
		res.Error = fmt.Sprintf("execute: %v", runner.err)
		dumpExecFailure(opt, e, prepared, runner.err)
		return res
	}

	res.Decl = sim.Decl
	res.Metrics = computeMetrics(sim)
	res.Trades = sim.Trades
	res.Signals = sim.Signals
	res.Equity = sim.Equity
	return res
}

