package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// EntryMeta is the manifest entry for one strategy.
type EntryMeta struct {
	File         string            `json:"file"`
	Slug         string            `json:"slug"`
	Title        string            `json:"title"`
	Author       string            `json:"author"`
	Kind         string            `json:"kind"` // strategy | indicator
	Version      int               `json:"version"`
	Symbol       string            `json:"symbol"`
	SymbolSource string            `json:"symbol_source"`
	Decl         map[string]interface{} `json:"decl"`
	Code         string            `json:"code"`
	SkipReason   string            `json:"skip_reason,omitempty"` // for skipped files
}

// Result is the outcome of one backtest run.
type Result struct {
	Entry        EntryMeta
	Status       string // ok | compile_error | exec_error | no_pine | skipped | indicator
	Error        string
	Prepared     string // preprocessed pine used
	Symbol       string
	SymbolSource string
	Bars         []Bar
	First        string
	Last         string
	Decl         DeclConfig
	Metrics      Metrics
	Trades       []Trade
	Signals      []Signal
	Equity       []float64
	Dates        []string
	RunAt        time.Time
	EngineMs     int64
}

// writeReport writes the per-strategy markdown report plus CSVs.
func writeReport(reportDir string, r *Result) error {
	slug := sanitizeName(r.Entry.Slug)
	dir := filepath.Join(reportDir, "strategies")
	if err := os.MkdirAll(filepath.Join(dir, "trades"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "equity"), 0o755); err != nil {
		return err
	}

	// trades CSV
	if len(r.Trades) > 0 {
		if err := writeTradesCSV(filepath.Join(dir, "trades", slug+".csv"), r); err != nil {
			return err
		}
	}
	// equity CSV
	if len(r.Equity) > 0 {
		if err := writeEquityCSV(filepath.Join(dir, "equity", slug+".csv"), r); err != nil {
			return err
		}
	}

	var b strings.Builder
	b.WriteString("# " + safeTitle(r.Entry.Title) + "\n\n")
	if r.Entry.Author != "" {
		b.WriteString("**Author:** " + r.Entry.Author + "  \n")
	}
	b.WriteString(fmt.Sprintf("**Source file:** `%s`  \n", r.Entry.File))
	b.WriteString(fmt.Sprintf("**Report generated:** %s  \n\n", r.RunAt.Format("2006-01-02 15:04:05 MST")))

	b.WriteString("## Status\n\n")
	switch r.Status {
	case "ok":
		b.WriteString("✅ **Backtest completed**\n\n")
	case "compile_error":
		b.WriteString("❌ **Compile failed**\n\n")
	case "exec_error":
		b.WriteString("⚠️ **Execution failed**\n\n")
	case "indicator":
		b.WriteString("ℹ️ **Indicator script** — no strategy orders; execution only.\n\n")
	default:
		b.WriteString("⏭️ **Skipped** — " + r.Entry.SkipReason + "\n\n")
	}
	if r.Error != "" {
		b.WriteString("```\n" + r.Error + "\n```\n\n")
	}

	b.WriteString("## Configuration\n\n")
	b.WriteString("| Setting | Value |\n|---|---|\n")
	b.WriteString(fmt.Sprintf("| Script kind | %s (Pine v%d) |\n", r.Entry.Kind, r.Entry.Version))
	b.WriteString(fmt.Sprintf("| Symbol | %s (source: %s) |\n", r.Symbol, r.SymbolSource))
	b.WriteString(fmt.Sprintf("| Timeframe | 1D |\n"))
	b.WriteString(fmt.Sprintf("| Bars | %d |\n", len(r.Bars)))
	if r.First != "" {
		b.WriteString(fmt.Sprintf("| Period | %s → %s |\n", r.First, r.Last))
	}
	b.WriteString(fmt.Sprintf("| Initial capital | %s |\n", f2(r.Decl.InitialCapital)))
	b.WriteString(fmt.Sprintf("| Qty type / value | %s / %s |\n", r.Decl.QtyType, f2(r.Decl.QtyValue)))
	b.WriteString(fmt.Sprintf("| Pyramiding | %d |\n", r.Decl.Pyramiding))
	b.WriteString(fmt.Sprintf("| Process orders on close | %v |\n", r.Decl.ProcessOrdersOnClose))
	b.WriteString(fmt.Sprintf("| Commission | %s / %s |\n", r.Decl.CommissionType, f2(r.Decl.CommissionValue)))
	b.WriteString(fmt.Sprintf("| Engine time | %d ms |\n", r.EngineMs))
	b.WriteString("\n")

	if r.Status == "ok" {
		m := r.Metrics
		b.WriteString("## Performance\n\n")
		b.WriteString("| Metric | Strategy | Buy & Hold |\n|---|---|---|\n")
		b.WriteString(fmt.Sprintf("| Total return | **%s** | %s |\n", pct1(m.ReturnPct), pct1(m.BuyHoldReturn)))
		b.WriteString(fmt.Sprintf("| Final equity | %s | — |\n", f2(m.FinalEquity)))
		b.WriteString(fmt.Sprintf("| Net profit | %s | — |\n", f2(m.NetProfit)))
		b.WriteString(fmt.Sprintf("| CAGR (annualized) | %s | — |\n", pct1(m.CAGR)))
		b.WriteString(fmt.Sprintf("| Sharpe ratio | %s | — |\n", f2(m.Sharpe)))
		b.WriteString(fmt.Sprintf("| Sortino ratio | %s | — |\n", f2(m.Sortino)))
		b.WriteString(fmt.Sprintf("| Volatility (ann.) | %s | — |\n", pct1(m.Volatility)))
		b.WriteString(fmt.Sprintf("| Max drawdown | **%s** | — |\n", pct1(m.MaxDrawdown)))
		b.WriteString(fmt.Sprintf("| Exposure | %s | 100%% |\n", pct1(m.ExposurePct)))
		b.WriteString("\n")

		b.WriteString("## Trades\n\n")
		b.WriteString("| Metric | Value |\n|---|---|\n")
		b.WriteString(fmt.Sprintf("| Closed trades | %d |\n", m.NTrades))
		b.WriteString(fmt.Sprintf("| Win rate | %s |\n", pct1(m.WinRate)))
		b.WriteString(fmt.Sprintf("| Profit factor | %s |\n", f2(m.ProfitFactor)))
		b.WriteString(fmt.Sprintf("| Expectancy (per trade) | %s |\n", f2(m.Expectancy)))
		b.WriteString(fmt.Sprintf("| Avg win / avg loss | %s / %s |\n", f2(m.AvgWin), f2(m.AvgLoss)))
		b.WriteString(fmt.Sprintf("| Best / worst trade | %s / %s |\n", f2(m.BestTrade), f2(m.WorstTrade)))
		b.WriteString(fmt.Sprintf("| Max consecutive wins / losses | %d / %d |\n", m.MaxConsecWins, m.MaxConsecLoss))
		b.WriteString(fmt.Sprintf("| Avg bars held | %s |\n", f2(m.AvgBarsHeld)))
		b.WriteString("\n")

		if len(r.Trades) > 20 {
			b.WriteString("First 20 of " + fmt.Sprintf("%d", len(r.Trades)) + " trades shown; full list in CSV below.\n\n")
		}
		b.WriteString("| # | Entry date | Exit date | Side | Qty | Entry | Exit | PnL | Bars | Reason |\n|---|---|---|---|---|---|---|---|---|---|\n")
		limit := 20
		if len(r.Trades) < limit {
			limit = len(r.Trades)
		}
		for i := 0; i < limit; i++ {
			t := r.Trades[i]
			side := "long"
			if t.Dir < 0 {
				side = "short"
			}
			ed := ""
			if t.EntryBar >= 0 && t.EntryBar < len(r.Dates) {
				ed = r.Dates[t.EntryBar]
			}
			xd := ""
			if t.ExitBar >= 0 && t.ExitBar < len(r.Dates) {
				xd = r.Dates[t.ExitBar]
			}
			b.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s | %s | %s | %s | %d | %s |\n",
				i+1, ed, xd, side, f2(t.Qty), f4(t.EntryPrice), f4(t.ExitPrice), f2(t.Pnl), t.ExitBar-t.EntryBar, t.Reason))
		}
		b.WriteString("\n### Attachments\n\n")
		b.WriteString(fmt.Sprintf("- [Trades CSV](trades/%s.csv)\n", slug))
		b.WriteString(fmt.Sprintf("- [Equity curve CSV](equity/%s.csv)\n", slug))
		b.WriteString("\n")
	}

	b.WriteString("## Script\n\n")
	if r.Prepared != "" {
		b.WriteString("<details><summary>Preprocessed Pine Script used for this run (v" + fmt.Sprint(r.Entry.Version) + " → engine)</summary>\n\n```pine\n")
		b.WriteString(r.Prepared)
		b.WriteString("\n```\n\n</details>\n")
	} else {
		b.WriteString("_No script content._\n")
	}

	dest := filepath.Join(dir, slug+".md")
	return os.WriteFile(dest, []byte(b.String()), 0o644)
}

