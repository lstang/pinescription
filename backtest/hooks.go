package main

import (
	"math"
	"strconv"
	"strings"

	pinego "github.com/woodstock-tokyo/pinescription"
)

// Runner provides the per-bar state shared by hooks and the simulator.
type Runner struct {
	sim        *Sim
	currentBar int
	err        error
}

func (r *Runner) setErr(e error) {
	if r.err == nil {
		r.err = e
	}
}

// --- type coercion helpers -------------------------------------------------

func isNA(v interface{}) bool {
	if v == nil {
		return true
	}
	switch t := v.(type) {
	case float64:
		return math.IsNaN(t)
	case int:
		return false
	}
	return false
}

func numArg(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case bool:
		return 0 // bools are not prices
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return math.NaN()
}

func strArg(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	}
	return ""
}

func boolArg(v interface{}, def bool) bool {
	if v == nil {
		return def
	}
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		if math.IsNaN(t) {
			return def
		}
		return t != 0
	case int:
		return t != 0
	case string:
		b, _ := strconv.ParseBool(t)
		return b
	}
	return def
}

func argAt(args []interface{}, i int) interface{} {
	if i < 0 || i >= len(args) {
		return nil
	}
	return args[i]
}

func qtyArg(args []interface{}, i int) float64 {
	v := numArg(argAt(args, i))
	if isNA(argAt(args, i)) {
		return 0
	}
	if math.IsNaN(v) {
		return 0
	}
	return v
}

func dirFromArg(v interface{}) int {
	s := strings.ToLower(strArg(v))
	if s == "long" {
		return 1
	}
	if s == "short" {
		return -1
	}
	if b, ok := v.(bool); ok {
		if b {
			return 1
		}
		return -1
	}
	f := numArg(v)
	if f > 0 {
		return 1
	}
	if f < 0 {
		return -1
	}
	return 0
}

// --- hook registration ------------------------------------------------------

func noopHook(args ...interface{}) (interface{}, error) { return nil, nil }

