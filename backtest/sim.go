package main

import (
	"math"
	"sort"
)

// DeclConfig captures the strategy() declaration parameters that drive the
// backtest simulation.
type DeclConfig struct {
	Overlay              bool
	QtyType              string // "percent_of_equity", "fixed", "cash", ""
	QtyValue             float64
	InitialCapital       float64
	ProcessOrdersOnClose bool
	Pyramiding           int
	CommissionType       string // "percent", "cash_per_contract", "cash_per_order", ""
	CommissionValue      float64
	Slippage             float64
}

func defDecl() DeclConfig {
	return DeclConfig{
		QtyType:        "percent_of_equity",
		QtyValue:       100,
		InitialCapital: 10000,
		// Realistic default costs. The script's strategy() declaration
		// overrides these when it declares commission/slippage itself.
		CommissionType: "percent",
		CommissionValue: defaultCommissionPct,
		Slippage:        defaultSlippageBps / 10000.0,
	}
}

// Realistic retail-ish trading costs applied when the strategy does not
// declare its own: 0.05% commission per fill (percent of notional) and
// 2 basis points of slippage per fill.
const (
	defaultCommissionPct = 0.05
	defaultSlippageBps   = 2.0
)

// applySlippage shifts a fill price against the trader: buys fill higher,
// sells fill lower, by Decl.Slippage (fraction of price).
func (s *Sim) applySlippage(price float64, buying bool) float64 {
	if s.Decl.Slippage <= 0 || price <= 0 {
		return price
	}
	if buying {
		return price * (1 + s.Decl.Slippage)
	}
	return price * (1 - s.Decl.Slippage)
}

// Intent is an order request captured from a strategy.* call during a bar's
// evaluation. It is queued at the end of that bar and filled either at the
// next bar's open (market) or intrabar via stop/limit levels (conditional).
type Intent struct {
	Bar       int
	Kind      string // entry, order, exit, close, close_all, cancel, cancel_all
	ID        string
	EntryID   string // for strategy.exit: which entry to apply to
	Dir       int    // +1 long, -1 short (entries)
	Qty       float64
	Limit     float64
	Stop      float64
	Profit    float64 // price distance
	Loss      float64 // price distance
	TrailPts  float64
	TrailOff  float64
	OCA       string
	When      bool
	Comment   string
	Seq       int
}

// CondOrder is a resting conditional order evaluated intrabar.
type CondOrder struct {
	Seq       int
	Kind      string // entry_stop, entry_limit, exit
	ID        string
	EntryID   string
	Dir       int
	Qty       float64
	Stop      float64
	Limit     float64
	Profit    float64
	Loss      float64
	TrailPts  float64
	TrailOff  float64
	OCA       string
	FromBar   int
	Armed     bool // exit order has seen its entry position at least once
}

// Position tracks one entry id's open quantity.
type Position struct {
	ID   string
	Qty  float64 // signed
	Avg  float64
	FromBar int
	TrailHi float64
	TrailLo float64
}

// Trade is a closed (portion of a) position.
type Trade struct {
	EntryID   string
	Qty       float64
	Dir       int
	EntryPrice float64
	ExitPrice float64
	Pnl       float64
	EntryBar  int
	ExitBar   int
	Reason    string
}

// Signal is any order/exit intent captured (for reporting).
type Signal struct {
	Bar     int
	ID      string
	Kind    string
	Dir     int
	Qty     float64
	Price   float64
	Comment string
}

// Sim is the bar-by-bar strategy simulator.
type Sim struct {
	Bars  []Bar
	Decl  DeclConfig

	positions map[string]*Position
	seq       int

	// pending market orders filled at next bar open
	pendingMarket []Intent
	// resting conditional orders evaluated intrabar
	pendingCond []CondOrder

	cash  float64
	eqPrev float64 // equity marked at prior close (for % sizing)

	Equity  []float64 // marked at each bar close
	Trades  []Trade
	Signals []Signal
	Exposure int // bars with a position
	lastQty  float64
}

