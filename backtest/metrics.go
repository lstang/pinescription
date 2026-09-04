package main

import (
	"math"
	"strconv"
)

// Metrics aggregates performance statistics for a backtest.
type Metrics struct {
	InitCapital   float64
	FinalEquity   float64
	NetProfit     float64
	ReturnPct     float64
	CAGR          float64
	BuyHoldReturn float64
	MaxDrawdown   float64
	Sharpe        float64
	Sortino       float64
	Volatility    float64 // annualized

	NTrades       int
	WinRate       float64
	ProfitFactor  float64
	Expectancy    float64
	AvgWin        float64
	AvgLoss       float64
	BestTrade     float64
	WorstTrade    float64
	MaxConsecWins int
	MaxConsecLoss int
	AvgBarsHeld   float64
	ExposurePct   float64
}

const daysPerYear = 252.0

func computeMetrics(s *Sim) Metrics {
	m := Metrics{}
	init := initialCap(s)
	m.InitCapital = init
	n := len(s.Equity)
	if n == 0 {
		return m
	}
	m.FinalEquity = s.Equity[n-1]
	m.NetProfit = m.FinalEquity - init
	if init != 0 {
		m.ReturnPct = m.NetProfit / init * 100
	}
	if init > 0 && m.FinalEquity > 0 && n > 0 {
		m.CAGR = (math.Pow(m.FinalEquity/init, daysPerYear/float64(n)) - 1) * 100
	}
	if len(s.Bars) > 1 && s.Bars[0].C != 0 {
		m.BuyHoldReturn = (s.Bars[n-1].C/s.Bars[0].C - 1) * 100
	}
	m.MaxDrawdown = maxDrawdown(s.Equity, init) * 100

	// daily returns
	ret := make([]float64, 0, n)
	prev := init
	for _, v := range s.Equity {
		if prev > 0 {
			ret = append(ret, v/prev-1)
		}
		prev = v
	}
	if len(ret) > 1 {
		mean, sd := meanStd(ret)
		if sd > 0 {
			m.Sharpe = mean / sd * math.Sqrt(daysPerYear)
			m.Volatility = sd * math.Sqrt(daysPerYear) * 100
		}
		var ddSum, ddN float64
		for _, r := range ret {
			if r < 0 {
				ddSum += r * r
				ddN++
			}
		}
		if ddN > 0 {
			dd := math.Sqrt(ddSum / ddN)
			if dd > 0 {
				m.Sortino = mean / dd * math.Sqrt(daysPerYear)
			}
		}
	}

	// trade stats
	ts := s.Trades
	m.NTrades = len(ts)
	if m.NTrades > 0 {
		var wins, losses float64
		var grossWin, grossLoss float64
		var totalBars int
		var consecW, consecL, maxW, maxL int
		for _, t := range ts {
			if t.Pnl > 0 {
				wins++
				grossWin += t.Pnl
				consecW++
				consecL = 0
			} else {
				losses++
				grossLoss += -t.Pnl
				consecL++
				consecW = 0
			}
			if consecW > maxW {
				maxW = consecW
			}
			if consecL > maxL {
				maxL = consecL
			}
			totalBars += t.ExitBar - t.EntryBar
		}
		m.WinRate = wins / float64(m.NTrades) * 100
		m.Expectancy = (grossWin - grossLoss) / float64(m.NTrades)
		m.AvgBarsHeld = float64(totalBars) / float64(m.NTrades)
		m.MaxConsecWins = maxW
		m.MaxConsecLoss = maxL
		if wins > 0 {
			m.AvgWin = grossWin / wins
		}
		if losses > 0 {
			m.AvgLoss = grossLoss / losses
			m.ProfitFactor = grossWin / grossLoss
		} else if grossWin > 0 {
			m.ProfitFactor = math.Inf(1)
		}
		best := ts[0].Pnl
		worst := ts[0].Pnl
		for _, t := range ts {
			if t.Pnl > best {
				best = t.Pnl
			}
			if t.Pnl < worst {
				worst = t.Pnl
			}
		}
		m.BestTrade = best
		m.WorstTrade = worst
	}
	if n > 0 {
		m.ExposurePct = float64(s.Exposure) / float64(n) * 100
	}
	return m
}

func meanStd(xs []float64) (float64, float64) {
	n := len(xs)
	if n == 0 {
		return 0, 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / float64(n)
	var v float64
	for _, x := range xs {
		d := x - mean
		v += d * d
	}
	return mean, math.Sqrt(v / float64(n-1))
}

func f2(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func f4(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	return strconv.FormatFloat(v, 'f', 4, 64)
}

func pct1(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + "%"
}

func pct2(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "—"
	}
	return strconv.FormatFloat(v, 'f', 2, 64) + "%"
}