package main

import (
	"fmt"
	"math"
	"sort"
	"testing"
)

// TestProbeWoodieMultipliers measures per-trade equity growth factors for the
// Woodie pivot strategy over full AAPL data and prints the largest ones.
func TestProbeWoodieMultipliers(t *testing.T) {
	provider, err := LoadCSVProvider("AAPL", "../_bt_cache/AAPL.csv")
	if err != nil {
		t.Skipf("no AAPL data: %v", err)
	}
	bars := provider.bars
	src := `
//@version=5
strategy(title="Woodie Probe", overlay = true)
xHigh  = security("SYM","D", high[1])
xLow   = security("SYM","D", low[1])
xClose = security("SYM","D", close[1])
xPP = (xHigh+xLow+(xClose*2)) / 4
pos = (close[1] < xPP[1] and close > xPP ? 1 : (close < xPP ? -1 : nz(pos[1], 0)))
if (pos == 1)
    strategy.entry("Long", "long")
if (pos == -1)
    strategy.entry("Short", "short")
`
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

	type trk struct {
		i       int
		factor  float64
		entryB  int
		exitB   int
		dir     int
		ep, xp  float64
		eqB, eqA float64
	}
	// equity before trade k = equity at entry bar's PREVIOUS close (approx via entry bar open mark not stored); use equity[entryBar] vs equity[exitBar]
	factors := make([]trk, 0, len(sim.Trades))
	prevEq := initialCap(sim)
	for i, tr := range sim.Trades {
		eqA := sim.Equity[tr.ExitBar]
		var eqB float64
		if tr.EntryBar > 0 {
			eqB = sim.Equity[tr.EntryBar-1]
		} else {
			eqB = initialCap(sim)
		}
		_ = prevEq
		prevEq = eqA
		if eqB > 0 {
			factors = append(factors, trk{i: i, factor: eqA / eqB, entryB: tr.EntryBar, exitB: tr.ExitBar, dir: tr.Dir, ep: tr.EntryPrice, xp: tr.ExitPrice, eqB: eqB, eqA: eqA})
		}
	}
	sort.Slice(factors, func(a, b int) bool { return math.Abs(factors[a].factor-1) > math.Abs(factors[b].factor-1) })
	fmt.Printf("total trades=%d final equity=%.4g\n", len(sim.Trades), sim.Equity[len(sim.Equity)-1])
	for k := 0; k < 15 && k < len(factors); k++ {
		f := factors[k]
		eb, xb := bars[f.entryB], bars[f.exitB]
		fmt.Printf("trade %5d factor=%9.4f dir=%d entry(bar %d) O=%.4f H=%.4f L=%.4f C=%.4f | exit(bar %d) O=%.4f H=%.4f L=%.4f C=%.4f fillEp=%.4f xp=%.4f eq %.3g->%.3g\n",
			f.i, f.factor, f.dir, f.entryB, eb.O, eb.H, eb.L, eb.C, f.exitB, xb.O, xb.H, xb.L, xb.C, f.ep, f.xp, f.eqB, f.eqA)
	}
	// distribution of factors
	buckets := map[string]int{}
	for _, f := range factors {
		switch {
		case f.factor > 1.5:
			buckets[">1.5"]++
		case f.factor > 1.2:
			buckets["1.2-1.5"]++
		case f.factor > 1.05:
			buckets["1.05-1.2"]++
		case f.factor >= 0.95:
			buckets["0.95-1.05"]++
		case f.factor >= 0.8:
			buckets["0.8-0.95"]++
		default:
			buckets["<0.8"]++
		}
	}
	fmt.Println("factor buckets:", buckets)
}