func NewSim(bars []Bar, decl DeclConfig) *Sim {
	init := decl.InitialCapital
	if init <= 0 {
		init = 10000
	}
	return &Sim{
		Bars:      bars,
		Decl:      decl,
		positions: map[string]*Position{},
		cash:      init,
		eqPrev:    init,
		Equity:    make([]float64, len(bars)),
	}
}

// PositionSize is the aggregate signed position size.
func (s *Sim) PositionSize() float64 {
	var q float64
	for _, p := range s.positions {
		q += p.Qty
	}
	return q
}

// PositionAvg is the volume-weighted average price of the aggregate position.
func (s *Sim) PositionAvg() float64 {
	var q, pq float64
	for _, p := range s.positions {
		q += p.Qty
		pq += p.Qty * p.Avg
	}
	if math.Abs(q) < 1e-12 {
		return 0
	}
	return pq / q
}

// EquityNow marks equity at the given price.
func (s *Sim) EquityNow(price float64) float64 {
	return s.cash + s.PositionSize()*price
}

// AddIntent is invoked by the strategy.* hooks during the current bar's eval.
func (s *Sim) AddIntent(it Intent) {
	s.seq++
	it.Seq = s.seq
	s.Signals = append(s.Signals, Signal{
		Bar: it.Bar, ID: it.ID, Kind: it.Kind, Dir: it.Dir, Qty: it.Qty, Comment: it.Comment,
	})
	switch it.Kind {
	case "cancel":
		s.cancelOrders(it.ID)
		return
	case "cancel_all":
		s.pendingMarket = nil
		s.pendingCond = nil
		return
	}
	if it.Kind == "close_all" {
		// close entire position at next open
		q := s.PositionSize()
		if math.Abs(q) < 1e-12 {
			return
		}
		s.pendingMarket = append(s.pendingMarket, Intent{Bar: it.Bar, Kind: "exit", ID: "__close_all", Qty: q, When: true})
		return
	}
	if it.Kind == "close" {
		q := s.qtyForEntry(it.ID)
		if math.Abs(q) < 1e-12 {
			return
		}
		s.pendingMarket = append(s.pendingMarket, Intent{Bar: it.Bar, Kind: "exit", ID: "__close_"+it.ID, EntryID: it.ID, Qty: q, When: true})
		return
	}
	if it.Kind == "exit" {
		if s.declareCond(it) {
			return
		}
		// plain market exit
		s.pendingMarket = append(s.pendingMarket, it)
		return
	}
	// entries / orders
	if it.Kind != "entry" && it.Kind != "order" {
		return
	}
	// a new entry with the same id replaces any pending order with that id
	s.pendingMarket = dropPending(s.pendingMarket, it.ID)
	s.pendingCond = dropCond(s.pendingCond, it.ID)
	if it.Stop > 0 {
		s.pendingCond = append(s.pendingCond, CondOrder{Seq: it.Seq, Kind: "entry_stop", ID: it.ID, Dir: it.Dir, Qty: it.Qty, Stop: it.Stop, OCA: it.OCA, FromBar: it.Bar})
		return
	}
	if it.Limit > 0 {
		s.pendingCond = append(s.pendingCond, CondOrder{Seq: it.Seq, Kind: "entry_limit", ID: it.ID, Dir: it.Dir, Qty: it.Qty, Limit: it.Limit, OCA: it.OCA, FromBar: it.Bar})
		return
	}
	s.pendingMarket = append(s.pendingMarket, it)
}

func dropPending(in []Intent, id string) []Intent {
	out := in[:0]
	for _, it := range in {
		if it.ID == id {
			continue
		}
		out = append(out, it)
	}
	return out
}

func dropCond(in []CondOrder, id string) []CondOrder {
	out := in[:0]
	for _, c := range in {
		if c.ID == id {
			continue
		}
		out = append(out, c)
	}
	return out
}