func writeTradesCSV(path string, r *Result) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"#", "entry_date", "exit_date", "side", "qty", "entry_price", "exit_price", "pnl", "bars_held", "reason", "entry_id"})
	for i, t := range r.Trades {
		ed := ""
		if t.EntryBar >= 0 && t.EntryBar < len(r.Dates) {
			ed = r.Dates[t.EntryBar]
		}
		xd := ""
		if t.ExitBar >= 0 && t.ExitBar < len(r.Dates) {
			xd = r.Dates[t.ExitBar]
		}
		side := "long"
		if t.Dir < 0 {
			side = "short"
		}
		_ = w.Write([]string{
			fmt.Sprintf("%d", i+1), ed, xd, side, f2(t.Qty), f4(t.EntryPrice), f4(t.ExitPrice),
			f2(t.Pnl), fmt.Sprintf("%d", t.ExitBar-t.EntryBar), t.Reason, t.EntryID,
		})
	}
	return w.Error()
}

func writeEquityCSV(path string, r *Result) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"date", "equity", "close", "position"})
	pos := 0.0
	// reconstruct position history from trades is complex; store equity + close only
	for i := 0; i < len(r.Equity); i++ {
		date := ""
		if i < len(r.Dates) {
			date = r.Dates[i]
		}
		cl := 0.0
		if i < len(r.Bars) {
			cl = r.Bars[i].C
		}
		_ = pos
		_ = w.Write([]string{date, f2(r.Equity[i]), f4(cl), f2(pos)})
	}
	return w.Error()
}

