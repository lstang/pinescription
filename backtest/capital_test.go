package main

import (
	"math"
	"testing"
	"time"
)

// Declaring initial_capital in strategy() must seed the actual account cash,
// not just the metrics denominator. Before the fix, NewSim seeded cash from
// the 10000 default and the strategy() hook only updated Decl later, so a
// strategy declaring initial_capital=1 traded with 10000 while ReturnPct
// divided by 1 — inflating returns 10000x.
func TestDeclaredInitialCapitalSeedsCash(t *testing.T) {
	cs := []float64{10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21}
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
	src := `
//@version=5
strategy("cap", initial_capital=1234, default_qty_type=strategy.percent_of_equity, default_qty_value=100)
if bar_index == 1
	strategy.entry("e", strategy.long)
`
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
	m := computeMetrics(sim)
	if m.InitCapital != 1234 {
		t.Fatalf("InitCapital = %v, want 1234", m.InitCapital)
	}
	// 100% of 1234 equity buys at price ~11 -> position value ~1234; equity
	// must never exceed a small multiple of the declared capital. Before the
	// fix the sim traded with 10000 and equity reached ~10x the declared cap.
	maxEq := 0.0
	for _, v := range sim.Equity {
		if v > maxEq {
			maxEq = v
		}
	}
	if maxEq > 1234*5 {
		t.Fatalf("equity reached %.2f — cash was not seeded from declared initial_capital", maxEq)
	}
	if math.IsNaN(m.ReturnPct) {
		t.Fatalf("ReturnPct NaN")
	}
}