func (s *Sim) cancelOrders(id string) {
	s.pendingMarket = dropPending(s.pendingMarket, id)
	s.pendingCond = dropCond(s.pendingCond, id)
}

// qtyForEntry returns the open quantity attributable to an entry id
// (aggregate if the id is unknown/empty).
func (s *Sim) qtyForEntry(id string) float64 {
	if p, ok := s.positions[id]; ok {
		return -p.Qty // negative of signed -> amount to close
	}
	return -s.PositionSize()
}

// declareCond converts a strategy.exit intent into a resting conditional order.
// The order is queued even when the referenced entry has not filled yet (the
// common same-bar strategy.entry + strategy.exit pattern): it arms once the
// position exists. Qty is resolved at arming time when not specified.
func (s *Sim) declareCond(it Intent) bool {
	hasSL := it.Stop > 0 || it.Loss > 0
	hasTP := it.Limit > 0 || it.Profit > 0
	hasTrail := it.TrailPts > 0 || it.TrailOff > 0
	if !hasSL && !hasTP && !hasTrail {
		return false // plain market exit handled by caller
	}
	// Re-declaring an exit with the same id modifies the resting order
	// (TradingView semantics). Without the dedup, stale stops from earlier
	// bars accumulate and the loosest (often outdated) level governs exits.
	s.pendingCond = dropCond(s.pendingCond, it.ID)
	s.pendingCond = append(s.pendingCond, CondOrder{
		Seq: it.Seq, Kind: "exit", ID: it.ID, EntryID: it.EntryID,
		Qty: it.Qty, Stop: it.Stop, Limit: it.Limit, Profit: it.Profit, Loss: it.Loss,
		TrailPts: it.TrailPts, TrailOff: it.TrailOff, FromBar: it.Bar,
	})
	return true
}

// Step processes bar b: fills market orders at the open, evaluates conditional
// orders intrabar, queues intents captured during bar b's evaluation, and
// marks equity at the close.
func (s *Sim) Step(b int) {
	s.fillMarketOrders(b)
	s.processConditionals(b)
	if len(s.Bars) == 0 {
		return
	}
	s.Equity[b] = s.EquityNow(s.Bars[b].C)
	s.eqPrev = s.Equity[b]
	if math.Abs(s.PositionSize()) > 1e-12 {
		s.Exposure++
	}
	s.lastQty = s.PositionSize()
}

func (s *Sim) fillPrice(openPrice, closePrice float64) float64 {
	if s.Decl.ProcessOrdersOnClose {
		return closePrice
	}
	return openPrice
}

func negateDir(d float64) int {
	if d > 0 {
		return 1
	}
	if d < 0 {
		return -1
	}
	return 0
}

// fillMarketOrders fills queued market orders at the current bar. Orders
// submitted during bar b's script evaluation are deferred to bar b+1: the
// signal is derived from bar b's close, so filling on bar b's own open is
// same-bar look-ahead bias that massively inflates backtest returns.
func (s *Sim) fillMarketOrders(b int) {
	if len(s.pendingMarket) == 0 {
		return
	}
	// Split the queue: only orders from earlier bars may fill on this bar.
	var ready, deferred []Intent
	for _, it := range s.pendingMarket {
		if it.Bar < b {
			ready = append(ready, it)
		} else {
			deferred = append(deferred, it)
		}
	}
	s.pendingMarket = deferred
	if len(ready) == 0 {
		return
	}
	bar := s.Bars[b]
	closed := s.Decl.ProcessOrdersOnClose
	var fillPrice float64
	if closed && b > 0 {
		fillPrice = s.Bars[b-1].C // previous close (orders submitted on b-1)
	} else {
		fillPrice = bar.O
	}
	orders := ready
	for _, it := range orders {
		if !it.When {
			continue
		}
		if it.Kind == "exit" || it.Kind == "close" {
			// close matching entry portion or whole position
			s.executeExit(it, fillPrice, b, "market")
			continue
		}
		// entry
		dir := it.Dir
		if dir == 0 {
			dir = signOf(s.PositionSize())
		}
		if dir == 0 {
			dir = 1
		}
		s.executeEntry(it, dir, fillPrice, b)
	}
}

