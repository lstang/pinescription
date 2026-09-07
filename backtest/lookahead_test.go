package main

import (
	"testing"
	"time"
)

// runLookaheadCase compiles and runs a strategy over the given bars, returning
// the sim so tests can inspect fills.
func runLookaheadCase(t *testing.T, bars []Bar, src string) *Sim {
	t.Helper()
	prepared := preprocess(src)
	sim := NewSim(bars, defDecl())
	runner := &Runner{sim: sim}
	engine, err := runner.buildEngine()
	if err != nil {
		t.Fatalf("hook: %v", err)
	}
	provider := &CSVProvider{symbol: "TEST", bars: bars}
	engine.RegisterMarketDataProvider(provider)
	engine.SetDefaultSymbol("TEST")
	engine.SetDefaultValueType("close")
	engine.SetTimeframe("1D")
	bc, err := engine.Compile(prepared)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, _, err = engine.ExecuteStepped(bc, func(barIdx int) error {
		// Same sequencing as the harness in main.go: after bar i's eval, step
		// bar i+1 (fills bar i's signals at bar i+1's open), then advance
		// currentBar so bar i+1's eval stamps its intents correctly.
		if barIdx == 0 {
			sim.Equity[0] = initialCap(sim)
		}
		if barIdx+1 < len(bars) {
			sim.Step(barIdx + 1)
		}
		if runner.err != nil {
			e := runner.err
			runner.err = nil
			return e
		}
		runner.currentBar = barIdx + 1
		return nil
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return sim
}

func mkBars(os_ []float64, cs []float64) []Bar {
	bars := make([]Bar, len(cs))
	for i := range cs {
		hi, lo := os_[i], os_[i]
		if cs[i] > hi {
			hi = cs[i]
		}
		if cs[i] < lo {
			lo = cs[i]
		}
		bars[i] = Bar{O: os_[i], H: hi + 0.01, L: lo - 0.01, C: cs[i], V: 1000, Date: time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC)}
	}
	return bars
}

// The look-ahead test: a strategy that enters long when close > open on the
// bar's own evaluation must NOT be filled at that same bar's open.
func TestNoLookaheadSameBarFill(t *testing.T) {
	// bar0: O=10 C=11 (up bar, entry signal) -> fill at bar1 open, not bar0 open
	// bar1: O=11 C=12 (up, hold)
	// bar2: O=12 C=10 (down, exit signal) -> fill at bar3 open, not bar2 open
	// bar3: O=10 C=10.5 (flat, just the exit fill bar)
	bars := mkBars(
		[]float64{10, 11, 12, 10},
		[]float64{11, 12, 10, 10.5},
	)
	src := `
//@version=5
strategy("lookahead", overlay=true)
strategy.entry("L", strategy.long, when = close > open)
strategy.close_all(when = close < open)
`
	sim := runLookaheadCase(t, bars, src)

	// Expect exactly one closed trade: entered at bar1 open (11), closed at bar3 open (10).
	if len(sim.Trades) != 1 {
		t.Fatalf("expected exactly 1 closed trade, got %d: %+v", len(sim.Trades), sim.Trades)
	}
	tr := sim.Trades[0]
	if tr.EntryBar != 1 {
		t.Errorf("entry must be on bar 1 (fill at bar1 open), got entryBar=%d", tr.EntryBar)
	}
	if tr.ExitBar != 3 {
		t.Errorf("exit must be on bar 3 (fill at bar3 open), got exitBar=%d", tr.ExitBar)
	}
	if absF(tr.EntryPrice-11.0) > 0.02 {
		t.Errorf("entry price should be bar1 open ~11 (slipped), got %v", tr.EntryPrice)
	}
	if absF(tr.ExitPrice-10.0) > 0.02 {
		t.Errorf("exit price should be bar3 open ~10 (slipped), got %v", tr.ExitPrice)
	}
}

// close > open triggers on bar0 → fill must happen at bar1 open. Combined with
// the close < open exit signal on the same bar0... no, keep it simple: signal on
// bar0, fill bar1.
func TestNoLookaheadSignalOnFirstBar(t *testing.T) {
	bars := mkBars([]float64{10, 11}, []float64{11, 12})
	src := `
//@version=5
strategy("first bar signal", overlay=true)
strategy.entry("L", strategy.long, when = close > open)
`
	sim := runLookaheadCase(t, bars, src)
	// With only 2 bars: signal on bar0 fills at bar1 open; position remains open
	// at the end (no more bars). Closed trades should be 0.
	if len(sim.Trades) != 0 {
		t.Fatalf("expected 0 closed trades (position still open at end), got %d: %+v", len(sim.Trades), sim.Trades)
	}
}

func absF(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
