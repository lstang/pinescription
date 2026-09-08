package main

import (
	"math"
	"testing"
)

// A short position that loses more than 100% of equity must hit the
// margin-call floor: equity is wiped to 0, a margin_call trade is booked,
// and no further orders fill. Before the fix, equity drifted arbitrarily
// negative (e.g. -1.6e13%), which no real account can produce.
func TestMarginCallWipesShortBeyondFullLoss(t *testing.T) {
	cs := []float64{
		100, 100, 100, 100, 100,
		// explosive rally against the short
		120, 150, 200, 280, 400,
		560, 800, 1100, 1500, 2000,
		2600, 3300, 4100, 5000, 6000,
	}
	decl := defDecl()
	decl.QtyType = "percent_of_equity"
	decl.QtyValue = 100 // full-equity short
	src := `
//@version=5
strategy("shortwipe", pyramiding=0)
if close <= close[1]
    strategy.entry("short", strategy.short)
`
	sim := runPyramidCase(t, mkBarsSimple(cs), decl, src)

	// Find the wipe bar: first bar where equity <= 0.
	wipeIdx := -1
	for i, e := range sim.Equity {
		if e <= 0 {
			wipeIdx = i
			break
		}
	}
	if wipeIdx < 0 {
		t.Fatalf("expected a margin call, got final equity %v (min equity %v)",
			sim.Equity[len(sim.Equity)-1], minEq(sim.Equity))
	}
	// After the wipe, equity must be pinned at exactly 0 — no negative
	// drift, no resurrection.
	for i := wipeIdx; i < len(sim.Equity); i++ {
		if math.Abs(sim.Equity[i]) > 1e-9 {
			t.Fatalf("equity after margin call at bar %d is %v at bar %d; want 0",
				wipeIdx, sim.Equity[i], i)
		}
	}
	// A margin_call trade must be recorded.
	found := false
	for _, tr := range sim.Trades {
		if tr.Reason == "margin_call" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no margin_call trade recorded; trades=%+v", sim.Trades)
	}
	// And no further fills: nothing may exit after the wipe bar.
	for _, tr := range sim.Trades {
		if tr.ExitBar > wipeIdx {
			t.Fatalf("trade exited at bar %d after margin call at bar %d", tr.ExitBar, wipeIdx)
		}
	}
}

// A healthy long-only strategy must be completely unaffected by the
// margin-call machinery.
func TestMarginCallDoesNotTouchHealthyStrategy(t *testing.T) {
	cs := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110}
	decl := defDecl()
	decl.QtyType = "percent_of_equity"
	decl.QtyValue = 100
	src := `
//@version=5
strategy("longok", pyramiding=0)
if close > close[1]
    strategy.entry("long", strategy.long)
`
	sim := runPyramidCase(t, mkBarsSimple(cs), decl, src)

	if sim.wiped {
		t.Fatalf("healthy long strategy was margin-called")
	}
	last := sim.Equity[len(sim.Equity)-1]
	if last <= sim.Equity[0] {
		t.Fatalf("long strategy should profit, got %v -> %v", sim.Equity[0], last)
	}
	for _, tr := range sim.Trades {
		if tr.Reason == "margin_call" {
			t.Fatalf("unexpected margin_call trade: %+v", tr)
		}
	}
}

func minEq(eq []float64) float64 {
	m := math.Inf(1)
	for _, e := range eq {
		if e < m {
			m = e
		}
	}
	return m
}