// sameDirEntryCount counts open entry positions signed in `dir`.
func (s *Sim) sameDirEntryCount(dir int) int {
	n := 0
	for _, p := range s.positions {
		if math.Abs(p.Qty) > 1e-12 && signOf(p.Qty) == dir {
			n++
		}
	}
	return n
}

func (s *Sim) executeEntry(it Intent, dir int, price float64, b int) {
	if price <= 0 {
		return
	}
	cur := signOf(s.PositionSize())
	if it.Kind == "order" {
		// strategy.order keeps the legacy guard: without pyramiding declared,
		// same-direction orders are ignored and opposite orders reverse.
		if s.Decl.Pyramiding <= 0 && cur != 0 {
			if dir == cur {
				return
			}
			s.closeAllAt(price, b, "reverse")
		}
	} else {
		// strategy.entry TV semantics:
		//  - an entry whose ID matches an already-open entry position is ignored;
		//  - same-direction adds are capped by `pyramiding` (0 or 1 => one entry);
		//  - an opposite-direction entry reverses: close existing, open new.
		if cur != 0 {
			if dir == cur {
				if p := s.positions[it.ID]; p != nil && signOf(p.Qty) == dir {
					return
				}
				limit := s.Decl.Pyramiding
				if limit < 1 {
					limit = 1
				}
				if s.sameDirEntryCount(dir) >= limit {
					return
				}
			} else {
				s.closeAllAt(price, b, "reverse")
			}
		}
	}
	qty := it.Qty
	if qty <= 0 {
		qty = s.defaultQty(dir, price)
	}
	if qty <= 0 {
		return
	}
	fee := s.fee(qty, price)
	s.applyFill(it.ID, dir, qty, price, b)
	s.cash -= fee
	if it.OCA != "" {
		s.cancelOCA(it.OCA)
	}
}

func (s *Sim) defaultQty(dir int, price float64) float64 {
	d := s.Decl
	switch d.QtyType {
	case "fixed":
		v := d.QtyValue
		if v <= 0 {
			v = 1
		}
		return v
	case "cash":
		if price <= 0 {
			return 0
		}
		return d.QtyValue / price
	default: // percent_of_equity
		if price <= 0 {
			return 0
		}
		pct := d.QtyValue
		if pct <= 0 {
			pct = 100
		}
		return s.eqPrev * pct / 100.0 / price
	}
}

func (s *Sim) applyFill(id string, dir int, qty float64, price float64, b int) {
	// Slippage: every fill shifts against the trader — buys fill higher,
	// sells fill lower. Applied at the entry choke point so entries,
	// reversals and adds all see the slipped price.
	price = s.applySlippage(price, dir > 0)
	existing := s.positions[id]
	if existing == nil {
		existing = &Position{ID: id, FromBar: b}
		s.positions[id] = existing
	}
	old := existing.Qty
	newQty := old + float64(dir)*qty
	if math.Abs(newQty) < 1e-12 {
		delete(s.positions, id)
		return
	}
	// weighted average (for adding) or keep avg when reducing
	if math.Abs(old) < 1e-12 {
		existing.Avg = price
	} else if signOf(old) == signOf(newQty) {
		existing.Avg = (math.Abs(old)*existing.Avg + qty*price) / math.Abs(newQty)
	}
	existing.Qty = newQty
	if existing.TrailHi == 0 || price > existing.TrailHi {
		existing.TrailHi = price
	}
	if existing.TrailLo == 0 || price < existing.TrailLo {
		existing.TrailLo = price
	}
	// cash accounting
	if dir > 0 {
		s.cash -= qty * price
	} else {
		s.cash += qty * price
	}
}

