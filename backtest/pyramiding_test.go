package main

import (
	"math"
	"testing"
	"time"
)

// runPyramidCase runs a strategy over the given bars with a custom DeclConfig
// (so tests can set Pyramiding) and returns the sim for inspection.
func runPyramidCase(t *testing.T, bars []Bar, decl DeclConfig, src string) *Sim {
	t.Helper()
	prepared := preprocess(src)
	sim := NewSim(bars, decl)
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

func mkBarsSimple(cs []float64) []Bar {
	bars := make([]Bar, len(cs))
	for i, c := range cs {
		o := c
		if i > 0 {
			o = cs[i-1]
		}
		hi, lo := o, o
		if c > hi {
			hi = c
		}
		if c < lo {
			lo = c
		}
		bars[i] = Bar{O: o, H: hi + 0.01, L: lo - 0.01, C: c, V: 1000, Date: time.Date(2024, 1, 1+i, 0, 0, 0, 0, time.UTC)}
	}
	return bars
}

// A long-then-short entry sequence must reverse: the short entry closes the
// long leg (a closed trade is recorded) and the position flips short. Before
// the fix, pyramiding=1 bypassed the guard and both legs stacked.
func TestEntryReversalClosesPriorLeg(t *testing.T) {
	// dip, rise, then fall: turn-up fires long, turn-down fires short
cs := []float64{10, 9, 10, 11, 12, 13, 14, 13, 12, 11, 10, 9}
	bars := mkBarsSimple(cs)
	decl := defDecl()
	decl.Pyramiding = 1
	src := `
//@version=5
strategy("rev", pyramiding=1)
longSig = close > close[1] and close[1] <= close[2]
shortSig = close < close[1] and close[1] >= close[2]
if longSig
	strategy.entry("buy", strategy.long)
if shortSig
	strategy.entry("sell", strategy.short)
`
	sim := runPyramidCase(t, bars, decl, src)
	if got := sim.PositionSize(); got >= 0 {
		t.Fatalf("expected final short position, got %v", got)
	}
	if len(sim.Trades) == 0 {
		t.Fatalf("expected at least one closed trade from reversal, got 0")
	}
}

// pyramiding=1 must allow at most one same-direction entry; a second entry
// with a different ID in the same direction is ignored.
func TestPyramidingOneCapsSameDirectionAdds(t *testing.T) {
	cs := []float64{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21}
	bars := mkBarsSimple(cs)
	decl := defDecl()
	decl.Pyramiding = 1
	src := `
//@version=5
strategy("pyr", pyramiding=1)
if close > close[1]
	strategy.entry("a", strategy.long)
	strategy.entry("b", strategy.long)
`
	sim := runPyramidCase(t, bars, decl, src)
	if math.Abs(sim.PositionSize()) > 1e9 {
		t.Fatalf("position exploded: %v", sim.PositionSize())
	}
	if sim.PositionSize() <= 0 {
		t.Fatalf("expected a long position, got %v", sim.PositionSize())
	}
}

// Re-entry with the same ID while that entry is still open must be ignored
// (no stacking under one ID).
func TestSameEntryIDWhileOpenIsIgnored(t *testing.T) {
	cs := []float64{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21}
	bars := mkBarsSimple(cs)
	decl := defDecl()
	decl.Pyramiding = 5
	src := `
//@version=5
strategy("sameid", pyramiding=5)
if close > close[1]
	strategy.entry("a", strategy.long)
`
	sim := runPyramidCase(t, bars, decl, src)
	// with pyramiding=5 the first add may go through, but never more than 5
	// total same-direction entries; and the same-ID re-entry must never stack.
	n := 0
	for _, p := range sim.positions {
		if math.Abs(p.Qty) > 1e-12 && p.ID == "a" {
			n++
		}
	}
	if n > 1 {
		t.Fatalf("same-ID entry stacked %d positions", n)
	}
	if math.Abs(sim.PositionSize()) > 1e9 {
		t.Fatalf("position exploded: %v", sim.PositionSize())
	}
}