func safeTitle(t string) string {
	if t == "" {
		return "Untitled strategy"
	}
	return strings.NewReplacer("\n", " ", "\r", " ", "|", "\\|").Replace(t)
}

func sanitizeName(s string) string {
	s = strings.NewReplacer("\\", "_", "/", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_").Replace(s)
	s = strings.Trim(s, " .")
	if s == "" {
		return "untitled"
	}
	return s
}

// writeIndex writes report/index.csv and report/README.md summarizing all runs.
func writeIndex(reportDir string, results []*Result, counts map[string]int, runMeta RunMeta) error {
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return err
	}
	// CSV
	f, err := os.Create(filepath.Join(reportDir, "index.csv"))
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"slug", "title", "file", "author", "kind", "version", "status", "error", "symbol", "symbol_source", "bars", "return_pct", "cagr", "sharpe", "sortino", "max_drawdown", "trades", "win_rate", "profit_factor", "final_equity"})
	ok := 0
	for _, r := range results {
		m := r.Metrics
		_ = w.Write([]string{
			"strategies/" + sanitizeName(r.Entry.Slug),
			safeTitle(r.Entry.Title), r.Entry.File, r.Entry.Author, r.Entry.Kind,
			fmt.Sprintf("%d", r.Entry.Version), r.Status, r.Error, r.Symbol, r.SymbolSource,
			fmt.Sprintf("%d", len(r.Bars)), pct2(m.ReturnPct), pct1(m.CAGR), f2(m.Sharpe), f2(m.Sortino),
			pct1(m.MaxDrawdown), fmt.Sprintf("%d", m.NTrades), pct1(m.WinRate), f2(m.ProfitFactor), f2(m.FinalEquity),
		})
		if r.Status == "ok" {
			ok++
		}
	}
	w.Flush()
	f.Close()

	// README
	var b strings.Builder
	b.WriteString("# Backtest Report Index\n\n")
	b.WriteString(fmt.Sprintf("Generated **%s** by the Pinescription Go backtest harness.\n\n", runMeta.GeneratedAt.Format("2006-01-02 15:04:05")))
	b.WriteString("## Summary\n\n")
	b.WriteString("| Status | Count |\n|---|---|\n")
	for _, k := range []string{"ok", "compile_error", "exec_error", "indicator", "no_pine", "skipped"} {
		b.WriteString(fmt.Sprintf("| %s | %d |\n", k, counts[k]))
	}
	b.WriteString("\n")
	if runMeta.StrategiesDir != "" {
		b.WriteString(fmt.Sprintf("- Strategies directory: `%s`\n", runMeta.StrategiesDir))
	}
	if runMeta.DataCache != "" {
		b.WriteString(fmt.Sprintf("- Data cache: `%s`\n", runMeta.DataCache))
	}
	b.WriteString(fmt.Sprintf("- Parquet source: `%s`\n", runMeta.Parquet))
	b.WriteString(fmt.Sprintf("- Compiled: %d, executed OK: %d\n", counts["pine"], ok))
	b.WriteString("\nLink: [index.csv](index.csv) — full machine-readable results for every strategy.\n\n")

	// sort successful by return desc
	sortable := make([]*Result, 0, len(results))
	for _, r := range results {
		if r.Status == "ok" {
			sortable = append(sortable, r)
		}
	}
	if len(sortable) > 0 {
		b.WriteString("## Best performers (by total return)\n\n")
		sort.Slice(sortable, func(i, j int) bool {
			return sortable[i].Metrics.ReturnPct > sortable[j].Metrics.ReturnPct
		})
		limit := 30
		if len(sortable) < limit {
			limit = len(sortable)
		}
		b.WriteString("| Rank | Strategy | Symbol | Return | CAGR | Sharpe | MaxDD | Trades | Win rate | Link |\n|---|---|---|---|---|---|---|---|---|---|\n")
		for i := 0; i < limit; i++ {
			r := sortable[i]
			m := r.Metrics
			b.WriteString(fmt.Sprintf("| %d | %s | %s | %s | %s | %s | %s | %d | %s | [report](strategies/%s.md) |\n",
				i+1, safeTitle(r.Entry.Title), r.Symbol, pct1(m.ReturnPct), pct1(m.CAGR),
				f2(m.Sharpe), pct1(m.MaxDrawdown), m.NTrades, pct1(m.WinRate), sanitizeName(r.Entry.Slug)))
		}
		b.WriteString("\n")
	}

	b.WriteString("## All strategies\n\n")
	b.WriteString("| Strategy | Symbol | Status | Return | CAGR | Sharpe | MaxDD | Trades | Link |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range results {
		status := r.Status
		if r.Status == "ok" {
			m := r.Metrics
			b.WriteString(fmt.Sprintf("| %s | %s | ✅ | %s | %s | %s | %s | %d | [report](strategies/%s.md) |\n",
				safeTitle(r.Entry.Title), r.Symbol, pct1(m.ReturnPct), pct1(m.CAGR), f2(m.Sharpe), pct1(m.MaxDrawdown), m.NTrades, sanitizeName(r.Entry.Slug)))
		} else {
			why := r.Error
			if why == "" {
				why = r.Entry.SkipReason
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %s | — | — | — | — | — | [report](strategies/%s.md) |\n",
				safeTitle(r.Entry.Title), r.Symbol, status, sanitizeName(r.Entry.Slug)))
			_ = why
		}
	}
	b.WriteString("\n")
	return os.WriteFile(filepath.Join(reportDir, "README.md"), []byte(b.String()), 0o644)
}

type RunMeta struct {
	GeneratedAt  time.Time
	StrategiesDir string
	DataCache    string
	Parquet      string
}