func (s *Sim) fee(qty, price float64) float64 {
	d := s.Decl
	switch d.CommissionType {
	case "cash_per_contract":
		return math.Abs(qty) * d.CommissionValue
	case "cash_per_order":
		return d.CommissionValue
	default: // percent
		return math.Abs(qty) * price * d.CommissionValue / 100.0
	}
}

func (s *Sim) closeAllAt(price float64, b int, reason string) {
	for id, p := range s.positions {
		q := p.Qty
		if math.Abs(q) < 1e-12 {
			continue
		}
		s.realize(id, q, price, b, reason)
	}
	s.positions = map[string]*Position{}
}// executeExit closes the requested portion. EntryID "" closes the whole
// position (per-pyramiding rules may cap).
func (s *Sim) executeExit(it Intent, price float64, b int, reason string) {
	if it.ID == "__close_all" {
		s.closeAllAt(price, b, reason)
		return
	}
	if it.EntryID != "" {
		if p, ok := s.positions[it.EntryID]; ok && math.Abs(p.Qty) > 1e-12 {
			q := it.Qty
			if q == 0 || q > math.Abs(p.Qty) {
				q = math.Abs(p.Qty)
			}
			s.realize(it.EntryID, -float64(signOf(p.Qty))*q, price, b, reason)
		}
		return
	}
	// close whole position
	q := math.Abs(s.PositionSize())
	if q > 1e-12 {
		if it.Qty > 0 && it.Qty < q {
			s.realizeFirst(it.Qty, price, b, reason)
		} else {
			s.closeAllAt(price, b, reason)
		}
	}
}

// realize closes `qty` (signed amount to close) of entry id and records a trade.
func (s *Sim) realize(id string, closeQty float64, price float64, b int, reason string) {
	p := s.positions[id]
	if p == nil {
		return
	}
	dir := signOf(p.Qty)
	cl := math.Abs(closeQty)
	if cl > math.Abs(p.Qty) {
		cl = math.Abs(p.Qty)
	}
	// Slippage: closing a long is a sell (fill lower), closing a short is
	// a buy (fill higher). Applied at the exit choke point so market exits,
	// stop/limit fills, reversals and close_all all see the slipped price.
	price = s.applySlippage(price, dir < 0)
	pnl := float64(dir) * (price - p.Avg) * cl
	t := Trade{
		EntryID: id, Qty: cl, Dir: dir, EntryPrice: p.Avg, ExitPrice: price,
		Pnl: pnl, EntryBar: p.FromBar, ExitBar: b, Reason: reason,
	}
	s.Trades = append(s.Trades, t)
	// cash: closing cash flow already accounted via sign: closing long sells:
	if dir > 0 {
		s.cash += cl * price
	} else {
		s.cash -= cl * price
	}
	// reduce position
	p.Qty -= float64(dir) * cl // for long: qty - cl; for short: qty + cl
	if math.Abs(p.Qty) < 1e-12 {
		delete(s.positions, id)
	}
}

// realizeFirst closes from the earliest entry first (FIFO by id order).
func (s *Sim) realizeFirst(qty float64, price float64, b int, reason string) {
	ids := make([]string, 0, len(s.positions))
	for id := range s.positions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rem := qty
	for _, id := range ids {
		p := s.positions[id]
		if p == nil || math.Abs(p.Qty) < 1e-12 {
			continue
		}
		cl := math.Min(rem, math.Abs(p.Qty))
		s.realize(id, -float64(signOf(p.Qty))*cl, price, b, reason)
		rem -= cl
		if rem <= 0 {
			return
		}
	}
}

