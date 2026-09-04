// SPDX-FileCopyrightText: 2026 Woodstock K.K.
//
// SPDX-License-Identifier: AGPL-3.0-only

package pinescription

import (
	"sync"
	"time"

	wseries "github.com/woodstock-tokyo/pinescription/series"
)

type flowKind string

const (
	flowNone     flowKind = "none"
	flowBreak    flowKind = "break"
	flowContinue flowKind = "continue"
	flowReturn   flowKind = "return"
)

const maxPooledInterfaceCap = 256
const maxPooledEnvMapEntries = 64

var interfaceSlicePool = sync.Pool{New: func() interface{} { return make([]interface{}, 0, 8) }}
var envMapPool = sync.Pool{New: func() interface{} { return make(map[string]interface{}, 8) }}

var disableCallArgPooling bool
var disableEnvMapPooling bool
var disableLoopIteratorFastPath bool
var disableSwitchCaseConstFastPath bool

func acquireInterfaceSlice(size int) []interface{} {
	v := interfaceSlicePool.Get()
	buf, ok := v.([]interface{})
	if !ok {
		return make([]interface{}, 0, size)
	}
	if cap(buf) < size {
		return make([]interface{}, 0, size)
	}
	return buf[:0]
}

func releaseInterfaceSlice(buf []interface{}) {
	if buf == nil {
		return
	}
	if cap(buf) > maxPooledInterfaceCap {
		return
	}
	for i := range buf {
		buf[i] = nil
	}
	interfaceSlicePool.Put(buf[:0])
}

func acquireEnvMap() map[string]interface{} {
	v := envMapPool.Get()
	m, ok := v.(map[string]interface{})
	if !ok {
		return map[string]interface{}{}
	}
	clear(m)
	return m
}

func releaseEnvMap(m map[string]interface{}) {
	if m == nil {
		return
	}
	if len(m) > maxPooledEnvMapEntries {
		return
	}
	clear(m)
	envMapPool.Put(m)
}

type flow struct {
	kind     flowKind
	value    interface{}
	hasValue bool
}

// RuntimeSnapshot is a point-in-time view of the execution state produced by Runtime.Snapshot.
// BarIndex is the zero-based index of the current bar. LastValue is the numeric result
// of the last evaluated expression. Symbols and SeriesKeys describe the data in use.
// Variables holds the current value of every top-level variable and function parameter
// in scope, keyed by name.
type RuntimeSnapshot struct {
	BarIndex        int
	LastValue       float64
	ActiveSymbol    string
	ActiveValueType string
	Symbols         []string
	SeriesKeys      []string
	Variables       map[string]interface{}
}