// buildEngine creates an isolated engine with all strategy/plot/utility hooks
// registered against the runner.
func (r *Runner) buildEngine() (*pinego.Engine, error) {
	e := pinego.NewEngine()

	reg := func(name string, params []string, fn pinego.UserFunction) {
		// Registration failures (reserved names, builtin conflicts) are
		// intentionally ignored: the script will still compile, and any
		// missing hook surfaces as a normal runtime error with a clear message.
		_ = e.RegisterFunctionWithParamNames(name, params, fn)
	}
	noopP := func(params ...string) pinego.UserFunction { return noopHook }

	// plotting / drawing / alerts
	reg("plot", []string{"series", "title", "color", "style", "linewidth", "transp", "offset", "trackprice", "display", "editable", "show_last", "histbase", "join", "format", "precision", "tooltip", "text_font_family"}, noopP())
	reg("plotshape", []string{"series", "title", "style", "location", "color", "offset", "text", "textcolor", "size", "transp", "display", "editable", "show_last", "textalign", "tooltip", "text_font_family", "force_overlay"}, noopP())
	reg("plotchar", []string{"series", "title", "char", "location", "color", "offset", "text", "textcolor", "size", "display", "editable", "show_last", "textalign", "tooltip", "text_font_family", "force_overlay", "transp"}, noopP())
	reg("plotarrow", []string{"series", "title", "color", "colorup", "colordown", "offset", "minheight", "maxheight", "display", "editable", "show_last", "tooltip", "textcolor", "transp"}, noopP())
	reg("plotcandle", []string{"open", "high", "low", "close", "title", "color", "wickcolor", "bodycolor", "bordercolor", "editable", "show_last", "transp", "force_overlay"}, noopP())
	reg("plotbar", []string{"open", "high", "low", "close", "title", "color", "editable", "show_last"}, noopP())
	reg("label.set_xy", []string{"id", "x", "y", "xloc"}, noopP())
	reg("label.set_x", []string{"id", "x", "xloc"}, noopP())
	reg("label.set_y", []string{"id", "y"}, noopP())
	reg("label.set_yloc", []string{"id", "yloc"}, noopP())
	reg("label.set_xloc", []string{"id", "xloc"}, noopP())
	reg("label.set_color", []string{"id", "color"}, noopP())
	reg("label.set_textcolor", []string{"id", "color"}, noopP())
	reg("label.set_size", []string{"id", "size"}, noopP())
	reg("label.set_style", []string{"id", "style"}, noopP())
	reg("label.set_tooltip", []string{"id", "tooltip"}, noopP())
	reg("line.set_x2", []string{"id", "x", "xloc"}, noopP())
	reg("line.set_x1", []string{"id", "x", "xloc"}, noopP())
	reg("line.set_y1", []string{"id", "y"}, noopP())
	reg("line.set_y2", []string{"id", "y"}, noopP())
	reg("line.set_extend", []string{"id", "extend"}, noopP())
	reg("line.set_width", []string{"id", "width"}, noopP())
	reg("line.set_style", []string{"id", "style"}, noopP())
	reg("box.set_left", []string{"id", "left"}, noopP())
	reg("box.set_right", []string{"id", "right"}, noopP())
	reg("box.set_top", []string{"id", "top"}, noopP())
	reg("box.set_bottom", []string{"id", "bottom"}, noopP())
	reg("box.set_border_color", []string{"id", "color"}, noopP())
	reg("box.set_bgcolor", []string{"id", "color"}, noopP())
	// hline is preprocessed away (replaced with na) before compile, so no hook
	// is needed; alertcondition/alert are engine builtins already.
	reg("heikinashi", []string{"symbol"}, func(args ...interface{}) (interface{}, error) {
		// heikinashi(syminfo.tickerid) returns a tuple of OHLC; the engine has
		// no tuple for it, so approximate with a float map-like value is not
		// possible. Return close as a single-value approximation is wrong for
		// tuple unpacking; instead register noop that returns current close.
		return lastClose(r.sim), nil
	})
	// fill accepts both the v4 positional (h1, h2, color, ...) and the v5
	// named plot1=/plot2=/top_color= forms; the handler is a noop so the extra
	// names only need to be bound, never interpreted.
	reg("fill", []string{"h1", "h2", "color", "title", "transp", "display", "fillgaps", "editable", "show_last", "top_color", "bottom_color", "plot1", "plot2"}, noopP())
	reg("bgcolor", []string{"color", "title", "transp", "editable", "display", "offset"}, noopP())

	// cross-symbol / multi-timeframe approximations
	secHook := func(args ...interface{}) (interface{}, error) {
		// symbol, timeframe, expression, ... -> return the current-bar expression
		// value (no true timeframe aggregation)
		if len(args) >= 3 {
			return args[2], nil
		}
		return nil, nil
	}
	reg("security", []string{"symbol", "timeframe", "expression", "gaps", "lookahead", "ignore_invalid_symbol"}, secHook)
	reg("request.security", []string{"symbol", "timeframe", "expression", "gaps", "lookahead", "ignore_invalid_symbol"}, secHook)
	reg("request.security_lower_tf", []string{"symbol", "timeframe", "expression", "gaps"}, secHook)
	reg("tickerid", []string{"prefix", "ticker"}, func(args ...interface{}) (interface{}, error) {
		if len(args) >= 2 {
			return strArg(args[0]) + ":" + strArg(args[1]), nil
		}
		return "SYM", nil
	})
	reg("syminfo.tickerid", nil, func(args ...interface{}) (interface{}, error) { return "SYM", nil })

	// strategy declaration
	declParams := []string{
		"title", "shorttitle", "overlay", "format", "precision", "scale",
		"default_qty_type", "default_qty_value", "initial_capital", "currency",
		"process_orders_on_close", "commission_type", "commission_value",
		"slippage", "calc_on_every_tick", "max_bars_back", "pyramiding",
		"close_entries_rule", "explicit_plot_zorder", "default_price_type",
		"fill_orders_on_standard_bar", "backtest_fill_limits_assumption",
		"margin_long", "margin_short", "calc_on_order_fills",
		"max_lines_count", "max_labels_count", "max_boxes_count",
		"max_orders_count", "max_entries_count", "use_bar_magnifier",
		"linktoseries", "fill_orders_on_standard_ohlc", "max_bars_back2",
		"behind_chart", "default_qty_type2",
	}
	reg("strategy", declParams, func(args ...interface{}) (interface{}, error) {
		if r.currentBar != 0 {
			return nil, nil
		}
		d := &r.sim.Decl
		overlay := boolArg(argAt(args, 2), true)
		d.Overlay = overlay
		if q := numArg(argAt(args, 6)); !math.IsNaN(q) {
			switch int(q) {
			case 1:
				d.QtyType = "percent_of_equity"
			case 0:
				d.QtyType = "fixed"
			case 2:
				d.QtyType = "cash"
			}
		}
		if v := numArg(argAt(args, 7)); !math.IsNaN(v) {
			d.QtyValue = v
		}
		if v := numArg(argAt(args, 8)); !math.IsNaN(v) && v > 0 {
			d.InitialCapital = v
		}
		d.ProcessOrdersOnClose = boolArg(argAt(args, 10), false)
		if p := numArg(argAt(args, 16)); !math.IsNaN(p) {
			d.Pyramiding = int(p)
		}
		if c := numArg(argAt(args, 11)); !math.IsNaN(c) {
			switch int(c) {
			case 1:
				d.CommissionType = "percent"
			case 2:
				d.CommissionType = "cash_per_contract"
			case 3:
				d.CommissionType = "cash_per_order"
			}
		}
		if v := numArg(argAt(args, 12)); !math.IsNaN(v) {
			d.CommissionValue = v
		}
		return nil, nil
	})
	reg("study", []string{"title", "shorttitle", "overlay", "format", "precision", "scale", "max_bars_back", "resolution", "resolution_gaps", "explicit_plot_zorder", "max_labels_count", "max_lines_count", "dynamic_requests", "timeframe", "timeframe_gaps"}, noopP())
	reg("indicator", []string{"title", "shorttitle", "overlay", "format", "precision", "scale", "max_bars_back", "resolution", "resolution_gaps", "explicit_plot_zorder", "max_labels_count", "max_lines_count", "dynamic_requests", "timeframe", "timeframe_gaps"}, noopP())

	// strategy order hooks. v3 scripts use the "long"/"short" named or
	// positional bool argument for direction instead of the v4+ strategy.long
	// constant; the bindNamedCallArgs machinery maps long=true onto the
	// "direction" slot when it is passed positionally.
	entryParams := []string{"id", "direction", "long", "short", "qty", "limit", "stop", "oca_name", "oca_type", "when", "comment", "alert_message", "disable_alert"}
	reg("strategy.entry", entryParams, func(args ...interface{}) (interface{}, error) {
		dir := dirFromArg(argAt(args, 1))
		// v3 style: long=true / short=true named arguments carry direction
		if dir == 0 {
			if boolArg(argAt(args, 2), false) {
				dir = 1
			} else if boolArg(argAt(args, 3), false) {
				dir = -1
			}
		}
		it := Intent{
			Bar:  r.currentBar,
			Kind: "entry",
			ID:   strArg(argAt(args, 0)),
			Dir:  dir,
			Qty:  qtyArg(args, 4),
		}
		it.Limit = numArg(argAt(args, 5))
		it.Stop = numArg(argAt(args, 6))
		it.OCA = strArg(argAt(args, 7))
		it.When = boolArg(argAt(args, 9), true)
		it.Comment = strArg(argAt(args, 10))
		if math.IsNaN(it.Limit) {
			it.Limit = 0
		}
		if math.IsNaN(it.Stop) {
			it.Stop = 0
		}
		r.sim.AddIntent(it)
		return nil, nil
	})
	reg("strategy.order", entryParams, func(args ...interface{}) (interface{}, error) {
		it := Intent{
			Bar: r.currentBar, Kind: "order",
			ID: strArg(argAt(args, 0)), Dir: dirFromArg(argAt(args, 1)), Qty: qtyArg(args, 2),
		}
		it.Limit = numArg(argAt(args, 3))
		it.Stop = numArg(argAt(args, 4))
		it.OCA = strArg(argAt(args, 5))
		it.When = boolArg(argAt(args, 7), true)
		it.Comment = strArg(argAt(args, 8))
		if math.IsNaN(it.Limit) {
			it.Limit = 0
		}
		if math.IsNaN(it.Stop) {
			it.Stop = 0
		}
		r.sim.AddIntent(it)
		return nil, nil
	})
	exitParams := []string{"id", "from_entry", "qty", "qty_percent", "loss", "profit", "limit", "stop", "trail_price", "trail_offset", "trail_points", "oca_name", "oca_type", "when", "comment", "alert_message", "comment_profit", "comment_loss", "comment_trailing", "alert_profit", "alert_loss", "alert_trailing", "disable_alert"}
	reg("strategy.exit", exitParams, func(args ...interface{}) (interface{}, error) {
		it := Intent{
			Bar: r.currentBar, Kind: "exit",
			ID:      strArg(argAt(args, 0)),
			EntryID: strArg(argAt(args, 1)),
			Qty:     qtyArg(args, 2),
		}
		it.Loss = numArg(argAt(args, 4))
		it.Profit = numArg(argAt(args, 5))
		it.Limit = numArg(argAt(args, 6))
		it.Stop = numArg(argAt(args, 7))
		it.TrailPts = numArg(argAt(args, 10))
		it.TrailOff = numArg(argAt(args, 9))
		it.When = boolArg(argAt(args, 13), true)
		it.Comment = strArg(argAt(args, 14))
		for _, v := range []*float64{&it.Loss, &it.Profit, &it.Limit, &it.Stop, &it.TrailPts, &it.TrailOff} {
			if math.IsNaN(*v) {
				*v = 0
			}
		}
		if math.Abs(it.Loss) < 1e-12 && math.Abs(it.Profit) < 1e-12 &&
			math.Abs(it.Limit) < 1e-12 && math.Abs(it.Stop) < 1e-12 &&
			math.Abs(it.TrailPts) < 1e-12 && math.Abs(it.TrailOff) < 1e-12 {
			// pure market exit (no stop/profit levels)
			if it.When {
				r.sim.AddIntent(it)
			}
			return nil, nil
		}
		if it.When {
			r.sim.AddIntent(it)
		}
		return nil, nil
	})
	reg("strategy.close", []string{"id", "comment", "qty", "when", "alert_message", "disable_alert", "qty_percent", "immediately"}, func(args ...interface{}) (interface{}, error) {
		it := Intent{Bar: r.currentBar, Kind: "close", ID: strArg(argAt(args, 0))}
		if boolArg(argAt(args, 3), true) {
			r.sim.AddIntent(it)
		}
		return nil, nil
	})
	reg("strategy.close_all", []string{"comment", "when", "alert_message", "disable_alert"}, func(args ...interface{}) (interface{}, error) {
		// v2/v3: strategy.close_all(comment_or_when)
		when := true
		if v, ok := argAt(args, 1).(bool); ok {
			when = v
		}
		if it := argAt(args, 0); it != nil {
			if b, ok := it.(bool); ok {
				when = b
			}
		}
		if when {
			r.sim.AddIntent(Intent{Bar: r.currentBar, Kind: "close_all"})
		}
		return nil, nil
	})
	reg("strategy.cancel", []string{"id", "when", "comment"}, func(args ...interface{}) (interface{}, error) {
		if boolArg(argAt(args, 1), true) {
			r.sim.AddIntent(Intent{Bar: r.currentBar, Kind: "cancel", ID: strArg(argAt(args, 0))})
		}
		return nil, nil
	})
	reg("strategy.cancel_all", []string{"when", "comment"}, func(args ...interface{}) (interface{}, error) {
		if boolArg(argAt(args, 0), true) {
			r.sim.AddIntent(Intent{Bar: r.currentBar, Kind: "cancel_all"})
		}
		return nil, nil
	})

	// risk helpers: allow_entry_in / max_intraday_filled_orders are no-ops that
	// simply accept their argument.
	reg("strategy.risk.allow_entry_in", []string{"direction"}, func(args ...interface{}) (interface{}, error) {
		return nil, nil
	})
	reg("strategy.risk.max_position_size", []string{"value"}, func(args ...interface{}) (interface{}, error) {
		return nil, nil
	})
	reg("strategy.risk.max_intraday_filled_orders", []string{"n"}, func(args ...interface{}) (interface{}, error) {
		return nil, nil
	})
	reg("strategy.risk.allow_entries_in_direction", []string{"direction"}, func(args ...interface{}) (interface{}, error) {
		return nil, nil
	})

	// strategy value hooks
	fnum := func(v interface{}) (interface{}, error) {
		f, ok := v.(float64)
		if !ok {
			return 0.0, nil
		}
		if math.IsNaN(f) {
			return 0.0, nil
		}
		return f, nil
	}
	reg("strategy.position_size", nil, func(args ...interface{}) (interface{}, error) {
		return fnum(r.sim.PositionSize())
	})
	reg("strategy.position_avg_price", nil, func(args ...interface{}) (interface{}, error) {
		return fnum(r.sim.PositionAvg())
	})
	reg("strategy.position_entry_name", nil, func(args ...interface{}) (interface{}, error) {
		// name of the last opened entry id
		name := ""
		for id := range r.sim.positions {
			name = id
		}
		return name, nil
	})
	reg("strategy.open_trades", nil, func(args ...interface{}) (interface{}, error) {
		n := 0
		for _, p := range r.sim.positions {
			if p.Qty != 0 {
				n++
			}
		}
		return fnum(float64(n))
	})
	reg("strategy.opentrades", nil, func(args ...interface{}) (interface{}, error) {
		n := 0
		for _, p := range r.sim.positions {
			if p.Qty != 0 {
				n++
			}
		}
		return fnum(float64(n))
	})
	reg("ta.vwap", []string{"source"}, func(args ...interface{}) (interface{}, error) {
		// cumulative VWAP over the bars seen so far:
		// sum(typical*volume)/sum(volume)
		var pv, v float64
		end := r.currentBar
		if end >= len(r.sim.Bars) {
			end = len(r.sim.Bars) - 1
		}
		for i := 0; i <= end; i++ {
			b := r.sim.Bars[i]
			typ := (b.H + b.L + b.C) / 3
			pv += typ * b.V
			v += b.V
		}
		if v <= 0 {
			return math.NaN(), nil
		}
		return pv / v, nil
	})
	reg("ta.obv", []string{"source"}, func(args ...interface{}) (interface{}, error) {
		// On-Balance Volume: cumulative volume signed by close direction.
		end := r.currentBar
		if end >= len(r.sim.Bars) {
			end = len(r.sim.Bars) - 1
		}
		var obv float64
		for i := 1; i <= end; i++ {
			b := r.sim.Bars[i]
			prev := r.sim.Bars[i-1]
			if b.C > prev.C {
				obv += b.V
			} else if b.C < prev.C {
				obv -= b.V
			}
		}
		return obv, nil
	})
	reg("strategy.closedtrades", nil, func(args ...interface{}) (interface{}, error) {
		return fnum(float64(len(r.sim.Trades)))
	})
	reg("strategy.wintrades", nil, func(args ...interface{}) (interface{}, error) {
		n := 0
		for _, t := range r.sim.Trades {
			if t.Pnl > 0 {
				n++
			}
		}
		return fnum(float64(n))
	})
	reg("strategy.losstrades", nil, func(args ...interface{}) (interface{}, error) {
		n := 0
		for _, t := range r.sim.Trades {
			if t.Pnl < 0 {
				n++
			}
		}
		return fnum(float64(n))
	})
	reg("strategy.netprofit", nil, func(args ...interface{}) (interface{}, error) {
		return fnum(r.sim.EquityNow(lastClose(r.sim)) - initialCap(r.sim))
	})
	reg("strategy.grossprofit", nil, func(args ...interface{}) (interface{}, error) {
		var g float64
		for _, t := range r.sim.Trades {
			if t.Pnl > 0 {
				g += t.Pnl
			}
		}
		return fnum(g)
	})
	reg("strategy.grossloss", nil, func(args ...interface{}) (interface{}, error) {
		var g float64
		for _, t := range r.sim.Trades {
			if t.Pnl < 0 {
				g += -t.Pnl
			}
		}
		return fnum(g)
	})
	reg("strategy.openprofit", nil, func(args ...interface{}) (interface{}, error) {
		q := r.sim.PositionSize()
		if math.Abs(q) < 1e-12 {
			return 0.0, nil
		}
		return fnum(q * (lastClose(r.sim) - r.sim.PositionAvg()))
	})
	reg("strategy.equity", nil, func(args ...interface{}) (interface{}, error) {
		return fnum(r.sim.EquityNow(lastClose(r.sim)))
	})
	reg("strategy.cash", nil, func(args ...interface{}) (interface{}, error) {
		return fnum(r.sim.cash)
	})
	reg("strategy.max_drawdown", nil, func(args ...interface{}) (interface{}, error) {
		return fnum(maxDrawdown(r.sim.Equity, initialCap(r.sim)))
	})

	return e, nil
}

func initialCap(s *Sim) float64 {
	c := s.Decl.InitialCapital
	if c <= 0 {
		return 10000
	}
	return c
}

func lastClose(s *Sim) float64 {
	if len(s.Bars) == 0 {
		return 0
	}
	return s.Bars[len(s.Bars)-1].C
}

func maxDrawdown(eq []float64, init float64) float64 {
	peak := init
	md := 0.0
	for _, v := range eq {
		if v > peak {
			peak = v
		}
		if peak > 0 && v < peak {
			dd := (peak - v) / peak
			if dd > md {
				md = dd
			}
		}
	}
	return md
}