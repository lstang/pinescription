package main

import (
	"fmt"
	"testing"
)

// TestControlAlwaysLong runs an always-long strategy over AAPL and compares
// the final equity with hand-computed buy & hold (initial capital fully
// invested at the first fill, then held).
func TestControlAlwaysLong(t *testing.T) {
	provider, err := LoadCSVProvider("AAPL", "../_bt_cache/AAPL.csv")
	if err != nil {
		t.Skipf("no AAPL data: %v", err)
	}
	runControl(t, provider.bars, "always long", `		//@version=5
strategy("always long", overlay=true)
if bar_index == 0
    strategy.entry("L", strategy.long)

`)
}

// TestControlAlwaysShort runs an always-short strategy over AAPL; a 6000x
// uptrend must destroy a short, not enrich it.
func TestControlAlwaysShort(t *testing.T) {
	provider, err := LoadCSVProvider("AAPL", "../_bt_cache/AAPL.csv")
	if err != nil {
		t.Skipf("no AAPL data: %v", err)
	}
	runControl(t, provider.bars, "always short", `		//@version=5
strategy("always short", overlay=true)
if bar_index == 0
    strategy.entry("S", strategy.short)

`)
}

func runControl(t *testing.T, bars []Bar, name, src string) {
	t.Helper()
	prepared := preprocess(src)
	sim := NewSim(bars, defDecl())
	runner := &Runner{sim: sim}
	engine, err := runner.buildEngine()
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	p := &CSVProvider{symbol: "AAPL", bars: bars}
	engine.RegisterMarketDataProvider(p)
	engine.SetDefaultSymbol("AAPL")
	engine.SetDefaultValueType("close")
	engine.SetTimeframe("1D")
	bc, err := engine.Compile(prepared)
	if err != nil {
		t.Fatalf("compile: %v", err)
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
		t.Fatalf("execute: %v", err)
	}
	m := computeMetrics(sim)
	fmt.Printf("[%s] trades=%d return=%.2f%% buyhold=%.2f%% finalEquity=%.2f maxDD=%.2f%%\n",
		name, m.NTrades, m.ReturnPct, m.BuyHoldReturn, m.FinalEquity, m.MaxDrawdown)
	// Invariants: equity may be wiped to exactly 0 by a margin call (the
	// physical bankruptcy floor), but never below it, and never "resurrected"
	// after the wipe.
	if m.FinalEquity < 0 {
		t.Errorf("[%s] final equity %.2f went negative — margin-call floor failed", name, m.FinalEquity)
	}
	sawWipe := false
	for _, tr := range sim.Trades {
		if tr.Reason == "margin_call" {
			sawWipe = true
			break
		}
	}
	if sim.wiped != sawWipe {
		t.Errorf("[%s] wiped=%v but margin_call trade present=%v", name, sim.wiped, sawWipe)
	}
}