// Runtime represents the execution state of a compiled Pine Script program. It is
// produced by Engine.ExecuteWithRuntime and must not be mutated by callers. After
// a Runtime is no longer needed, call Release to return pooled memory.
type Runtime struct {
	program          Program
	userFns          map[string]UserFunction
	userFnParamSpecs map[string]callParamSpec

	rootNamespaces map[string]interface{}

	seriesByKey         map[string]SeriesExtended
	namedSeries         map[string]SeriesExtended
	seriesExprByName    map[string]*Expr
	seriesExprResolving map[string]bool
	// seriesExprCache memoizes derived series per expression pointer: nested
	// calls like hma(hma(close, 20), 50) re-derive the inner series from
	// scratch every bar (O(bars²) per level). Cached queues extend
	// incrementally — one new value per bar. (*Queue satisfies
	// SeriesExtended and exposes Update for incremental extension.)
	seriesExprCache     map[*Expr]*wseries.Queue
	indicatorState      map[string]interface{}
	extremaState        map[extremaStateKey]*extremaIndicatorState
	valueTypesBySymbol  map[string]map[string]bool
	loadSeries          func(symbol, valueType string) (SeriesExtended, error)

	activeSymbol    string
	activeValueType string
	timeframe       string
	session         string
	timeframePeriod string
	timeframeBase   string
	timeframeMult   int
	timeframeSecs   int
	timeframeSecsOK bool
	currentTime     time.Time
	startTime       time.Time
	logSink         func(level, message string, ts time.Time)
	alertSink       func(AlertEvent)
	barStep         time.Duration
	lastTimeIndex   int
	lastBarOpen     time.Time
	lastBarClose    time.Time
	lastTradingDay  time.Time
	seriesKeyCache  map[string]map[string]string
	identKindCache  map[string]identifierKind
	expectedBars    int
	rootHistoryVars []string
	rootHistorySet  map[string]struct{}
	priceCacheBar   int
	priceCacheMask  uint8
	priceCacheOpen  float64
	priceCacheHigh  float64
	priceCacheLow   float64
	priceCacheClose float64
	priceCacheVol   float64
	loopBindings    []loopBinding

	barIndex   int
	evalOffset int
	lastValue  float64

	// evalDepth guards against pathological expression nesting (e.g. a var
	// bound to an index of itself) so a bad script errors instead of
	// overflowing the goroutine stack and killing the whole process.
	evalDepth int
	// historyResolving tracks variable names whose seriesArgument expr is
	// currently being evaluated; a re-entrant lookup for the same name
	// falls through to the history queue instead of recursing forever.
	historyResolving map[string]bool

	// barssinceMemo caches, per condition expression, the most recent bar where
	// the condition was true so ta.barssince / ta.valuewhen do not rescan the
	// whole history on every bar (which made rarely-true conditions O(n^2)).
	barssinceMemo map[*Expr]barssinceMemoEntry

	envStack       []map[string]interface{}
	consts         map[string]bool
	declaredTypes  map[string]string
	history        map[string][]interface{}
	numericHistory map[string][]float64
	historyKind    map[string]historyStorageKind
}

// barssinceMemoEntry tracks the most recent bar (relative to the runtime's
// current bar) where a condition evaluated true.
type barssinceMemoEntry struct {
	lastTrueBar  int
	lastCheckBar int
	found        bool
}

type historyStorageKind uint8

const (
	historyStorageUnknown historyStorageKind = iota
	historyStorageNumeric
	historyStorageGeneric
)

type customTypeInstance struct {
	TypeName string
	Fields   map[string]interface{}
}

type identifierKind uint8

const (
	identKindGeneric identifierKind = iota
	identKindPrice
	identKindBarIndex
	identKindMathConst
	identKindSessionConst
	identKindTime
	identKindTimeframe
	identKindDotted
)

type loopBinding struct {
	name  string
	value float64
}

type callParamSpec struct {
	Names    []string
	Required int
}

func (s callParamSpec) indexOf(name string) int {
	for i, candidate := range s.Names {
		if candidate == name {
			return i
		}
	}
	return -1
}

