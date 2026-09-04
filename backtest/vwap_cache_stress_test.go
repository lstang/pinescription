package main

import (
	"testing"
	"time"
)

// TestVWAPSeriesCacheStress reproduces the hang where builtinVWAP calls
// getSeries inside its per-bar loop: every call re-fetches series and, once
// the provider's queue wraps, refetch allocates repeatedly. The engine must
// serve repeated getSeries calls for the same key from its own cache in O(1).
func TestVWAPSeriesCacheStress(t *testing.T) {
	n := 3000
	bars := make([]Bar, n)
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range bars {
		bars[i] = Bar{Date: base.AddDate(0, 0, i), O: 100, H: 101, L: 99, C: 100.5, V: 1000}
	}
	prov := &CSVProvider{symbol: "TEST", bars: bars}
	r := &Runner{sim: NewSim(bars, defDecl())}
	e, err := r.buildEngine()
	if err != nil {
		t.Fatal(err)
	}
	e.RegisterMarketDataProvider(prov)
	e.SetDefaultSymbol("TEST")
	e.SetDefaultValueType("close")
	e.SetTimeframe("1D")
	bc, err := e.Compile("v = ta.vwap(close)\nplot(v)")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, _, err := e.ExecuteStepped(bc, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 45*time.Second {
		t.Fatalf("vwap execution took %s — series cache regression", elapsed)
	}
}
