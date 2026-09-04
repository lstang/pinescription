package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pinego "github.com/woodstock-tokyo/pinescription"
	"github.com/woodstock-tokyo/pinescription/series"
)

// Bar is a single daily OHLCV bar.
type Bar struct {
	Date time.Time
	O, H, L, C, V float64
}

// CSVProvider serves a single symbol's daily bars from a CSV file laid out as
// Date,Open,High,Low,Close,Vol[,Openint].
type CSVProvider struct {
	symbol      string
	bars        []Bar
	smu         sync.Mutex
	seriesCache map[string]pinego.SeriesExtended
}

// LoadCSVProvider reads a daily OHLCV CSV into a provider for symbol.
// Date column may be ISO (YYYY-MM-DD).
func LoadCSVProvider(symbol, path string) (*CSVProvider, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	get := func(rec []string, names ...string) (string, bool) {
		for _, n := range names {
			if i, ok := idx[n]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i]), true
			}
		}
		return "", false
	}
	p := &CSVProvider{symbol: symbol}
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		dateStr, ok := get(rec, "date")
		if !ok || dateStr == "" {
			continue
		}
		dt, err := time.Parse("2006-01-02", dateStr[:10])
		if err != nil {
			continue
		}
		fnum := func(names ...string) float64 {
			v, ok := get(rec, names...)
			if !ok || v == "" {
				return 0
			}
			f, _ := strconv.ParseFloat(v, 64)
			return f
		}
		o := fnum("open")
		h := fnum("high")
		lo := fnum("low")
		c := fnum("close")
		v := fnum("vol", "volume")
		if o == 0 && h == 0 && lo == 0 && c == 0 {
			continue
		}
		p.bars = append(p.bars, Bar{Date: dt, O: o, H: h, L: lo, C: c, V: v})
	}
	if len(p.bars) == 0 {
		return nil, fmt.Errorf("no bars in %s", path)
	}
	return p, nil
}

func (p *CSVProvider) GetSeries(key string) (pinego.SeriesExtended, error) {
	symbol, vt, _ := strings.Cut(key, "|")
	if symbol != p.symbol {
		return nil, fmt.Errorf("symbol %q not available from provider (have %q)", symbol, p.symbol)
	}
	// Serve repeated requests for the same (symbol, value_type) from a cache:
	// engine builtins (vwap, pvt, accdist, ...) call getSeries inside their
	// per-bar accumulation loops, and rebuilding a multi-thousand-bar queue
	// per call made those strategies effectively hang.
	p.smu.Lock()
	if ser, ok := p.seriesCache[vt]; ok {
		p.smu.Unlock()
		return ser, nil
	}
	p.smu.Unlock()
	q := series.NewQueue(len(p.bars) + 1)
	for _, b := range p.bars {
		var v float64
		switch vt {
		case "open":
			v = b.O
		case "high":
			v = b.H
		case "low":
			v = b.L
		case "close":
			v = b.C
		case "volume":
			v = b.V
		default:
			return nil, fmt.Errorf("value type %q not available", vt)
		}
		q.Update(v)
	}
	p.smu.Lock()
	if p.seriesCache == nil {
		p.seriesCache = map[string]pinego.SeriesExtended{}
	}
	p.seriesCache[vt] = q
	p.smu.Unlock()
	return q, nil
}

func (p *CSVProvider) GetSymbols() ([]string, error)     { return []string{p.symbol}, nil }
func (p *CSVProvider) GetValuesTypes() ([]string, error) { return []string{"open", "high", "low", "close", "volume"}, nil }
func (p *CSVProvider) SetTimeframe(tf string) error      { return nil }
func (p *CSVProvider) GetTimeframe() string              { return "1D" }
func (p *CSVProvider) SetSession(s string) error         { return nil }
func (p *CSVProvider) GetSession() string                { return "" }