var builtinCallParamSpecs = map[string]callParamSpec{
	"alert":        {Names: []string{"message", "freq"}, Required: 1},
	"alertcondition": {Names: []string{"condition", "title", "message", "display"}, Required: 1},
	"barcolor":     {Names: []string{"color", "offset", "editable", "show_last", "title", "display"}},
	"label.new":    {Names: []string{"x", "y", "text", "xloc", "yloc", "color", "style", "textcolor", "size", "textalign", "tooltip", "text_font_family"}, Required: 2},
	"label.delete": {Names: []string{"id"}, Required: 1},
	"line.new":     {Names: []string{"x1", "y1", "x2", "y2", "xloc", "extend", "color", "style", "width"}, Required: 4},
	"line.delete":  {Names: []string{"id"}, Required: 1},
	"box.new":      {Names: []string{"left", "top", "right", "bottom", "border_color", "border_width", "border_style", "extend", "xloc", "bgcolor", "text", "text_size", "text_color", "text_halign", "text_valign", "text_wrap", "force_overlay"}, Required: 4},
	"color.new":    {Names: []string{"color", "transp"}, Required: 1},
	"color.rgb":    {Names: []string{"red", "green", "blue", "transp"}, Required: 3},
	"color":        {Names: []string{"red", "green", "blue", "transp"}},
	"inputv":       {Names: []string{"defval", "title", "tooltip", "inline", "group", "display"}},
	"input.time":   {Names: []string{"defval", "title", "tooltip", "inline", "group", "display", "confirm"}},
	"round_to_mintick": {Names: []string{"x"}},
	"indicator":    {Names: []string{"title", "shorttitle", "overlay", "format", "precision", "scale", "max_bars_back", "timeframe", "timeframe_gaps", "explicit_plot_zorder", "max_lines_count", "max_labels_count", "max_boxes_count", "max_polylines_count", "dynamic_requests", "behind_chart"}},
	"input":        {Names: []string{"defval", "title", "tooltip", "inline", "group", "display"}},
	"input.bool":   {Names: []string{"defval", "title", "tooltip", "inline", "group", "display"}},
	"input.color":  {Names: []string{"defval", "title", "tooltip", "inline", "group", "display"}},
	"input.float":  {Names: []string{"defval", "title", "tooltip", "inline", "group", "display", "step", "minval", "maxval", "confirm"}},
	"input.int":    {Names: []string{"defval", "title", "tooltip", "inline", "group", "display", "step", "minval", "maxval", "confirm"}},
	"input.source": {Names: []string{"defval", "title", "tooltip", "inline", "group", "display"}},
	"input.string": {Names: []string{"defval", "title", "tooltip", "inline", "group", "display", "options", "confirm"}},
	"rsi":          {Names: []string{"source", "length"}, Required: 1},
	"ta.rsi":       {Names: []string{"source", "length"}, Required: 1},
	"cci":          {Names: []string{"source", "length"}, Required: 1},
	"ta.cci":       {Names: []string{"source", "length"}, Required: 1},
	"valuewhen":    {Names: []string{"condition", "source", "occurrence"}, Required: 1},
	"ta.valuewhen": {Names: []string{"condition", "source", "occurrence"}, Required: 1},
	"str.tostring": {Names: []string{"x", "format"}, Required: 1},
	"tostring":     {Names: []string{"x", "format"}, Required: 1},
	"string":       {Names: []string{"x", "format"}, Required: 1},
	"time":         {Names: []string{"timeframe", "session", "timezone"}},
	"time_close":   {Names: []string{"timeframe", "session", "timezone"}},
	"timestamp":    {Names: []string{"y", "m", "d", "h", "min", "s", "tz"}},
	"table.cell":   {Names: []string{"table_id", "column", "row", "text", "width", "height", "text_color", "text_halign", "text_valign", "text_size", "bgcolor", "tooltip", "text_font_family"}},
	"table.clear":  {Names: []string{"table_id", "start_column", "start_row", "end_column", "end_row"}, Required: 1},
	"table.new":    {Names: []string{"position", "columns", "rows", "bgcolor", "frame_color", "frame_width", "border_color", "border_width"}, Required: 3},

	// ta.* builtins that accept named arguments in Pine (source=/length=
	// etc.); binding them positionally makes scripts that use named args
	// compile instead of failing with "named arguments are not supported".
	"sma":       {Names: []string{"source", "length"}, Required: 1},
	"ta.sma":    {Names: []string{"source", "length"}, Required: 1},
	"ema":       {Names: []string{"source", "length"}, Required: 1},
	"ta.ema":    {Names: []string{"source", "length"}, Required: 1},
	"wma":       {Names: []string{"source", "length"}, Required: 1},
	"ta.wma":    {Names: []string{"source", "length"}, Required: 1},
	"hma":       {Names: []string{"source", "length"}, Required: 1},
	"ta.hma":    {Names: []string{"source", "length"}, Required: 1},
	"rma":       {Names: []string{"source", "length"}, Required: 1},
	"ta.rma":    {Names: []string{"source", "length"}, Required: 1},
	"vwma":      {Names: []string{"source", "length"}, Required: 1},
	"ta.vwma":   {Names: []string{"source", "length"}, Required: 1},
	"variance":  {Names: []string{"source", "length", "biased"}, Required: 1},
	"ta.variance": {Names: []string{"source", "length", "biased"}, Required: 1},
	"stdev":     {Names: []string{"source", "length", "biased"}, Required: 1},
	"ta.stdev":  {Names: []string{"source", "length", "biased"}, Required: 1},
	"dev":       {Names: []string{"source", "length", "biased"}, Required: 1},
	"ta.dev":    {Names: []string{"source", "length", "biased"}, Required: 1},
	"crossover": {Names: []string{"source1", "source2"}, Required: 2},
	"ta.crossover": {Names: []string{"source1", "source2"}, Required: 2},
	"crossunder": {Names: []string{"source1", "source2"}, Required: 2},
	"ta.crossunder": {Names: []string{"source1", "source2"}, Required: 2},
	"highest":     {Names: []string{"source", "length"}, Required: 1},
	"ta.highest":  {Names: []string{"source", "length"}, Required: 1},
	"lowest":      {Names: []string{"source", "length"}, Required: 1},
	"ta.lowest":   {Names: []string{"source", "length"}, Required: 1},
	"highestbars": {Names: []string{"source", "length"}, Required: 1},
	"ta.highestbars": {Names: []string{"source", "length"}, Required: 1},
	"lowestbars":  {Names: []string{"source", "length"}, Required: 1},
	"ta.lowestbars": {Names: []string{"source", "length"}, Required: 1},
	"atr":          {Names: []string{"length"}, Required: 1},
	"ta.atr":       {Names: []string{"length"}, Required: 1},
	"tr":           {Names: []string{"handle_na"}},
	"ta.tr":        {Names: []string{"handle_na"}},
	"mom":          {Names: []string{"source", "length"}, Required: 1},
	"ta.mom":       {Names: []string{"source", "length"}, Required: 1},
	"roc":          {Names: []string{"source", "length"}, Required: 1},
	"ta.roc":       {Names: []string{"source", "length"}, Required: 1},
	"mfi":          {Names: []string{"source", "length"}, Required: 1},
	"ta.mfi":       {Names: []string{"source", "length"}, Required: 1},
	"obv":          {Names: []string{"source"}},
	"ta.obv":       {Names: []string{"source"}},
	"cmo":          {Names: []string{"source", "length"}, Required: 1},
	"ta.cmo":       {Names: []string{"source", "length"}, Required: 1},
	"change":       {Names: []string{"source", "length"}, Required: 1},
	"ta.change":    {Names: []string{"source", "length"}, Required: 1},
	"cum":          {Names: []string{"source"}, Required: 1},
	"ta.cum":       {Names: []string{"source"}, Required: 1},
	"sum":          {Names: []string{"source", "length"}, Required: 1},
	"ta.sum":       {Names: []string{"source", "length"}, Required: 1},
	"median":       {Names: []string{"source", "length"}, Required: 1},
	"ta.median":    {Names: []string{"source", "length"}, Required: 1},
	"percentrank":  {Names: []string{"source", "length"}, Required: 1},
	"ta.percentrank": {Names: []string{"source", "length"}, Required: 1},
	"correlation":  {Names: []string{"source1", "source2", "length"}, Required: 2},
	"ta.correlation": {Names: []string{"source1", "source2", "length"}, Required: 2},
	"vwap":         {Names: []string{"source"}},
	"ta.vwap":      {Names: []string{"source"}},
	"linreg":       {Names: []string{"source", "length", "offset"}, Required: 1},
	"ta.linreg":    {Names: []string{"source", "length", "offset"}, Required: 1},
}
