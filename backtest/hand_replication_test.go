package main

import (
	"fmt"
	"testing"
)

// TestHandReplicatedWoodie implements the Woodie pivot flip strategy directly
// on the CSV bars with zero engine involvement: pos[t] from H/L/C[t-1] pivot
// and close crossings, entries at open[t+1], full-equity compounding. If this
// matches the engine's 10^58 result, the numbers are a property of the
// strategy+data, not a sim bug.
func TestHandReplicatedWoodie(t *testing.T) {
	provider, err := LoadCSVProvider("AAPL", "F:/pitrading/_bt_cache/AAPL.csv")
	if err != nil {
		t.Skipf("no AAPL data: %v", err)
	}
	bars := provider.bars
	pp := make([]float64, len(bars))
	for i := range bars {
		if i == 0 {
			pp[i] = 0 // na
			continue
		}
		pp[i] = (bars[i-1].H + bars[i-1].L + 2*bars[i-1].C) / 4
	}
	pos := 0
	cap := 10000.0
	trades := 0
	wins := 0
	prevPos := 0
	entryOpen := 0.0
	entryBar := -1
	for i := 1; i < len(bars); i++ {
		// pos rule (evaluated at bar i's close)
		var p int
		switch {
		case i >= 2 && bars[i-1].C < pp[i-1] && bars[i].C > pp[i]:
			p = 1
		case bars[i].C < pp[i]:
			p = -1
		default:
			p = prevPos
		}
		prevPos = p
		// fill at next bar's open for signals queued on bar i
		if i+1 < len(bars) && p != 0 && p != pos {
			// reverse at open[i+1]
			if pos != 0 {
				// close existing
				var pnlPct float64
				if pos > 0 {
					pnlPct = bars[i+1].O/entryOpen - 1
				} else {
					pnlPct = 1 - bars[i+1].O/entryOpen
				}
				cap *= 1 + pnlPct*0.999 // 0.1% round-trip cost
				trades++
				if pnlPct > 0 {
					wins++
				}
			}
			pos = p
			entryOpen = bars[i+1].O
			entryBar = i + 1
		}
	}
	_ = entryBar
	// mark final equity at last close
	finalEq := cap
	if pos != 0 {
		var pnlPct float64
		last := bars[len(bars)-1].C
		if pos > 0 {
			pnlPct = last/entryOpen - 1
		} else {
			pnlPct = 1 - last/entryOpen
		}
		finalEq = cap * (1 + pnlPct*0.999)
	}
	fmt.Printf("hand-replicated: trades=%d winrate=%.1f%% finalEquity=%.4g (return %.4g%%)\n",
		trades, float64(wins)/float64(trades)*100, finalEq, (finalEq/10000-1)*100)
}