// processConditionals evaluates resting conditional orders against bar b's
// range and updates trailing stops. Conditional orders submitted during bar
// b's own evaluation only become active on bar b+1 (same anti-look-ahead rule
// as market orders).
func (s *Sim) processConditionals(b int) {
	if b < 0 || b >= len(s.Bars) {
		return
	}
	bar := s.Bars[b]

	// 0. Split the resting orders: those submitted during this bar's own
	// evaluation are not active yet and stay pending for the next bar.
	deferred := []CondOrder{}
	keep := []CondOrder{} // exit orders that remain resting after this bar
	orderCond := []CondOrder{}
	braces := map[string]bracket{}
	trailSeen := map[string]int{} // position id -> direction of trailing exit
	for _, c := range s.pendingCond {
		if c.FromBar >= b {
			deferred = append(deferred, c)
			continue
		}
		if c.Kind != "exit" {
			orderCond = append(orderCond, c)
			continue
		}
		// Exit order: resolve the position(s) it applies to. An empty
		// from_entry applies to every open position (TradingView semantics);
		// it was previously dropped entirely, silently disarming stops.
		targets := []string{}
		if c.EntryID != "" {
			if p, ok := s.positions[c.EntryID]; ok && math.Abs(p.Qty) > 1e-12 {
				targets = append(targets, c.EntryID)
			}
		} else {
			for id, p := range s.positions {
				if math.Abs(p.Qty) > 1e-12 {
					targets = append(targets, id)
				}
			}
			sort.Strings(targets)
		}
		if len(targets) == 0 {
			// Nothing to attach to. If the order armed before, its entry is
			// gone — drop it (TV cancels exits when their entries close).
			// Otherwise keep it resting: the entry may fill on a later bar
			// (common same-bar strategy.entry + strategy.exit declarations
			// previously lost their stops entirely).
			if c.Armed {
				continue
			}
			keep = append(keep, c)
			continue
		}
		c.Armed = true
		keep = append(keep, c)
		for _, apply := range targets {
			p := s.positions[apply]
			dir := signOf(p.Qty)
			stop := c.Stop
			lim := c.Limit
			if c.Loss > 0 {
				stop = stopAt(p, c.Loss, true)
			}
			if c.Profit > 0 {
				lim = stopAt(p, c.Profit, false)
			}
			if c.TrailPts > 0 {
				if dir > 0 {
					// The trail level for this bar must be derived from the
					// extreme through the PREVIOUS bar: raising the trail with
					// this bar's high and then triggering on this bar's low
					// assumes the high traded before the low, which
					// manufactures same-bar profits.
					ts := p.TrailHi - c.TrailPts - c.TrailOff
					if stop == 0 || ts > stop {
						stop = ts
					}
				} else {
					ts := p.TrailLo + c.TrailPts + c.TrailOff
					if stop == 0 || ts < stop {
						stop = ts
					}
				}
				trailSeen[apply] = dir
			}
			braces[apply] = mergeBracket(braces[apply], bracket{entryID: apply, stop: stop, limit: lim})
		}
	}

	// execute brackets (conservative stop-first)
	for id, br := range braces {
		p := s.positions[id]
		if p == nil || math.Abs(p.Qty) < 1e-12 {
			continue
		}
		dir := signOf(p.Qty)
		var fill float64
		var reason string
		if br.stop > 0 && ((dir > 0 && bar.L <= br.stop) || (dir < 0 && bar.H >= br.stop)) {
			fill, reason = br.stop, "stop_loss"
		} else if br.limit > 0 && ((dir > 0 && bar.H >= br.limit) || (dir < 0 && bar.L <= br.limit)) {
			fill, reason = br.limit, "take_profit"
		} else {
			continue
		}
		// A level already beyond the bar's open (invalid placement — e.g. a
		// long stop above the market — or a gap through the level) fills at
		// the open. Filling at the raw level transacted at a price the market
		// never traded (worst case seen: long stops at 100x market price)
		// and manufactured astronomical returns.
		if dir > 0 {
			// long: buys fill at the higher open, sells at the lower open
			if reason == "stop_loss" && bar.O < fill {
				fill = bar.O
			}
			if reason == "take_profit" && bar.O > fill {
				fill = bar.O
			}
		} else {
			// short: stop is a buy (fill at the higher open when gapped),
			// take-profit is a buy below (fill at the lower open when gapped)
			if reason == "stop_loss" && bar.O > fill {
				fill = bar.O
			}
			if reason == "take_profit" && bar.O < fill {
				fill = bar.O
			}
		}
		// close the qty this bracket applies to (qty is whole position here)
		s.realize(id, -float64(dir)*math.Abs(p.Qty), fill, b, reason)
		// record a signal with the actual price
		s.Signals = append(s.Signals, Signal{Bar: b, ID: "__exit_" + id, Kind: "exit", Dir: dir, Qty: math.Abs(p.Qty), Price: fill, Comment: reason})
	}

	// 2. resting conditional entries
	still := orderCond[:0]
	for _, c := range orderCond {
		if c.Kind != "entry_stop" && c.Kind != "entry_limit" {
			continue
		}
		if c.FromBar >= b {
			still = append(still, c) // submitted this bar: activates next bar
			continue
		}
		var trig bool
		if c.Kind == "entry_stop" {
			trig = (c.Dir > 0 && bar.H >= c.Stop) || (c.Dir < 0 && bar.L <= c.Stop)
		} else {
			trig = (c.Dir > 0 && bar.L <= c.Limit) || (c.Dir < 0 && bar.H >= c.Limit)
		}
		if !trig {
			still = append(still, c)
			continue
		}
		price := c.Stop
		if c.Kind == "entry_limit" {
			price = c.Limit
		}
		// A level already beyond the bar's open (invalid placement — e.g. a
		// long stop-entry far below the market — or a gap through it) fills
		// at the open. Filling at the raw level bought far below the market
		// and booked instant mark-to-market windfalls.
		if c.Dir > 0 && price < bar.O {
			price = bar.O
		}
		if c.Dir < 0 && price > bar.O {
			price = bar.O
		}
		it := Intent{ID: c.ID, Dir: c.Dir, Qty: c.Qty, OCA: c.OCA, When: true}
		s.executeEntry(it, c.Dir, price, b)
		// note: executeEntry cancels OCA group
	}
	s.pendingCond = append(append(deferred, keep...), still...)

	// 2b. Update trailing extremes AFTER level computation and fills, so the
	// next bar's trail uses this bar's high/low but this bar's trigger check
	// did not.
	for apply, dir := range trailSeen {
		if p, ok := s.positions[apply]; ok {
			if dir > 0 {
				p.TrailHi = math.Max(p.TrailHi, bar.H)
			} else {
				p.TrailLo = math.Min(p.TrailLo, bar.L)
			}
		}
	}
}

// stopAt computes stop/target prices from an average price and a distance.
func stopAt(p *Position, distance float64, isStop bool) float64 {
	dir := signOf(p.Qty)
	if isStop {
		if dir > 0 {
			return p.Avg - distance
		}
		return p.Avg + distance
	}
	if dir > 0 {
		return p.Avg + distance
	}
	return p.Avg - distance
}

type bracket struct {
	entryID string
	stop    float64
	limit   float64
}

func mergeBracket(a, b bracket) bracket {
	if b.stop > 0 {
		a.stop = b.stop
	}
	if b.limit > 0 {
		a.limit = b.limit
	}
	return a
}

func (s *Sim) cancelOCA(group string) {
	if group == "" {
		return
	}
	var pm []Intent
	for _, it := range s.pendingMarket {
		if it.OCA == group && it.Kind != "exit" {
			continue
		}
		pm = append(pm, it)
	}
	s.pendingMarket = pm
	var pc []CondOrder
	for _, c := range s.pendingCond {
		if c.OCA == group && c.Kind != "exit" {
			continue
		}
		pc = append(pc, c)
	}
	s.pendingCond = pc
}

func signOf(f float64) int {
	if f > 0 {
		return 1
	}
	if f < 0 {
		return -1
	}
	return 0
}