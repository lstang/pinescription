package main

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// preprocess normalizes legacy (v1..v5) Pine idiom that the v6 engine rejects
// into equivalent constructs the engine understands. All replacements are
// applied outside string literals.
func preprocess(src string) string {
	// NBSP (and other invisible Unicode whitespace) used for indentation is
	// not treated as whitespace by the lexer; normalize to plain spaces.
	src = strings.ReplaceAll(src, "\u00a0", " ")
	src = strings.ReplaceAll(src, "\u3000", "  ")

	// The lexer has no /* */ block comments (Pine v5+ removed them), but the
	// FMZ backtest headers use them. Strip block comments outside string
	// literals first so quoted content inside comments cannot confuse the
	// string-aware replacements that follow.
	src = stripBlockComments(src)
	// Inline line comments ("// ...") are meaningless to the parser but break
	// line-joining and if-to-ternary rewrites ("10 // 10% limit" swallows the
	// rest of a built ternary). Strip them string-aware (URLs and titles
	// containing "//" inside quotes are preserved). Do this before the joins.
	src = stripLineComments(src)

	// The parser does not treat tabs as indentation; normalize leading
	// whitespace to spaces (a tab becomes 4 spaces, matching Pine's default
	// indentation increment), then snap each code line's indent onto the block
	// stack so off-by-one indentation artifacts (common in converted sources)
	// cannot break the parser's strict INDENT/DEDENT accounting.
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		trimmed := strings.TrimLeft(ln, " \t")
		lead := 0
		for _, r := range ln[:len(ln)-len(trimmed)] {
			if r == '\t' {
				lead += 4
			} else {
				lead++
			}
		}
		lines[i] = strings.Repeat(" ", lead) + trimmed
	}
	src = strings.Join(lines, "\n")
	src = snapIndentation(src)
	// "x = if(cond)" (no space between if and the paren) is a common
	// conversion artifact; the if-expression rewriters below only recognize
	// "if " followed by whitespace, so normalize to "if (cond)" — the paren
	// becomes part of the condition, which the statement form also accepts.
	src = regexp.MustCompile(`(?m)^([ \t]*[A-Za-z_][A-Za-z0-9_]*\s*(?::=|=)[ \t]*)if\(`).ReplaceAllString(src, "${1}if (")
	// strategy.initial_capital = X is an assignment to a builtin the engine
	// exposes as a constant; drop the assignment line (its value cannot be
	// honored, and the token guard below would corrupt it into "10000 = X").
	src = regexp.MustCompile(`(?m)^[ \t]*strategy\.initial_capital\s*=[^\n]*\n?`).ReplaceAllString(src, "")
	// v3/v4 continuation lines that begin with a binary operator ("or",
	// "and", "+", ...) are joined onto the previous line; the engine's lexer
	// otherwise treats them as the start of a new statement. Joining first
	// also folds multi-line call argument lists into a single line so the
	// reserved-variable rename below can tell named arguments ("input(...,
	// type=...)") apart from statement-level assignments ("label = ...").
	src = joinTrailingOperatorLines(src)
	src = joinContinuationLines(src)
	// Lines with a surplus of ")" are either the tail of a call opened on a
	// previous line (join) or conversion artifacts (drop).
	src = fixUnbalancedCloseLines(src)
	// Residue lines starting with a top-level comma (", linestyle=3, ...)")
	// are extraction artifacts: drop them.
	src = dropLeadingCommaContinuations(src)
	// A declaration call whose trailing "/" is residue of a corrupted
	// banner comment ("//******" mis-stripped): "study(...) /" -> "study(...)".
	src = dropDeclarationTrailingSlash(src)
	// "var bool a = na, var bool b = na", "isLong := true, TriggerPrice :=
	// close", "bgcolor(...), alertcondition(...)" — several statements on one
	// line separated by top-level commas (common in converted sources) are not
	// supported by the parser: split into one statement per line.
	src = splitStatementCommas(src)
	// Empty conditional/loop blocks ("if cond" with no body because the next
	// code line dedents) cannot be parsed; drop the dangling statement.
	src = dropEmptyBlocks(src)
	// v5 generics: "var array_<line> lines = array.new<line>()" ->
	// "var lines = array.new()". The engine's array is untyped.
	src = regexp.MustCompile(`\barray_<[^>\n]*>`).ReplaceAllString(src, "array_")
	src = regexp.MustCompile(`\barray\.new<[^>\n]*>\s*\(`).ReplaceAllString(src, "array.new(")
	src = regexp.MustCompile(`\barray\.new_float\b|\barray\.new_int\b|\barray\.new_bool\b|\barray\.new_string\b`).ReplaceAllString(src, "array.new")
	// drop library import lines BEFORE the reserved-word rename: the rename
	// turns the import keyword "as" into the identifier "as_" and the import
	// stripper no longer recognizes the line ("keyword import ... not
	// supported").
	src = stripImportLines(src)
	// Variables that shadow engine keywords ("return", "type", "label",
	// "color", ...) break parsing.
	src = renameReservedVarNames(src)
	// Function signatures/bodies that use engine keywords as names or params
	// ("function(source, length) =>", "IF(input) =>") break the parser.
	src = renameKeywordParams(src)
	// v5 typed array parameter annotations ("getArrayValue(float[] arr, int
	// ago) =>"): the engine has no typed params, strip the type tokens.
	src = stripTypedArrayParams(src)
	// Section-header lines that are a lone bare identifier ("Stochastic")
	// would fail at runtime as unknown identifiers.
	src = dropLoneIdentLines(src)
	// "f(args) => name = expr" bodies cannot be parsed by the engine's arrow
	// handling, so expand them to an indented block that assigns and returns.
	src = rewriteArrowAssignmentBody(src)

	guard := map[string]string{
		"strategy.long":                    `"long"`,
		"strategy.short":                   `"short"`,
		"strategy.percent_of_equity":       "1",
		"strategy.fixed":                   "0",
		"strategy.cash":                    "2",
		"strategy.commission.percent":      "1",
		"strategy.commission.cash_per_contract": "2",
		"strategy.commission.cash_per_order": "3",
		"strategy.currency.USD":            `"USD"`,
		"strategy.currency.EUR":            `"EUR"`,
		"strategy.currency.CNY":            `"CNY"`,
		"strategy.currency.JPY":            `"JPY"`,
		"strategy.currency.GBP":            `"GBP"`,
		"syminfo.mintick":                  "1.0",
		"syminfo.pointvalue":               "1.0",
		"syminfo.tickvalue":                "1.0",
		"syminfo.pipvalue":                 "0.01",
		"syminfo.pip":                      "0.01",
		"syminfo.session":                  `"24x7"`,
		"syminfo.timezone":                 `"UTC"`,
		"syminfo.currency":                 `"USD"`,
		"syminfo.tickerid":                 `"SYM"`,
		"syminfo.ticker":                   `"SYM"`,
		"syminfo.root":                     `"SYM"`,
		"syminfo.prefix":                   `""`,
		"syminfo.type":                     `""`,
		"syminfo.description":              `""`,
		"time_tradingday":                  "time",
		"barmerge.lookahead_on":            "true",
		"barmerge.lookahead_off":           "false",
		"barmerge.gaps_on":                 "true",
		"barmerge.gaps_off":                "false",
		"currency.USD":                     `"USD"`,
		"currency.EUR":                     `"EUR"`,
		"currency.NONE":                    `"NONE"`,
		"currency.GBP":                     `"GBP"`,
		"currency.JPY":                     `"JPY"`,
		"currency.CNY":                     `"CNY"`,
		"currency.USDT":                    `"USDT"`,
		"currency.USDC":                    `"USDC"`,
		"currency.BUSD":                    `"BUSD"`,
		"currency.TRY":                     `"TRY"`,
		"currency.CAD":                     `"CAD"`,
		"currency.AUD":                     `"AUD"`,
		"currency.CHF":                     `"CHF"`,
		"currency.RUB":                     `"RUB"`,
		"scale.left":                       "-1",
		"scale.right":                      "1",
		"scale.none":                       "2",
		"timeframe.period":                 `"1D"`,
		"timeframe.isintraday":             "false",
		"timeframe.isseconds":              "false",
		"timeframe.isminutes":              "false",
		"timeframe.isdaily":                "true",
		"timeframe.isweekly":               "false",
		"timeframe.ismonthly":              "false",
		"barstate.isconfirmed":             "true",
		"barstate.isnew":                   "true",
		"barstate.ishistory":               "true",
		"barstate.isrealtime":              "false",
		"barstate.isfirstbar":              "false",
		"barstate.islast":                  "false",
		"xloc.bar_index":                   "1",
		"xloc.bar_time":                    "0",
		"yloc.belowbar":                    "1",
		"yloc.abovebar":                    "2",
		"yloc.price":                       "3",
		"strategy.direction.all":           "0",
		"strategy.direction.long":          "1",
		"strategy.direction.short":         "-1",
		"strategy.initial_capital":          "10000",
		"dayofweek.monday":                 "1",
		"dayofweek.tuesday":                "2",
		"dayofweek.wednesday":              "3",
		"dayofweek.thursday":               "4",
		"dayofweek.friday":                 "5",
		"dayofweek.saturday":               "6",
		"dayofweek.sunday":                 "0",
		"month.january":                    "1", "month.february": "2", "month.march": "3",
		"month.april":                      "4", "month.may": "5", "month.june": "6",
		"month.july":                       "7", "month.august": "8", "month.september": "9",
		"month.october":                    "10", "month.november": "11", "month.december": "12",
	}
	// guard keys are dotted ident chains; longer keys first so "syminfo.ticker"
	// cannot match inside "syminfo.tickerid", and token-boundary aware so the
	// replacement never lands mid-identifier
	keys := make([]string, 0, len(guard))
	for k := range guard {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		src = replaceTokensOutsideStrings(src, k, guard[k])
	}

	// plot/line/shape style constants -> plain numbers
	styleMap := map[string]string{
		"plot.style_line": "1", "plot.style_cross": "2", "plot.style_area": "3",
		"plot.style_columns": "4", "plot.style_circles": "5", "plot.style_histogram": "6",
		"plot.style_stepline": "7", "plot.style_areabr": "8", "plot.style_arrow_up": "9",
		"plot.style_arrow_down": "10", "plot.style_linebr": "7", "plot.style_treeline": "7",
		"hline.style_solid": "1", "hline.style_dotted": "2", "hline.style_dashed": "3",
		"shape.triangleup": "1", "shape.triangledown": "2", "shape.square": "3",
		"shape.circle": "4", "shape.xcross": "5", "shape.labelup": "6", "shape.labeldown": "7",
		"shape.arrowup": "8", "shape.arrowdown": "9", "shape.flag": "10",
		"shape.cross": "5", "shape.diamond": "3", "shape.triangle": "1",
		"shape.triangleup2": "15", "shape.triangledown2": "16", "shape.arrowup2": "13",
		"shape.arrowdown2": "14", "shape.square2": "11", "shape.xcross2": "10",
		"shape.circle2": "9", "shape.label": "6", "shape.none": "0",
		"label.style_label_up": "1", "label.style_label_down": "2", "label.style_label_left": "3",
		"label.style_label_right": "4", "label.style_label_lower_left": "5",
		"label.style_label_lower_right": "6", "label.style_label_upper_left": "7",
		"label.style_label_upper_right": "8", "label.style_circle": "9",
		"label.style_xcross": "10", "label.style_square": "11", "label.style_diamond": "12",
		"label.style_arrow_up": "13", "label.style_arrow_down": "14",
		"label.style_arrowup": "13", "label.style_arrowdown": "14",
		"label.style_triangleup": "15", "label.style_triangledown": "16",
		"label.style_triangleup2": "15", "label.style_triangledown2": "16",
		"label.style_flower": "17", "label.style_flag": "18", "label.style_none": "19",
		"location.abovebar": "1", "location.belowbar": "2", "location.top": "3",
		"location.bottom": "4", "location.absolute": "5", "location.left": "6", "location.right": "7",
		"size.tiny": "1", "size.small": "2", "size.normal": "3", "size.large": "4", "size.huge": "5",
	}
	styleKeys := make([]string, 0, len(styleMap))
	for k := range styleMap {
		styleKeys = append(styleKeys, k)
	}
	sort.Slice(styleKeys, func(i, j int) bool { return len(styleKeys[i]) > len(styleKeys[j]) })
	for _, k := range styleKeys {
		src = replaceTokenOutsideStrings(src, k, styleMap[k])
	}

	// v3: style = <word> / color = <word> etc. (words only, not dotted
	// constants like label.style_arrowdown, which the map above handles)
	styleWord := regexp.MustCompile(`\bstyle\s*=\s*(line|cross|circles|area|columns|histogram|stepline|areabr|arrowup|arrow_up|arrowdown|arrow_down|label|left|right|top|bottom|linebr)((?:\s|,|\)|\]|;|\n|$))`)
	src = styleWord.ReplaceAllString(src, "style=1$2")
	linestyleWord := regexp.MustCompile(`\blinestyle\s*=\s*(solid|dotted|dashed)\b`)
	src = linestyleWord.ReplaceAllString(src, "linestyle=1")
	// bare linebr as a plot style value (v4 idiom: style=linebr)
	src = replaceTokensOutsideStrings(src, "linebr", "7")

	// ticker.heikinashi(sym) -> plain symbol (request.security then reads raw
	// OHLC as a close approximation of heikin-ashi candles).
	heikinashiRe := regexp.MustCompile(`ticker\.heikinashi\s*\([^)\n]*\)`)
	src = heikinashiRe.ReplaceAllString(src, "syminfo.tickerid")

	// bare color constants -> color.* (outside strings), skipping lines that
	// define a variable of the same name.
	colorNames := []string{
		"aqua", "black", "blue", "fuchsia", "gray", "green", "lime", "maroon",
		"navy", "olive", "orange", "purple", "red", "silver", "teal", "white", "yellow",
	}
	tokenRe := regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\b`)
	src = applyLinewise(src, func(ln string) string {
		t := strings.TrimSpace(ln)
		// skip variable definitions like "red = ..." (also const red = ...)
		if m := tokenRe.FindStringSubmatch(t); len(m) == 2 {
			for _, c := range colorNames {
				if m[1] == c {
					if strings.HasPrefix(t, c+" ") || strings.HasPrefix(t, c+"=") ||
						strings.HasPrefix(t, "var "+c+" ") || strings.HasPrefix(t, "const "+c+" ") {
						return ln
					}
				}
			}
		}
		out := ln
		for _, c := range colorNames {
			// Only replace when not already namespaced (color.red stays intact).
			out = replaceColorWord(out, c)
		}
		return out
	})

	// iff(a, b, c) -> (a ? b : c), balanced-paren aware (supports nesting and
	// multi-line calls).
	src = rewriteIff(src)

	// v2/v3 assignment-from-if with single-expression bodies:
	//   "x = if cond / expr / else / expr"  ->  "x = cond ? expr : expr"
	// Only when both branch values are plain expressions — branch values that
	// are themselves if/else blocks must go through the structural rewriters
	// (rewriteIfExprChain / rewriteIfAssignBlock); a blind flatten here
	// corrupts them ("x = (... : if cond)" with a dangling body).
	ifAssign := regexp.MustCompile(`^([ \t]*[A-Za-z_][A-Za-z0-9_]*\s*(?::=|=)\s*)if\s+(.+?)\s*$`)
	ifLines := strings.Split(src, "\n")
	outL := make([]string, 0, len(ifLines))
	for li := 0; li < len(ifLines); li++ {
		m := ifAssign.FindStringSubmatch(ifLines[li])
		if m == nil || li+3 >= len(ifLines) {
			outL = append(outL, ifLines[li])
			continue
		}
		v1 := strings.TrimSpace(ifLines[li+1])
		elseLine := strings.TrimSpace(ifLines[li+2])
		v2 := strings.TrimSpace(ifLines[li+3])
		plain := func(v string) bool {
			return v != "" && v != "if" && !strings.HasPrefix(v, "if ") && !strings.HasPrefix(v, "else")
		}
		if elseLine == "else" && plain(v1) && plain(v2) {
			outL = append(outL, m[1]+"("+m[2]+" ? "+v1+" : "+v2+")")
			li += 3
			continue
		}
		outL = append(outL, ifLines[li])
	}
	src = strings.Join(outL, "\n")

	// v5-style conditional expressions with else-if / else chains:
	//   "x = if c1 / e1 / else if c2 / e2 / else / e3"  ->  "x = c1 ? e1 : c2 ? e2 : e3"
	src = rewriteIfExprChain(src)

	// math.round(x, precision=4) -> math.round(x, 4) (the engine's round
	// accepts a positional precision but not the named form).
	src = roundPrecisionRe.ReplaceAllString(src, "$1($2, $3)")

	// v3 comma-separated top-level assignments: "a = 0, b = 0" or
	// "longStop = 0.0, shortStop = 0.0" -> separate lines.
	src = splitCommaAssignments(src)

	// "x := if cond\n    s = ...\n    value\nelse\n    alt" (multi-statement
	// if-expression) -> a var declaration plus a statement-form if/else.
	src = rewriteIfExprAssign(src)

	// "gx = for i = 1 to n\n    ...\n    value" (for-loop used as an
	// expression value) -> statement-form loop assigning the variable.
	src = rewriteForExprAssign(src)

	// "name = if cond\n    value\nelse\n    ..." with arbitrary (nested)
	// branch blocks -> statement-form if/else assigning the variable.
	src = rewriteIfAssignBlock(src)

	// stray second call argument after a closed paren, an extraction artifact
	// (") ,transp=0" appended to a plot/fill call): drop the trailing
	// ",transp=N". String-aware so only real occurrences are touched.
	src = stripStrayTransp(src)

	// "x = (cond ? x = a : x = b)" (redundant self-assignment inside ternary
	// branches) -> "x = (cond ? a : b)".
	src = rewriteTernarySelfAssign(src)

	// v3 damaged ternaries of the form "a == 0 ? 100 : = 0 ? b : c" (the
	// extractor dropped the variable between ": " and "=") -> collapse the
	// broken middle branch: "a == 0 ? 100 : (b ? 0 : c)" is not recoverable,
	// but the common idiom is "down == 0 ? 100 : (up == 0 ? 0 : expr)", so
	// restore the missing condition from the leading comparison:
	// "X == 0 ? 100 : = 0 ? 0 : E" -> "X == 0 ? 100 : 0".
	src = fixDroppedTernaryCond(src)

	// "if x then" (v2) -> "if x"
	src = regexp.MustCompile(`(?m)^([ \t]*if .*?) then\s*$`).ReplaceAllString(src, "$1")

	// input() named args the engine rejects or that duplicate positional
	// forms: minval/maxval/step/group/display/tooltip/confirm and type=...
	// constants. Stripping is string- and paren-aware so titles like
	// tooltip="a, b (c)" are removed whole instead of being truncated at the
	// comma inside the literal (which used to corrupt the line into an
	// unterminated string).
	src = stripInputNamedArgs(src)
	// v3 style: input(defval, "title", input.integer, ...) positional type arg
	// (also the bare-word type form: input(false, "WIP", bool)).
	posTypeRe := regexp.MustCompile(`(input\.[A-Za-z_]+|\binput)\s*\(([^()\n]*?)\s*,\s*(?:input\.[A-Za-z_]+|bool_?|integer_?|int_?|float_?|string_?|color_?|source_?|resolution_?|symbol_?|timeframe_?|session_?|date_?)\s*([,)])`)
	src = posTypeRe.ReplaceAllString(src, "$1($2$3")
	// bare input.integer/input.bool/... as standalone identifiers (v2 style)
	bareInputTypeRe := regexp.MustCompile(`\binput\.(integer|float|bool|string|color|source|resolution|symbol|timeframe|session|date)\b`)
	src = bareInputTypeRe.ReplaceAllString(src, "input")

	// stripping type=/title= args can leave empty arguments ("(a, , b)"):
	// collapse them outside string literals.
	src = collapseEmptyArgs(src)

	// input*(...) with a positional 2nd argument is a title in v3/v4; a
	// following named title= duplicates it. Drop the named title when the call
	// already has a positional title, and strip options=[...] array literals
	// (the engine has no array literal syntax for them).
	src = fixInputDuplicates(src)

	// v3/v4 input(40, defval=40, ...) passes defval both positionally and
	// named: drop the named form.
	src = stripInputDefvalDupes(src)

	// dedupe repeated named args inside a single call (v3/v4 scripts
	// occasionally pass when=/comment= twice).
	src = dedupeNamedArgs(src)

	// var bool x = na / bool x = na is a common v4 idiom; the engine's bool
	// type check rejects na, so initialize such vars to false instead.
	boolVarRe := regexp.MustCompile(`(?m)^(\s*(?:var\s+)?bool\s+[A-Za-z_][A-Za-z0-9_]*\s*=\s*)na(\s*)$`)
	src = boolVarRe.ReplaceAllString(src, "${1}false$2")
	// "x = bool(na)" / "x = bool(true)" call-form declarations: the static
	// type checker infers the call as numeric, poisoning the variable (a
	// later "x := x and y" then fails with "cannot assign int/float to bool
	// variable"). Unwrap the literal.
	boolCallRe := regexp.MustCompile(`(?m)^(\s*(?:var\s+)?[A-Za-z_][A-Za-z0-9_]*\s*:?=[ \t]*)bool\(\s*(na|true|false)\s*\)([ \t]*(?:\/\/.*)?)$`)
	src = boolCallRe.ReplaceAllString(src, "${1}${2}${3}")
	// assignments like "up := na" where up was declared bool earlier: the
	// engine rejects assigning na to a bool var, so rewrite to false. Only
	// lines that already declare/assign the var, and where the whole RHS is na.
	src = rewriteBoolNAAssign(src)

	// ta.crossover(source1 = a, source2 = b) / ta.crossunder named args: the
	// engine builtins accept only positional args, so drop the names.
	src = stripCrossNamedArgs(src)

	// v4 ta.* names that the harness exposes only under ta.* (or that only
	// exist under ta.* in this engine): rewrite the bare call forms.
	for _, pair := range []struct{ bare, namespaced string }{
		{"rising", "ta.rising"},
		{"falling", "ta.falling"},
		{"dev", "ta.dev"},
		{"vwap", "ta.vwap"},
		{"obv", "ta.obv"},
		{"percentile_nearest_rank", "ta.percentile_nearest_rank"},
		{"percentrank", "ta.percentrank"},
	} {
		src = replaceCallsOutsideStrings(src, pair.bare, pair.namespaced)
	}
	// round_to_mintick(x, m) -> math.round(x / m) * m
	src = rewriteRoundToMintick(src)
	// non-English / abbreviated month words inside timestamp strings
	// ("1 Janv 2020", "1 Sept 2023") -> canonical English 3-letter forms.
	monthFix := regexp.MustCompile(`\b(Janv|Janvier|Sept|Septem|Settembre|Ene|Okt|Dez|Mai)\b`)
	fixMonth := map[string]string{
		"Janv": "Jan", "Janvier": "Jan", "Sept": "Sep", "Septem": "Sep",
		"Settembre": "Sep", "Ene": "Jan", "Okt": "Oct", "Dez": "Dec",
		"Mai": "May",
	}
	if monthFix.MatchString(src) {
		parts := splitOutsideStrings(src)
		for i, p := range parts {
			if i%2 == 0 {
				parts[i] = monthFix.ReplaceAllStringFunc(p, func(m string) string {
					if v, ok := fixMonth[m]; ok {
						return v
					}
					return m
				})
			}
		}
		src = strings.Join(parts, "")
	}
	// v4 color(color.red, 80) 2-arg constructor -> color.new(color.red, 80)
	// (color.new is already namespaced and never matches the bare pattern).
	color2Arg := regexp.MustCompile(`\bcolor\s*\(\s*([^,()\n]+?)\s*,\s*([^,()\n]+?)\s*\)`)
	src = partsReplaceOutsideStrings(src, color2Arg, "color.new($1, $2)")
	// str.tonumber(s) -> float(s)
	src = replaceCallsOutsideStrings(src, "str.tonumber", "float")
	// bare ta.obv / obv series identifiers -> calls: ta.obv always becomes a
	// call (the engine hook is ta.obv()); bare obv only when the script does
	// not define its own obv variable ("obv = ta.obv" shadows the builtin).
	src = rewriteIdentifiersToCalls(src, []string{"ta.obv"})
	if !regexp.MustCompile(`(?m)^\s*(?:var\s+)?obv\s*(:=|=)`).MatchString(src) {
		src = rewriteIdentifiersToCalls(src, []string{"obv"})
		src = rewriteBareValueCall(src, "obv", "ta.obv")
	}

	// hline is a reserved word in the engine (level drawing only): drop calls.
	// Must be paren-balanced (nested color.new(...) in the args used to leave
	// a dangling ")" behind that corrupted the following lines).
	src = dropBalancedCalls(src, "hline")

	// strategy.* value identifiers (position_size, netprofit, ...) are
	// registered with the engine as zero-arg hooks; scripts reference them as
	// plain identifiers, so rewrite them into call form. (open_trades is the
	// v5 name; v3/v4 scripts use strategy.opentrades.)
	strategyValueFns := []string{
		"strategy.position_size", "strategy.position_avg_price",
		"strategy.position_entry_name", "strategy.open_trades",
		"strategy.opentrades", "strategy.closedtrades", "strategy.wintrades",
		"strategy.losstrades", "strategy.netprofit", "strategy.grossprofit",
		"strategy.grossloss", "strategy.openprofit", "strategy.equity",
		"strategy.cash", "strategy.max_drawdown", "strategy.alert_message",
	}
	src = rewriteIdentifiersToCalls(src, strategyValueFns)
	// vwap / obv are only rewritten to the ta.* call form when the script does
	// NOT define its own variable of that name ("vwap = ta.vwap(close)" then
	// uses it as a plain series). Same guard as rewriteBareTR. The namespaced
	// form is always turned into a call regardless of the script's own vars
	// ("obv = ta.obv" needs ta.obv() on the RHS).
	src = rewriteIdentifiersToCalls(src, []string{"ta.vwap"})
	if !regexp.MustCompile(`(?m)^\s*(?:var\s+)?vwap\s*(:=|=)`).MatchString(src) {
		src = rewriteIdentifiersToCalls(src, []string{"vwap"})
		src = rewriteBareValueCall(src, "vwap", "ta.vwap")
	}
	src = rewriteIdentifiersToCalls(src, []string{"ta.obv"})
	if !regexp.MustCompile(`(?m)^\s*(?:var\s+)?obv\s*(:=|=)`).MatchString(src) {
		src = rewriteIdentifiersToCalls(src, []string{"obv"})
		src = rewriteBareValueCall(src, "obv", "ta.obv")
	}

	// accdist / ta.accdist and ta.pvt / pvt are zero-arg series hooks in the
	// engine; extracted scripts sometimes reference them as bare identifiers
	// (e.g. "h = accdist"). Skip when the script defines its own variable.
	src = rewriteIdentifiersToCalls(src, []string{"ta.accdist", "ta.pvt"})
	if !regexp.MustCompile(`(?m)^\s*(?:var\s+)?accdist\s*(:=|=)`).MatchString(src) {
		src = rewriteIdentifiersToCalls(src, []string{"accdist"})
	}
	if !regexp.MustCompile(`(?m)^\s*(?:var\s+)?pvt\s*(:=|=)`).MatchString(src) {
		src = rewriteIdentifiersToCalls(src, []string{"pvt"})
	}

	// v3 trade-list method idioms (strategy.opentrades()[0].entry_price(0),
	// strategy.open_trades[0].exit_price(0), strategy.closedtrades().profit(n),
	// ...). The harness exposes the trade lists as counts, so a whole chain
	// "strategy.<list>() [n] . <method>(args)" is collapsed to a scalar
	// proxy: entry_price approximates the position average price, everything
	// else is 0 (the info is not available per-trade). A paren-balanced
	// scanner handles nested calls inside the argument list.
	src = collapseTradeListChains(src)// ta.tr / bare tr as a series identifier (v5: ta.rma(ta.tr, n); v3:
// rma(tr, n)) -> tr(1) call, unless already a call (ta.tr(true)) or a user
// variable assigned in the script.
	src = rewriteBareTRIdent(src)
	src = rewriteBareTR(src)

	// engine exposes pivot functions only under ta.*; v3 scripts call them
	// bare. Rewrite bare pivothigh(/pivotlow( calls to the ta.* form.
	src = replaceCallsOutsideStrings(src, "pivothigh", "ta.pivothigh")
	src = replaceCallsOutsideStrings(src, "pivotlow", "ta.pivotlow")

	// v3 single-arg highest(n)/lowest(n) (source defaults to high/low).
	src = rewriteSingleArgExtrema(src)

	// input.timeframe is not a registered builtin; treat like plain input.
	src = replaceTokensOutsideStrings(src, "input.timeframe", "input")

	// v3 "input.time" used as a positional type argument inside input(...)
	// ("input(timestamp(...), \"Title\", input.time, group=...)" — nested
	// parens defeat the simple posTypeRe above): drop the ", input.time"
	// segment with a paren-aware scanner.
	src = stripInputTimeTypeArg(src)

	// v4 type-constructor idiom: var t = table(na) initializes a table handle
	// with the type constructor; the engine only knows table.new/table.cell,
	// so rewrite bare table( calls to table.new(. (table.new/table.cell are
	// dotted and never matched by the bare-name matcher.)
	src = replaceCallsOutsideStrings(src, "table", "table.new")

	// timestamp(year=..., month=..., day=..., ...) named-args form (seen in
	// converted sources) -> positional timestamp(y, m, d, h, min, sec).
	src = rewriteTimestampNamedArgs(src)

	// forward / self-referencing series (v3 idiom: "x = f(x[1])" or using a
	// var assigned later): declare them with var float before first use.
	src = declareSelfReferencingSeries(src)

	// "color" used as a plain variable name (v3 idiom) collides with the
	// color.* builtin namespace: rename it to colorv.
	src = renameUserColorVar(src)

	// Last-resort repair for genuinely unterminated string literals (author or
	// extraction typos): close an unclosed quote at end of line. Runs last so
	// every earlier rewrite already had a chance to fix quoting.
	src = repairUnterminatedStrings(src)

	// Converted-source artifacts that the per-line pass below cannot catch:
	// import lines (library scripts cannot run standalone), stray backticks,
	// "strategy\n(" declarations split across lines, and multi-line function
	// bodies left dangling by the extraction.
	src = stripImportLines(src)
	src = stripStrayBackticks(src)
	src = joinSplitStrategyDecl(src)

	// Late residue pass: rewriters above (call drops, arg strips, joiners)
	// can leave standalone fragment lines ("a lone )", "na)", ", ..."). The
	// early passes ran before these rewrites existed, so re-run the
	// fragment cleanup once more at the very end.
	src = joinTrailingOperatorLines(src)
	src = fixUnbalancedCloseLines(src)
	src = dropLeadingCommaContinuations(src)

	// "longStopPrice := longStopPrice := if (cond)" — a duplicated reassign
	// prefix left by extraction before an if-expression; drop the dup BEFORE
	// the late if-rewriters run (they only match the single-prefix form).
	// (RE2 has no backreferences: match the shape, compare groups in Go.)
	dupRe := regexp.MustCompile(`(?m)^(\s*)([A-Za-z_][A-Za-z0-9_]*)\s*:=\s*([A-Za-z_][A-Za-z0-9_]*)\s*:=\s*(if\b)`)
	src = dupRe.ReplaceAllStringFunc(src, func(m string) string {
		parts := dupRe.FindStringSubmatch(m)
		if parts == nil || parts[2] != parts[3] {
			return m
		}
		return parts[1] + parts[2] + " := " + parts[4]
	})

	// Re-run the if-expression rewriters late: nested "x := y = if ..."
	// chains and if-assigns created by earlier rewrites only become
	// well-formed now.
	src = rewriteIfExprChain(src)
	src = rewriteIfExprAssign(src)
	src = rewriteIfAssignBlock(src)

	// Switch default arms of the form "=> runtime.error("..."), na" (the
	// comma+na fallback of the enclosing conditional from the ORIGINAL Pine
	// text): the parser's default arm accepts a single expression, so rewrite
	// to a paren-wrapped ternary-style call sequence — runtime.error aborts
	// compilation in real Pine, here the arm simply yields na.
	src = rewriteSwitchErrorDefaultArm(src)
	// "(cond ? x = expr : y)" and "(a ? b : x = expr)" self-assignment
	// branches that the regex rewriters cannot reach (nested ternaries,
	// call-bearing branches): drop the "x = " prefix from assignment-shaped
	// branch values.
	src = rewriteAssignmentBranchValues(src)
	// "price = (close[0] > 25000 ? 25000 : price = close[0])": an in-place
	// self-update inside a ternary branch. The parser sees "=" inside a
	// parenthesized expression; rewrite to an if/else statement pair.
	src = rewriteTernarySelfUpdateStmt(src)
	// "f_security(_sym, _res, _src, _rep) => request.security(...)..." — the
	// one-line arrow function with a long call body must be split so the body
	// is on its own line (the parser's inline body cannot index the result).
	src = splitInlineArrowBodies(src)
	// Un-terminated multiline calls ("strategy( ..." with no closer because
	// the extractor amputated it): close at end of the last arg line so the
	// parser sees a complete call.
	src = closeUnterminatedCalls(src)

	return src
}

// rewriteSwitchErrorDefaultArm rewrites the common "=> runtime.error("..."),
// na" switch default arm (a runtime.error call plus na fallback separated by
// a top-level comma) into "=> na" — the engine evaluates the arm value and
// runtime.error is not a supported builtin.
func rewriteSwitchErrorDefaultArm(src string) string {
	re := regexp.MustCompile(`(?m)^(\s*)=>\s*runtime\.error\((?:[^()]|\([^()]*\))*\)\s*,\s*na\s*$`)
	return re.ReplaceAllString(src, "${1}=> na")
}

// rewriteAssignmentBranchValues drops assignment prefixes inside ternary
// branch values on a single line: "(cond ? a : x = expr)" -> "(cond ? a :
// expr)" and "(cond ? x = expr : y)" -> "(cond ? expr : y)". Only applied
// when the line contains an assignment-shaped branch the parser would
// reject.
func rewriteAssignmentBranchValues(src string) string {
	lines := strings.Split(src, "\n")
	branchRe := regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*:?=[^=](.*)$`)
	for i, ln := range lines {
		if !strings.Contains(ln, "?") || !strings.Contains(ln, "=") {
			continue
		}
		// operate on the top-level paren group of the ternary
		depth := 0
		inStr := false
		quote := byte(0)
		esc := false
		start := -1
		for j := 0; j < len(ln); j++ {
			c := ln[j]
			if inStr {
				if esc {
					esc = false
				} else if c == '\\' {
					esc = true
				} else if c == quote {
					inStr = false
				}
				continue
			}
			switch c {
			case '\'', '"':
				inStr = true
				quote = c
			case '(':
				if depth == 0 && start < 0 {
					start = j
				}
				depth++
			case ')':
				depth--
			}
		}
		_ = start
		// find "ident =" inside the line's top-level ternary segment after '?'
		// or ':' and drop the "ident =" prefix
		for _, marker := range []string{"?", ":"} {
			for {
				idx := strings.Index(ln, marker)
				if idx < 0 {
					break
				}
				seg := ln[idx+1:]
				m := branchRe.FindStringSubmatch(strings.TrimLeft(seg, " \t"))
				if m == nil {
					break
				}
				lead := seg[:len(seg)-len(strings.TrimLeft(seg, " \t"))]
				rest := m[2]
				ln = ln[:idx+1] + lead + rest
			}
		}
		lines[i] = ln
	}
	return strings.Join(lines, "\n")
}

// rewriteTernarySelfUpdateStmt rewrites "x = (cond ? a : x = expr)" (an
// assignment-shaped branch whose target equals the statement's own LHS) into
// an if/else pair:
//
//	x = cond ? a : na
//	if not (cond)
//	    x := expr
func rewriteTernarySelfUpdateStmt(src string) string {
	// RE2 has no backreferences: match the general shape, then verify the
	// branch target equals the statement LHS in Go code.
	re := regexp.MustCompile(`(?m)^(\s*)([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\(([^?\n]+)\?([^:\n]*):\s*([A-Za-z_][A-Za-z0-9_]*)\s*=[^=]\s*([^)\n]+)\)\s*$`)
	return re.ReplaceAllStringFunc(src, func(m string) string {
		parts := re.FindStringSubmatch(m)
		if parts == nil {
			return m
		}
		indent, lhs, cond, tval, target, fval := parts[1], parts[2], parts[3], parts[4], parts[5], parts[6]
		if target != lhs {
			return m
		}
		return indent + lhs + " = " + strings.TrimSpace(cond) + " ? " + strings.TrimSpace(tval) + " : na\n" +
			indent + "if not (" + strings.TrimSpace(cond) + ")\n" +
			indent + "    " + lhs + " := " + strings.TrimSpace(fval)
	})
}

// splitInlineArrowBodies splits one-line function definitions whose body is
// a long expression: "f(a) => request.security(...)..." -> "f(a) =>\n    <body>".
// Only fires when the body contains an indexing suffix the inline parse
// cannot handle.
func splitInlineArrowBodies(src string) string {
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		idx := strings.Index(ln, " => ")
		if idx < 0 {
			continue
		}
		head := ln[:idx]
		// head must look like a function signature: name(params) or name
		if !regexp.MustCompile(`^[ \t]*[A-Za-z_][A-Za-z0-9_]*(\([^()\n]*\))?$`).MatchString(head) {
			continue
		}
		body := strings.TrimSpace(ln[idx+4:])
		if body == "" || strings.HasPrefix(body, "//") {
			continue
		}
		indent := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
		lines[i] = indent + strings.TrimSpace(head) + " =>\n" + indent + "    " + body
	}
	return strings.Join(lines, "\n")
}

// closeUnterminatedCalls appends the missing ")" to a multiline call whose
// parens never balance (extractor amputated the closer): strategy(... with
// args spread over lines but no closing paren line.
func closeUnterminatedCalls(src string) string {
	lines := strings.Split(src, "\n")
	depth := 0
	openStart := -1
	for i, ln := range lines {
		code := codeOnly(ln)
		t := strings.TrimSpace(code)
		if t == "" {
			continue
		}
		surplus := parenSurplus(t)
		if depth == 0 {
			if surplus > 0 && regexp.MustCompile(`^[A-Za-z_]`).MatchString(t) {
				depth = surplus
				openStart = i
			} else {
				continue
			}
		} else {
			depth += surplus
			if depth < 0 {
				depth = 0
				openStart = -1
			}
		}
	}
	if depth > 0 && openStart >= 0 {
		// find last non-blank line from the end and close there
		last := len(lines) - 1
		for last >= 0 && strings.TrimSpace(codeOnly(lines[last])) == "" {
			last--
		}
		if last >= 0 {
			lines[last] = strings.TrimRight(lines[last], " \t\r") + ")"
		}
	}
	return strings.Join(lines, "\n")
}

// stripImportLines drops "import user/lib/N as alias" declarations. Library
// imports cannot resolve in a standalone backtest; the alias calls they
// enable are stripped by a later pass that removes the "alias." prefix from
// call names (unknown aliases otherwise surface as parse errors).
func stripImportLines(src string) string {
	// "as" and the renamed "as_" (the reserved-word pass rewrites the import
	// keyword into an identifier) both appear in converted sources.
	aliasRe := regexp.MustCompile(`(?m)^[ \t]*import\s+[A-Za-z_][A-Za-z0-9_]*/[A-Za-z0-9_]+(?:/\d+)?\s+as_?\s+([A-Za-z_][A-Za-z0-9_]*)[ \t]*$`)
	aliases := aliasRe.FindAllStringSubmatch(src, -1)
	src = aliasRe.ReplaceAllString(src, "")
	// Alias-qualified member calls ("fuLi.getStatus(...)" / "fuLi.func(...)"
	// used both as statements and as expressions) become plain member calls
	// with the alias prefix dropped; the library function itself is not
	// available, so route them through a benign 0-value placeholder call.
	for _, m := range aliases {
		alias := m[1]
		if alias == "" {
			continue
		}
		memberRe := regexp.MustCompile(`\b` + regexp.QuoteMeta(alias) + `\.([A-Za-z_][A-Za-z0-9_]*)\s*\(`)
		src = memberRe.ReplaceAllString(src, "nzlib_")
	}
	return src
}

// dropBalancedCalls removes name(...) calls with a paren-balanced scanner,
// replacing each whole call with "na". Unlike a [^)]* regex this survives
// nested calls inside the argument list (hline(50, "x", color=color.new(#787B86,
// 50)) used to leave a dangling ")" behind). Single-line calls only: a call
// whose parens do not close on the same line is left alone (join passes and
// fixUnbalancedCloseLines handle those).
func dropBalancedCalls(src, name string) string {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*\(`)
	reKeep := regexp.MustCompile(`\x00KEEP\x00`)
	for {
		loc := re.FindStringIndex(src)
		if loc == nil {
			if strings.Contains(src, "\x00") {
				return reKeep.ReplaceAllString(strings.ReplaceAll(src, "\x00SKIP\x00", ""), "")
			}
			return src
		}
		start := loc[0]
		depth := 0
		j := loc[1] - 1
		end := -1
		for ; j < len(src); j++ {
			c := src[j]
			switch c {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = j
				}
			}
			if end >= 0 {
				break
			}
			if c == '\n' && depth > 0 {
				break // multi-line call: leave it alone
			}
		}
		if end < 0 {
			// Unclosed or multi-line: leave the call, but mark this spot so the
			// scanner does not revisit it forever.
			src = src[:loc[0]] + "\x00KEEP\x00" + src[loc[0]:]
			continue
		}
		src = src[:start] + "na" + src[end+1:]
	}
}

// stripStrayBackticks removes stray backquote characters that some converted
// sources carry (the lexer rejects them with "unexpected character '`'").
func stripStrayBackticks(src string) string {
	if !strings.Contains(src, "`") {
		return src
	}
	return strings.ReplaceAll(src, "`", "")
}

// joinSplitStrategyDecl merges a study/strategy/indicator declaration whose
// name and "(" were split across lines by the extraction:
//
//	strategy
//	 ( title=..., overlay=true
//	 )
//
// The lexer sees the bare "strategy" line as a keyword statement and fails
// with "expected IDENT, got =" / "expected ), got =".
func joinSplitStrategyDecl(src string) string {
	re := regexp.MustCompile(`(?m)^([ \t]*)(strategy|study|indicator)[ \t]*\r?\n[ \t]*\(`)
	for {
		loc := re.FindStringIndex(src)
		if loc == nil {
			return src
		}
		lineEnd := strings.IndexByte(src[loc[0]:], '\n')
		if lineEnd < 0 {
			return src
		}
		lineEnd += loc[0]
		// find the "(" on the following line(s): the regex guarantees the next
		// non-blank content after the name line starts with an optional indent
		// then "(" — locate it precisely.
		openIdx := strings.IndexByte(src[lineEnd:loc[1]], '(')
		if openIdx < 0 {
			return src // should not happen; bail safely
		}
		head := src[:lineEnd]
		// Collapse the name line and the following line's indent into "name(":
		// everything between them is whitespace only (guaranteed by the regex
		// requiring only spaces before the open paren).
		src = head + "(" + src[lineEnd+openIdx+1:]
	}
}

// stripTypedArrayParams removes v5 typed-parameter annotations in function
// signatures: "getArrayValue(float[] arr, int ago) =>" ->
// "getArrayValue(arr, ago) =>". The engine has no typed params, and the
// tokens "float[]" / "int" otherwise hit reserved-name guards.
func stripTypedArrayParams(src string) string {
	re := regexp.MustCompile(`\b(float|int|bool|string|color)_?\s*\[\]\s*([A-Za-z_][A-Za-z0-9_]*)`)
	src = re.ReplaceAllString(src, "$2")
	// typed scalar params inside a signature, including the extractor's
	// "float_"/"int_" underscore spellings: "volatility (float_ src,
	// int_ length) =>" -> "volatility(src, length) =>". A whitespace
	// lookahead after the keyword is REQUIRED: without it "bool_Long_EMA200"
	// matched as bool + ident and corrupted the identifier.
	// RE2-safe: consume the separating whitespace instead of a lookahead.
	// "bool_Long_EMA200" still cannot match: "bool" + optional "_" leaves
	// "Long..."/"_..." where [ \t]+ requires whitespace.
	re2 := regexp.MustCompile(`(\([^()\n]*?)\b(?:float|int|bool|string|color)_?[ \t]+([A-Za-z_][A-Za-z0-9_]*)`)
	for {
		next := re2.ReplaceAllString(src, "$1$2")
		if next == src {
			break
		}
		src = next
	}
	// typed declarations of array variables: "float_[] levels = array.new..."
	// -> "levels = array.new..." (the trailing "_[]" form comes from the
	// extractor mangling "float[]").
	re3 := regexp.MustCompile(`(?m)^(\s*)(?:var\s+)?[A-Za-z_]+_\s*\[\]\s*([A-Za-z_][A-Za-z0-9_]*)\s*=`)
	src = re3.ReplaceAllString(src, "$1$2 =")
	return src
}

// dropLoneIdentLines removes lines that consist of a single bare identifier
// ("Stochastic", "UTILITIES" section headers left in by conversions). The
// parser accepts them as expression statements but the runtime then fails
// with "unknown identifier" because nothing defines them.
func dropLoneIdentLines(src string) string {
	re := regexp.MustCompile(`(?m)^[ \t]*[A-Za-z_][A-Za-z0-9_]*[ \t]*\r?$`)
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		if !re.MatchString(ln) {
			continue
		}
		// literal keywords ("true", "false", "na") are legitimate branch
		// values ("if cond\n    true\nelse\n    false"), never section headers
		switch t {
		case "true", "false", "na":
			continue
		}
		// builtin constants and series identifiers are legitimate branch values
		// too ("if(histA_IsUp)\n    aqua" — the branch yields the color): they
		// are used exactly once and never defined, which the used-once heuristic
		// would otherwise flag as a section header.
		switch t {
		case "aqua", "black", "blue", "fuchsia", "gray", "green", "lime",
			"maroon", "navy", "olive", "orange", "purple", "red", "silver",
			"teal", "white", "yellow",
			"open", "high", "low", "close", "volume", "hl2", "hlc3",
			"ohlc4", "hlcc4", "time", "time_close", "bar_index",
			"last_bar_index", "last_bar_time", "math_pi", "math_e":
			continue
		}
		// control-flow keywords are structural ("else", "continue" inside a
		// loop, ...): deleting them amputates the branch or loop body. The
		// used-once heuristic cannot apply to them.
		switch t {
		case "else", "if", "for", "while", "break", "continue",
			"and", "or", "not", "to", "var":
			continue
		}
		// Only drop when the identifier is not defined or assigned anywhere in
		// the script (a bare "continue"-style keyword never reaches here).
		word := t
		defined := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\s*(:=|=|\()`).MatchString(src)
		used := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`).FindAllStringIndex(src, 2)
		if len(used) <= 1 && !defined {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}

// stripInputTimeTypeArg removes a v3 positional ", input.time" type argument
// from input(...) calls whose earlier arguments contain nested parens (the
// simple line-regex pass cannot match across them):
//
//	input(timestamp("2021 01 01"), "Backtest Start Time", input.time, group=g)
//
// becomes input(timestamp("2021 01 01"), "Backtest Start Time", group=g).
func stripInputTimeTypeArg(src string) string {
	re := regexp.MustCompile(`\binput\s*\(`)
	// The captured inner slice excludes the closing paren, so the trailing
	// delimiter after input.time may be absent — make it optional.
	typeArg := regexp.MustCompile(`,\s*input\.time\s*([,)])?`)
	var out strings.Builder
	pos := 0
	for {
		loc := re.FindStringIndex(src[pos:])
		if loc == nil {
			out.WriteString(src[pos:])
			break
		}
		openEnd := pos + loc[1]
		depth := 0
		j := openEnd - 1
		end := -1
		for ; j < len(src); j++ {
			switch src[j] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = j
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			out.WriteString(src[pos : openEnd])
			pos = openEnd
			continue
		}
		inner := src[openEnd:end]
		if strings.Contains(inner, "input.time") {
			// keep the terminating comma/paren captured by the group
			inner = typeArg.ReplaceAllString(inner, "$1")
		}
		out.WriteString(src[pos:openEnd])
		out.WriteString(inner)
		pos = end // keep the closing paren; the next scan resumes there
	}
	return out.String()
}

// fixUnbalancedCloseLines handles lines containing a surplus of ")".
// Tracks the running paren depth of the statement being read: a close-paren
// fragment is joined onto the open statement whenever the running depth is
// positive (the fragment closes a genuinely open multi-line call), and is
// dropped only when the running depth is zero (extractor residue from a
// removed wrapper call). The old previous-line-only check amputated the
// closer of any multi-line call whose argument lines were individually
// balanced, leaving the opening "(" dangling.
func fixUnbalancedCloseLines(src string) string {
	lines := strings.Split(src, "\n")
	depth := 0
	for i := 0; i < len(lines); i++ {
		code := codeOnly(lines[i])
		t := strings.TrimSpace(code)
		if t == "" {
			continue
		}
		surplus := parenSurplus(t)
		if surplus >= 0 {
			depth += surplus
			continue
		}
		// negative surplus: this line closes more than it opens
		if depth > 0 {
			// inside an open multi-line call: join the fragment onto the
			// previous code line so the call closes
			p := i - 1
			for p >= 0 && strings.TrimSpace(codeOnly(lines[p])) == "" {
				p--
			}
			if p >= 0 {
				lines[p] = strings.TrimRight(lines[p], " \t\r") + " " + t
				lines[i] = ""
				depth += surplus
				if depth < 0 {
					depth = 0
				}
				continue
			}
		}
		// depth == 0: close-paren residue from a stripped wrapper — drop
		lines[i] = ""
	}
	return strings.Join(lines, "\n")
}

// codeOnly returns the line with any "//" comment (outside string literals)
// removed, so comment parentheses never distort depth tracking.
func codeOnly(s string) string {
	inStr := false
	quote := byte(0)
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == quote {
				inStr = false
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inStr = true
			quote = c
		case '/':
			if i+1 < len(s) && s[i+1] == '/' {
				return s[:i]
			}
		}
	}
	return s
}

// dropLeadingCommaContinuations removes residue lines that begin with a
// top-level comma (", linestyle=3, linewidth=1)") — leftovers of call
// arguments whose opening was stripped by extraction. They are never valid
// statement starts in Pine.
// dropDeclarationTrailingSlash removes the stray division operator left at
// the end of a declaration call ("study("BRAHMASTRA", overlay=true) /") by
// corrupted banner comments. The next non-blank line starts a new statement,
// so the slash is pure residue.
func dropDeclarationTrailingSlash(src string) string {
	re := regexp.MustCompile(`(?m)^([ \t]*(?:strategy|study|indicator|library)\s*\(.*\))\s*/\s*$`)
	return re.ReplaceAllString(src, "$1")
}

func dropLeadingCommaContinuations(src string) string {
	lines := strings.Split(src, "\n")
	depth := 0
	for i, l := range lines {
		code := codeOnly(l)
		t := strings.TrimSpace(code)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, ",") && parenSurplus(t) <= 0 && depth <= 0 {
			// leading-comma line with NO open call above it: residue of a
		// stripped wrapper call — drop it.
			lines[i] = ""
			continue
		}
		depth += parenSurplus(t)
		if depth < 0 {
			depth = 0
		}
	}
	return strings.Join(lines, "\n")
}

// parenSurplus returns open "(" count minus close ")" count outside strings.
func parenSurplus(s string) int {
	depth := 0
	inStr := false
	quote := byte(0)
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == quote {
				inStr = false
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inStr = true
			quote = c
		case '(':
			depth++
		case ')':
			depth--
		}
	}
	return depth
}

// splitStatementCommas breaks lines that pack several statements on one line
// separated by top-level commas (common in converted sources):
//
//	"var bool a = na, var bool b = na"
//	"isLong := true, TriggerPrice := close"
//	"bgcolor(...), alertcondition(...)"
//
// Pine has no top-level comma operator: any comma outside parens/brackets/
// strings separates statements, so the splitter splits at every top-level
// comma. Commas inside call argument lists are at paren depth > 0 and are
// never touched.
func splitStatementCommas(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines)+8)
	// Running paren depth across lines: inside an open multi-line call the
	// leading comma of a continuation-arg line (", overlay = true") is an
	// ARGUMENT separator, not a statement separator. Splitting it amputated
	// the args from their call ("strategy(title=\"x\"\n       , overlay=true\n       ...").
	depth := 0
	for _, ln := range lines {
		trimmed := strings.TrimLeft(ln, " \t")
		indent := ln[:len(ln)-len(trimmed)]
		if depth > 0 {
			// inside an open call: keep the line whole, just track depth
			depth += parenSurplus(codeOnly(ln))
			if depth < 0 {
				depth = 0
			}
			out = append(out, ln)
			continue
		}
		segs := splitOnTopLevelCommas(ln)
		if len(segs) <= 1 {
			depth += parenSurplus(codeOnly(ln))
			if depth < 0 {
				depth = 0
			}
			out = append(out, ln)
			continue
		}
		// A multi-line switch/function call split across lines can leave a
		// segment of the form "=> runtime.error(...), na" (default arm plus
		// the na fallback of the enclosing conditional). Splitting that
		// amputates the arm from its switch; leave such lines intact.
		if strings.Contains(ln, "=>") {
			out = append(out, ln)
			continue
		}
		for _, seg := range segs {
			out = append(out, indent+strings.TrimSpace(seg))
		}
	}
	return strings.Join(out, "\n")
}

// splitOnTopLevelCommas splits a line at commas that are outside all parens,
// brackets and string literals.
func splitOnTopLevelCommas(ln string) []string {
	var segs []string
	var cur strings.Builder
	depth := 0
	inStr := false
	quote := byte(0)
	esc := false
	for i := 0; i < len(ln); i++ {
		c := ln[i]
		if inStr {
			cur.WriteByte(c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == quote {
				inStr = false
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inStr = true
			quote = c
			cur.WriteByte(c)
		case '(', '[':
			depth++
			cur.WriteByte(c)
		case ')', ']':
			if depth > 0 {
				depth--
			}
			cur.WriteByte(c)
		case ',':
			if depth == 0 {
				segs = append(segs, cur.String())
				cur.Reset()
				continue
			}
			cur.WriteByte(c)
		default:
			cur.WriteByte(c)
		}
	}
	segs = append(segs, cur.String())
	return segs
}


// repairUnterminatedStrings appends a closing quote to any line that ends
// inside an unclosed string literal, so a stray quote (from a typo or a
// botched conversion) cannot swallow the rest of the script.
func repairUnterminatedStrings(src string) string {
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		quote := byte(0)
		esc := false
		j := 0
		for ; j < len(ln); j++ {
			c := ln[j]
			if quote != 0 {
				if esc {
					esc = false
					continue
				}
				if c == '\\' {
					esc = true
					continue
				}
				if c == quote {
					quote = 0
				}
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
				continue
			}
			if c == '/' && j+1 < len(ln) && ln[j+1] == '/' {
				break // rest of line is a comment
			}
		}
		if quote != 0 {
			lines[i] = ln + string(quote)
		}
	}
	return strings.Join(lines, "\n")
}

// joinContinuationLines merges a line that begins with a binary operator
// (or / and / not / + / - / * / / / comparisons / ? / :) onto the previous
// non-blank, non-comment line. Converted v3/v4 sources frequently break a
// long condition across lines with the operator leading, which the engine's
// NEWLINE-terminated statement parser rejects.
func joinContinuationLines(src string) string {
	leadOp := regexp.MustCompile(`^\s*(?:(?:and|or|not)\b|[+*/%<>=!?&|^:]=?)`)
	lines := strings.Split(src, "\n")
	for i := 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		// A line starting with "=>" is a switch default arm, never a
		// continuation: joining it onto the previous case line corrupted
		// 'case => val' + '=>' + block into 'case => val =>' (a dozen
		// switch-based strategies failed on exactly this).
		if strings.HasPrefix(t, "=>") {
			continue
		}
		if !leadOp.MatchString(lines[i]) {
			continue
		}
		// find the previous non-blank line; do not join onto a comment
		prev := i - 1
		for prev >= 0 && strings.TrimSpace(lines[prev]) == "" && !strings.HasPrefix(strings.TrimSpace(lines[prev]), "//") {
			prev--
		}
		if prev < 0 || strings.HasPrefix(strings.TrimSpace(lines[prev]), "//") {
			continue
		}
		lines[prev] += " " + strings.TrimSpace(lines[i])
		lines[i] = ""
	}
	return strings.Join(lines, "\n")
}

// joinTrailingOperatorLines merges a line that ends with a binary operator
// (or / and / + / - / comparisons / ternary / ?) or an open token ( "(", "[",
// ",") onto the following non-blank, non-comment line. Sources frequently
// break a long condition or argument list with the operator at the end of the
// line, which the engine's NEWLINE-terminated statement parser rejects.
func joinTrailingOperatorLines(src string) string {
	trailSymbol := regexp.MustCompile(`(?:[+*/%<>=!?&|^:-]|,|\(|\[)$`)
	trailWord := regexp.MustCompile(`\b(?:and|or|not)$`)
	lines := strings.Split(src, "\n")
	// Running paren depth: inside an open multi-line call, a lone ")" line is
	// the call's legitimate closer, not extraction residue — keep it.
	depth := 0
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		// Conversion artifacts: a statement fragment that is only ")", "]",
		// "na)", "na," etc. left over from a botched extraction. It cannot be
		// repaired by joining (the following line is a fresh statement), so
		// drop it — but ONLY when no call is open above it: inside an open
		// multi-line call a bare ")" line is the legitimate closer, and
		// dropping it left the call unterminated so all following statements
		// were swallowed as arguments.
		if regexp.MustCompile(`^(?:na\s*[),]|[),])$`).MatchString(t) {
			if depth <= 0 {
				lines[i] = ""
			} else {
				depth += parenSurplus(t)
				if depth < 0 {
					depth = 0
				}
			}
			continue
		}
		// Skip lines with inline comments (the trailing token may be comment
		// text, and splicing code after a comment would corrupt it).
		if strings.Contains(t, "//") {
			continue
		}
		// Guard function-definition arrows and lone "=".
		if strings.HasSuffix(t, "=>") {
			continue
		}
		isCont := trailSymbol.MatchString(t) || trailWord.MatchString(t)
		if !isCont {
			depth += parenSurplus(codeOnly(t))
			if depth < 0 {
				depth = 0
			}
			continue
		}
		// Join the next non-blank, non-comment line.
		j := i + 1
		for j < len(lines) {
			nt := strings.TrimSpace(lines[j])
			if nt == "" {
				j++
				continue
			}
			if strings.HasPrefix(nt, "//") {
				j++
				continue
			}
			break
		}
		if j >= len(lines) {
			continue
		}
		// Preserve only the leading indentation of the first line. (Using
		// len-​TrimSpace on CRLF lines would count the trailing \r and
		// wrongly slice the first characters off the statement.)
		lead := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
		lines[i] = lead + t + " " + strings.TrimSpace(lines[j])
		lines[j] = ""
		// The merged line may itself end in an operator; continue from the
		// same index without re-trimming the whole line.
		i--
	}
	return strings.Join(lines, "\n")
}

// dropEmptyBlocks removes "if cond" / "while cond" / "for ..." statements
// whose block is empty: the next code line dedents to the same or a shallower
// level (only comments/blanks in between). The engine's parser rejects an
// empty INDENT block, so the dangling statement line is deleted.
func dropEmptyBlocks(src string) string {
	lines := strings.Split(src, "\n")
	keep := make([]bool, len(lines))
	for i := range lines {
		keep[i] = true
	}
	blockStmt := regexp.MustCompile(`^(\s*)(?:if|while|for|else)\b`)
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "//") || strings.HasSuffix(t, "=>") {
			continue
		}
		m := blockStmt.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		// A bare "else" with an empty body is a no-op branch: the next code
		// line dedents to the else's own level, which the parser rejects
		// ("expected INDENT, got IDENT"). Dropping the dangling else makes the
		// then-branch the effective result, matching Pine's runtime semantics.
		isBareElse := t == "else"
		if isBareElse {
			indent := len(m[1])
			j := i + 1
			for j < len(lines) {
				nt := strings.TrimSpace(lines[j])
				if nt == "" || strings.HasPrefix(nt, "//") {
					j++
					continue
				}
				break
			}
			if j >= len(lines) {
				continue
			}
			nind := len(lines[j]) - len(strings.TrimLeft(lines[j], " \t"))
			if nind <= indent {
				keep[i] = false
			}
			continue
		}
		indent := len(m[1])
		// skip comments/blanks to find the next code line
		j := i + 1
		for j < len(lines) {
			nt := strings.TrimSpace(lines[j])
			if nt == "" || strings.HasPrefix(nt, "//") {
				j++
				continue
			}
			break
		}
		if j >= len(lines) {
			continue
		}
		nind := len(lines[j]) - len(strings.TrimLeft(lines[j], " \t"))
		// a same-bar \"if ... then x\" idiom never appears here (engine has no
		// then), and any real body is indented deeper than the if itself
		if nind <= indent {
			keep[i] = false
		}
	}
	var b strings.Builder
	for i, ln := range lines {
		if keep[i] {
			b.WriteString(ln)
			b.WriteString("\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// rewriteArrowAssignmentBody turns "f(args) => name = expr" into a block form
// ("f(args) =>\n    name = expr\n    name") because the engine's arrow parser
// only accepts an expression after "=>", not an assignment. The transform is
// semantically identical: the assignment is the function's last statement and
// its value is implicitly returned.
func rewriteArrowAssignmentBody(src string) string {
	lines := strings.Split(src, "\n")
	pat := regexp.MustCompile(`^(\s*[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*\s*\()`)
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		if pat.FindStringSubmatch(ln) == nil {
			continue
		}
		// find "=>" at paren depth 0
		depth := 0
		arrow := -1
		for j := 0; j < len(t); j++ {
			switch t[j] {
			case '(':
				depth++
			case ')':
				depth--
			case '=':
				if depth == 0 && j+1 < len(t) && t[j+1] == '>' {
					arrow = j
				}
			}
		}
		if arrow < 0 {
			continue
		}
		rest := strings.TrimSpace(t[arrow+2:])
		if rest == "" || strings.HasPrefix(rest, "//") {
			continue
		}
		// split "name = rhs" at the first top-level '='
		d2 := 0
		eq := -1
		for j := 0; j < len(rest); j++ {
			switch rest[j] {
			case '(', '[':
				d2++
			case ')', ']':
				d2--
			case '=':
				// skip comparison/inequality operators ("==", "!=", "<=",
				// ">=") and the reassignment ":=" form: the char BEFORE the
				// '=' was checked before, but not the one AFTER — so
				// "x==\"Red\"" split into "x = =\"Red\"".
				if d2 == 0 && (j == 0 || rest[j-1] != '=' && rest[j-1] != '!' && rest[j-1] != '<' && rest[j-1] != '>' && rest[j-1] != ':') &&
					(j+1 >= len(rest) || rest[j+1] != '=') {
					eq = j
				}
			}
			if eq >= 0 {
				break
			}
		}
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(rest[:eq])
		rhs := strings.TrimSpace(rest[eq+1:])
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name) {
			continue
		}
		indent := leadingWhitespace(ln)
		head := strings.TrimSpace(t[:arrow])
		tail := []string{
			indent + head + " =>",
			indent + "  " + name + " = " + rhs,
			indent + "  " + name,
		}
		lines = append(lines[:i], append(tail, lines[i+1:]...)...)
	}
	return strings.Join(lines, "\n")
}

func leadingWhitespace(ln string) string {
	i := 0
	for i < len(ln) && (ln[i] == ' ' || ln[i] == '\t') {
		i++
	}
	return ln[:i]
}

// reservedVarNames are engine keywords and type names that the parser
// interprets specially at statement start; scripts sometimes use them as
// variable names ("return = ret", "label = ...", "variant(type, ...)").
var reservedVarNames = []string{
	"return", "break", "continue", "type",
	"int", "float", "bool", "string", "array", "matrix", "map",
	"plot", "hline", "color", "line", "label", "box", "table",
	"linefill", "polyline",
	// Parser keywords rejected as identifiers: "method = input(...)" fails
	// with "expected IDENT, got ="; "as"/"in" are v5 import keywords that
	// converted v3 scripts use as variable names ("as = security(...)").
	"method", "as", "in", "do", "export", "enum", "by",
	// Param/variable named "input" ("IF(input) =>", "x = input * 2"); calls
	// ("input(...)") and namespaces ("input.time") are preserved by the
	// genuine-use guard below.
	"input",
}

// renameKeywordParams rewrites engine-keyword parameter names inside
// user-function signatures and bodies:
//
//	IF(input) => ...            (param named "input" — the parser treats a
//	                            leading "input" param as a decl qualifier)
//	function(source, length) => ("function" as a fn NAME — parseFunction
//	                            expects an identifier after it, so "function"
//	                            followed by "(" fails with "expected IDENT,
//	                            got (")
//
// The whole-file word rename keeps declarations, calls and body references
// consistent.
func renameKeywordParams(src string) string {
	// "function(..." used as a user function name -> "functionf(...)"
	fnRe := regexp.MustCompile(`\bfunction\s*\(`)
	if fnRe.MatchString(src) {
		// rename declaration, calls and (no) body references; "function" is
		// only a valid name in these converted sources, never a builtin.
		src = rewriteWholeWord(src, "function", "functionf")
	}
	// param named "input": "name(input) =>" and body uses of "input"
	if regexp.MustCompile(`\(\s*input\s*\)`).MatchString(src) {
		src = rewriteWholeWord(src, "input", "inputv")
	}
	return src
}

// rewriteWholeWord replaces every whole-word occurrence of word with repl,
// skipping string literal contents.
func rewriteWholeWord(src, word, repl string) string {
	parts := splitOutsideStrings(src)
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`)
	for i, p := range parts {
		if i%2 == 0 {
			parts[i] = re.ReplaceAllString(p, repl)
		}
	}
	return strings.Join(parts, "")
}

// renameReservedVarNames renames variables that shadow engine keywords or
// type names ("return", "type", "label", "line", ...) to a safe spelling
// ("return_", "type_", "label_", ...). Genuine keyword uses are preserved:
// calls ("int(x)"), member access ("label.new(...)", "color.red"), typed
// declarations ("string foo = ..."), named arguments ("input(...,
// type=float)"), and genuine statements ("return expr") are all left as-is.
// Occurrences inside string literals and // comments are never touched.
func renameReservedVarNames(src string) string {
	lines := strings.Split(src, "\n")
	for i := range lines {
		for _, kw := range reservedVarNames {
			lines[i] = replaceReservedVar(kw, lines[i])
		}
	}
	return strings.Join(lines, "\n")
}

func isIdentChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// replaceReservedVar replaces whole-word occurrences of kw on the line except
// where the word is a genuine keyword use.
func replaceReservedVar(kw string, line string) string {
	var b strings.Builder
	i := 0
	for i < len(line) {
		ch := line[i]
		if ch == '\'' || ch == '"' {
			j := i + 1
			for j < len(line) && line[j] != ch {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(line) {
				j = len(line)
			}
			b.WriteString(line[i : min(j+1, len(line))])
			i = j + 1
			continue
		}
		if ch == '/' && i+1 < len(line) && line[i+1] == '/' {
			b.WriteString(line[i:])
			break
		}
		if strings.HasPrefix(line[i:], kw) {
			beforeOK := i == 0 || !isIdentChar(line[i-1])
			afterIdx := i + len(kw)
			afterOK := afterIdx >= len(line) || !isIdentChar(line[afterIdx])
			if beforeOK && afterOK && !reservedWordIsGenuineUse(kw, line, i, afterIdx) {
				b.WriteString(kw + "_")
				i += len(kw)
				continue
			}
		}
		b.WriteByte(ch)
		i++
	}
	return b.String()
}

// reservedWordIsGenuineUse reports whether the kw occurrence starting at pos
// (ending at end) is a real keyword use that must not be renamed.
func reservedWordIsGenuineUse(kw string, line string, pos, end int) bool {
	next := byte(' ')
	if end < len(line) {
		next = line[end]
	}
	// member of a namespace: label.new(...), color.red, table.cell(...),
	// input.float(...) — also backwards so "input.float  (100" (space before
	// the paren) is recognized as a member reference.
	if next == '.' {
		return true
	}
	pm := pos - 1
	for pm >= 0 && (line[pm] == ' ' || line[pm] == '\t') {
		pm--
	}
	if pm >= 0 && line[pm] == '.' {
		return true
	}
	// "for i = 1 to n by step" / "for x in arr" headers: "by"/"in" are the
	// genuine loop keywords here, not variable uses.
	if kw == "by" || kw == "in" {
		if regexp.MustCompile(`\bfor\b`).MatchString(line[:pos]) {
			return true
		}
	}
	// type parameter position: array.new<bool>(), map<string, float> — the
	// word is a genuine type name here, renaming it would corrupt the call.
	if pm >= 0 && line[pm] == '<' {
		return true
	}
	// function call / type conversion / declaration: int(x), plot(x), hline(...)
	if next == '(' {
		return true
	}
	// named argument: input(..., type=float) or v4-style "input(..., type =
	// input)" — skip whitespace on both sides of the word before checking.
	nn := end
	for nn < len(line) && (line[nn] == ' ' || line[nn] == '\t') {
		nn++
	}
	if nn < len(line) && line[nn] == '=' && (nn+1 >= len(line) || line[nn+1] != '=') {
		prev := pos - 1
		for prev >= 0 && (line[prev] == ' ' || line[prev] == '\t') {
			prev--
		}
		if prev >= 0 && (line[prev] == '(' || line[prev] == ',') {
			return true
		}
	}
	// the word is the VALUE of an argument assignment (e.g. "type=float",
	// "defval=color") when it directly follows a single '=' whose preceding
	// char is not also '=' (so "x == float" still gets renamed), AND the word
	// is followed by a value terminator (comma, closing paren/bracket, or end
	// of line). If the word flows into an operator ("ma2 = type ==\"SMA\"")
	// it is a genuine variable use, not an argument value, and gets renamed.
	pv := pos - 1
	for pv >= 0 && (line[pv] == ' ' || line[pv] == '\t') {
		pv--
	}
	if pv >= 0 && line[pv] == '=' && (pv == 0 || line[pv-1] != '=') {
		nn := end
		for nn < len(line) && (line[nn] == ' ' || line[nn] == '\t') {
			nn++
		}
		if nn >= len(line) || line[nn] == ',' || line[nn] == ')' || line[nn] == ']' {
			return true
		}
	}
	// the word is the first token of the line (after leading whitespace): it
	// can be a typed declaration ("string foo = ...") or a statement
	// ("return expr", an indented "break" in a loop), unless it is followed
	// directly by an assignment/index/bare end ("kw =", "kw :=", "kw[",
	// "kw ==...", or a bare "kw") which mark a variable use.
	j := pos
	for j > 0 && (line[j-1] == ' ' || line[j-1] == '\t' || line[j-1] == '\r') {
		j--
	}
	if strings.TrimLeft(line[:j], " \t\r") == "" {
		nn := end
		for nn < len(line) && (line[nn] == ' ' || line[nn] == '\t') {
			nn++
		}
		if nn >= len(line) {
			// bare "kw" at line end: multi-line call arguments were already
			// joined onto their opening line, so a lone keyword on its own
			// line is a statement keyword ("break", "continue") rather than a
			// variable use.
			return true
		}
		if line[nn] == '=' || line[nn] == '[' {
			// "kw :=" is also a variable use ("return := expr" inside a user
			// function): the old check missed the reassignment form, so the
			// identifier stayed "return" and the parser rejected it.
			if line[nn] == '=' && nn+1 < len(line) && line[nn+1] == '=' {
				return false // "kw ==" -> comparison against a variable
			}
			if line[nn] == '=' && kw == "return" {
				return false // "return = expr" -> variable use
			}
			if line[nn] == '[' && kw == "return" {
				return false // "return[1]" -> series reference
			}
			return false // "kw =", "kw[1]" -> variable use
		}
		// reassignment form "kw :=" ("return := ...") is a variable use too
		if line[nn] == ':' && nn+1 < len(line) && line[nn+1] == '=' && kw == "return" {
			return false
		}
		return true // "kw value..." -> genuine decl/statement
	}
	return false
}

// snapIndentation rewrites the leading whitespace of every code line so that
// it exactly matches a level on the block-indent stack, mimicking the lexer's
// INDENT/DEDENT accounting but tolerating off-by-one indentation artifacts
// that appear in converted sources (e.g. a 5-space body line inside a 4-space
// block, or a stray 1-space line after a dedent). New levels are only created
// for indents at least 2 deeper than the current level; anything in between
// snaps to the enclosing level. Blank lines and // comments are untouched.
func snapIndentation(src string) string {
	lines := strings.Split(src, "\n")
	stack := []int{0}
	// a line ending with "=>" opens a block: the next line's indent is a NEW
	// level even when it is only top+1 (a one-space arrow body such as
	// "step(i) =>\n i == 1 ? pd : ..." is a genuine block, not an off-by-one
	// artifact, and merging it left the function with an empty body).
	prevOpensBlock := false
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		indent := 0
		for indent < len(ln) && ln[indent] == ' ' {
			indent++
		}
		// An indent that matches NO stack level and is shallower than the
		// current top is an inexact dedent ("   if" at 3 spaces inside a
		// 4-space block). The lexer would pop past every level and fail with
		// "invalid indentation"; snap UP to the smallest stack level greater
		// than the indent so the line stays inside its innermost enclosing
		// block — the structure the author meant.
		exact := false
		for _, lv := range stack {
			if lv == indent {
				exact = true
				break
			}
		}
		if !exact && indent < stack[len(stack)-1] {
			target := -1
			for _, lv := range stack {
				if lv > indent && (target < 0 || lv < target) {
					target = lv
				}
			}
			if target > 0 {
				lines[i] = strings.Repeat(" ", target) + strings.TrimLeft(ln, " \t")
				indent = target
			}
		}
		for len(stack) > 1 && indent < stack[len(stack)-1] {
			stack = stack[:len(stack)-1]
		}
		top := stack[len(stack)-1]
		if indent > top {
			if indent == top+1 && !prevOpensBlock {
				// off-by-one: treat as part of the enclosing block
				lines[i] = strings.Repeat(" ", top) + strings.TrimLeft(ln, " ")
			} else {
				stack = append(stack, indent)
			}
		} else if indent < top {
			// unreachable given the pop loop, but keep for safety
			lines[i] = strings.Repeat(" ", top) + strings.TrimLeft(ln, " ")
		}
		prevOpensBlock = strings.HasSuffix(trimmed, "=>")
	}
	return strings.Join(lines, "\n")
}

// splitCommaAssignments splits v3 comma-separated top-level assignments like
// "longStop = 0.0, shortStop = 0.0" or "uptrend = false, dntrend = false"
// into separate lines. Only simple scalar RHS values are split; calls and
// expressions containing parens are left untouched so multi-line
// strategy(...) declarations survive.
func splitCommaAssignments(src string) string {
	lines := strings.Split(src, "\n")
	segRe := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*=`)
	for i, ln := range lines {
		if !strings.Contains(ln, ",") {
			continue
		}
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		// split on top-level commas (paren/bracket/string aware) so values that
		// are calls like "len = input(14, title=\"x\")" stay whole
		parts := splitTopLevelCommas(trimmed)
		if len(parts) < 2 {
			continue
		}
		// the FIRST part must be an assignment for this to be a legacy
		// comma-joined statement list ("a = 0, b = 0"). The remaining parts
		// may be assignments or bare expression statements ("TS = highest(...),
		// barssince(...)"), both of which the parser accepts on their own line.
		first := strings.TrimSpace(parts[0])
		if !segRe.MatchString(first) || strings.Contains(first, "? ") {
			continue
		}
		danger := false
		for _, p := range parts[1:] {
			tp := strings.TrimSpace(p)
			if tp == "" || strings.Contains(tp, "? ") {
				danger = true
				break
			}
		}
		if danger {
			continue
		}
		indent := ln[:len(ln)-len(strings.TrimLeft(ln, " \t"))]
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			out = append(out, indent+strings.TrimSpace(p))
		}
		lines[i] = strings.Join(out, "\n")
	}
	return strings.Join(lines, "\n")
}

// dedupeNamedArgs removes duplicate named arguments within a single call,
// keeping the first occurrence. Scans the whole source with string awareness
// so quoted arguments stay attached to their call.
func dedupeNamedArgs(src string) string {
	return dedupeNamedArgsScan(src)
}

func dedupeNamedArgsScan(src string) string {
	var out strings.Builder
	i, n := 0, len(src)
	for i < n {
		c := src[i]
		if c == '"' || c == '\'' {
			// copy string literal verbatim
			j := i + 1
			for j < n {
				if src[j] == '\\' && j+1 < n {
					j += 2
					continue
				}
				if src[j] == c {
					j++
					break
				}
				j++
			}
			out.WriteString(src[i:j])
			i = j
			continue
		}
		if c != '(' {
			out.WriteByte(c)
			i++
			continue
		}
		// find matching close paren, string-aware: string literals may contain
		// unbalanced parens ("...(JPY, XAU...") that must not affect depth.
		depth := 1
		j := i + 1
		esc := false
		inStr := false
		for j < n && depth > 0 {
			c := src[j]
			if inStr {
				if esc {
					esc = false
				} else if c == '\\' {
					esc = true
				} else if c == '"' || c == '\'' {
					inStr = false
				}
				j++
				continue
			}
			if c == '"' || c == '\'' {
				inStr = true
				j++
				continue
			}
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
			}
			j++
		}
		inner := src[i+1 : j-1]
		out.WriteByte('(')
		out.WriteString(dedupeArgsInner(inner))
		out.WriteByte(')')
		i = j
	}
	return out.String()
}

func dedupeArgsInner(inner string) string {
	// split top-level commas (respecting nesting and strings)
	segs := splitTopLevelCommas(inner)
	if len(segs) <= 1 {
		return inner
	}
	seen := map[string]bool{}
	kept := make([]string, 0, len(segs))
	for _, seg := range segs {
		trimmed := strings.TrimSpace(seg)
		name := ""
		if eq := strings.Index(trimmed, "="); eq > 0 {
			cand := strings.TrimSpace(trimmed[:eq])
			if isIdentLike(cand) {
				name = cand
			}
		}
		if name != "" {
			if seen[name] {
				continue
			}
			seen[name] = true
		}
		kept = append(kept, seg)
	}
	return strings.Join(kept, ",")
}

func splitTopLevelCommas(s string) []string {
	var segs []string
	depth := 0
	inStr := false
	esc := false
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' || c == '\'' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"', '\'':
			inStr = true
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				segs = append(segs, s[start:i])
				start = i + 1
			}
		}
	}
	segs = append(segs, s[start:])
	return segs
}

// rewriteIfExprAssign converts legacy v3/v4 multi-statement if-expressions
// used on the right-hand side of an assignment:
//   x := if cond
//       tempA = exprA
//       tempB
//   else
//       tempC
// into a declared variable plus a statement-form if/else that assigns the
// branch's final expression, which the engine's parser accepts:
//   var float x = na
//   if cond
//       tempA = exprA
//       x := tempB
//   else
//       x := tempC
func rewriteIfExprAssign(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines)+8)
	i := 0
	for i < len(lines) {
		ln := lines[i]
		m := regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_]*)\s*(:=|=)\s*if\s+(.+?)\s*$`).FindStringSubmatch(ln)
		if m == nil {
			out = append(out, ln)
			i++
			continue
		}
		indent := m[1]
		name := m[2]
		cond := m[4]
		// collect the indented body (must be deeper than the if line); blank
		// lines INSIDE the body do not end it — the body ends at the first
		// non-blank line that dedents. Breaking at blanks truncated
		// multi-statement bodies ("lastOperation := ...\n\n    longPyramiding
		// := ...\n    true") so the last-but-one assignment was mistaken for
		// the branch value and duplicated the target.
		j := i + 1
		var body []string
		for j < len(lines) {
			t := strings.TrimSpace(lines[j])
			if t == "" {
				// blank: part of the body if the next non-blank line is still
				// indented deeper than the if line
				k := j + 1
				for k < len(lines) && strings.TrimSpace(lines[k]) == "" {
					k++
				}
				if k >= len(lines) {
					break
				}
				ki := len(lines[k]) - len(strings.TrimLeft(lines[k], " \t"))
				if ki <= len(indent) {
					break
				}
				body = append(body, lines[j])
				j++
				continue
			}
			lineIndent := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
			if lineIndent <= len(indent) {
				break
			}
			body = append(body, lines[j])
			j++
		}
		// trailing blanks collected into body are not code
		for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
			body = body[:len(body)-1]
			j--
		}
		elseIdx := -1
		for k := j; k < len(lines); k++ {
			t := strings.TrimSpace(lines[k])
			if t == "" {
				continue
			}
			if t == "else" {
				elseIdx = k
			}
			break
		}
		// need a body to make this safe
		if len(body) < 2 {
			out = append(out, ln)
			i++
			continue
		}
		// Bail when the body is itself a nested if-expression (first line is
		// "if ...") or its value line is nested deeper than the body top —
		// rewriteIfAssignBlock handles those structurally.
		firstTrim := strings.TrimSpace(body[0])
		lastIndent := len(body[len(body)-1]) - len(strings.TrimLeft(body[len(body)-1], " "))
		firstIndent := len(body[0]) - len(strings.TrimLeft(body[0], " "))
		if strings.HasPrefix(firstTrim, "if ") || lastIndent != firstIndent {
			out = append(out, ln)
			i++
			continue
		}
		last := strings.TrimSpace(body[len(body)-1])
		bodyIndent := body[0][:len(body[0])-len(strings.TrimLeft(body[0], " "))]
		if elseIdx < 0 {
			// No else branch: the variable keeps na when the condition is
			// false, so re-declare it every bar (no var persistence).
		out = append(out, indent+"float "+name+" = na")
		out = append(out, indent+"if "+cond)
		for _, bl := range body[:len(body)-1] {
			out = append(out, bl)
		}
		// the branch value line may itself be an assignment ("stopLossPrice
		// = f(...)" or "longStopPrice := if (...)"): emit it verbatim —
		// prefixing the target again produced "x := y = expr" double
		// assignments that cannot parse.
		if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*:?=[^=]`).MatchString(last) {
			out = append(out, bodyIndent+name+" := "+last)
		} else {
			out = append(out, bodyIndent+last)
		}
		i = j
		continue
	}
		// else branch present: use its single value line as the alternate
		alt := ""
		k := elseIdx + 1
		for k < len(lines) && strings.TrimSpace(lines[k]) == "" {
			k++
		}
		if k < len(lines) {
			alt = strings.TrimSpace(lines[k])
		}
		if alt == "" {
			out = append(out, ln)
			i++
			continue
		}
		out = append(out, indent+"var float "+name+" = na")
		out = append(out, indent+"if "+cond)
		for _, bl := range body[:len(body)-1] {
			out = append(out, bl)
		}
		out = append(out, bodyIndent+name+" := "+last)
		out = append(out, indent+"else")
		out = append(out, bodyIndent+name+" := "+alt)
		i = elseIdx + 2
	}
	return strings.Join(out, "\n")
}

// rewriteIfAssignBlock turns a full if-expression assignment whose branches
// are arbitrary (possibly nested if-expression) blocks into pure statement
// form, so the engine's statement parser handles it:
//
//	demaCrossover = if (len2 > 0) and (len3 > 0)
//	    crossover(a, b) and (c > c[1])
//	else
//	    if (len2 > 0) and (len3 == 0)
//	        crossover(demaVal1, demaVal2)
//	    else
//	        crossover(close, demaVal1)
//
// becomes
//
//	float demaCrossover = na
//	if (len2 > 0) and (len3 > 0)
//	    demaCrossover := crossover(a, b) and (c > c[1])
//	else
//	    if (len2 > 0) and (len3 == 0)
//	        if (len3 > 0) and (len2 == 0)
//	            demaCrossover := crossover(demaVal1, demaVal3)
//	        else
//	            demaCrossover := crossover(close, demaVal1)
//
// (the inner if-expression is recursively rewritten on the following pass).
func rewriteIfAssignBlock(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines)+8)
	i := 0
	startRe := regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_]*)\s*(:=|=)\s*if\s+(.+?)\s*$`)
	// Chained form "x := y = if cond ..." (an if-expression assigned to y and
	// then reassigned to x): rewrite to "y = if cond ..." and append
	// "x := y" after the whole construct.
	chainRe := regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_]*)\s*:=\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*if\s+(.*)$`)
	// tuple-destructure form: "[_, upper, lower] = if cond ..." — the arms
	// yield the tuple components, so capture the whole tuple into one var
	// and destructure it after the if/else.
	tupleRe := regexp.MustCompile(`^(\s*)(\[[^\]\n]*\])\s*=\s*if\s+(.+?)\s*$`)
	for i < len(lines) {
		ln := lines[i]
		var m []string
		var tuple string
		var outerTarget string
		if cm := chainRe.FindStringSubmatch(ln); cm != nil {
			outerTarget = cm[2]
			m = []string{"", cm[1], cm[3], "=", cm[4]}
		} else if tm := tupleRe.FindStringSubmatch(ln); tm != nil {
			// normalize to the scalar form with a synthetic target name
			m = []string{"", tm[1], "tupleDest", "=", tm[3]}
			tuple = tm[2]
		} else {
			m = startRe.FindStringSubmatch(ln)
		}
		if m == nil {
			out = append(out, ln)
			i++
			continue
		}
		indent := m[1]
		name := m[2]
		cond := m[4]
		baseIndent := len(indent)
		// Collect the whole construct: body lines deeper than the if line,
		// plus any else / else if arms at the if line's own indent.
		j := i + 1
		end := j
		sawElse := false
		for j < len(lines) {
			t := strings.TrimSpace(lines[j])
			if t == "" {
				j++
				continue
			}
			li := len(lines[j]) - len(strings.TrimLeft(lines[j], " \t"))
			if li < baseIndent {
				break
			}
			if li == baseIndent {
				if t == "else" || strings.HasPrefix(t, "else ") {
					sawElse = true
					j++
					continue
				}
				break
			}
			j++
		}
		end = j
		if !sawElse || end == i+1 {
			out = append(out, ln)
			i++
			continue
		}
		// Re-emit as: float declaration + statement if/else. Within the whole
		// construct, branch header lines (if / else / else if) are structure
		// and pass through verbatim; every other (non-blank) line is a branch
		// value expression of some arm — including values of nested if-arms —
		// and becomes "name := value" at its original indent. Multi-statement
		// Multi-statement
		// arms never reach here (rewriteIfExprAssign consumed them or bailed).
		headerRe := regexp.MustCompile(`^(?:if\b|else\b|for\b|while\b)`)
		assignRe := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\s*:?=[^=]`)
		if outerTarget != "" {
			out = append(out, indent+outerTarget+" := "+name)
			out = append(out, indent+"float "+name+" = na")
		} else if tuple != "" {
			out = append(out, indent+tuple+" = tupleDest")
		} else {
			out = append(out, indent+"float "+name+" = na")
		}
		out = append(out, indent+"if "+cond)
		for k := i + 1; k < end; k++ {
			t := strings.TrimSpace(lines[k])
			if t == "" || strings.HasPrefix(t, "//") {
				continue
			}
			if headerRe.MatchString(t) {
				out = append(out, lines[k])
				continue
			}
			li := len(lines[k]) - len(strings.TrimLeft(lines[k], " \t"))
		// assignment-shaped branch lines are helper statements ("stopLoss
		// Price = f(...)"), not the arm value: pass through verbatim —
		// prefixing produced "name := other = expr" double targets. The
		// (?!=) guard keeps comparisons ("sum_dir == -3") from matching
		// as assignments via the first '=' of '=='.
		if assignRe.MatchString(t) {
			out = append(out, lines[k])
			continue
		}
			out = append(out, lines[k][:li]+name+" := "+t)
		}
		i = end
	}
	return strings.Join(out, "\n")
}

// rewriteForExprAssign turns a for-loop used as an expression value
//
//	gx = for i = 1 to per-1
//	    gmean := gmean * pow(price[i], 1.0/per)
//	    gmean
//
// into the statement form the engine supports (the loop's value is its last
// body expression, so capture it into the variable at the end of each pass):
//
//	float gx = na
//	for i = 1 to per-1
//	    gmean := gmean * pow(price[i], 1.0/per)
//	    gx := gmean
func rewriteForExprAssign(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines)+8)
	i := 0
	forRe := regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(for\b.*)$`)
	for i < len(lines) {
		ln := lines[i]
		m := forRe.FindStringSubmatch(ln)
		if m == nil {
			out = append(out, ln)
			i++
			continue
		}
		indent := m[1]
		name := m[2]
		header := m[3]
		// collect the indented body
		j := i + 1
		var body []string
		for j < len(lines) {
			t := strings.TrimSpace(lines[j])
			if t == "" {
				break
			}
			lineIndent := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
			if lineIndent <= len(indent) {
				break
			}
			body = append(body, lines[j])
			j++
		}
		if len(body) < 2 {
			out = append(out, ln)
			i++
			continue
		}
		last := strings.TrimSpace(body[len(body)-1])
		firstIndent := len(body[0]) - len(strings.TrimLeft(body[0], " "))
		lastIndent := len(body[len(body)-1]) - len(strings.TrimLeft(body[len(body)-1], " "))
		// The value expression must be at the body's top level (not nested in
		// an if inside the loop), and must not itself be an assignment.
		assignish := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\[\]]*\s*:=`) 
		if lastIndent != firstIndent || assignish.MatchString(last) {
			out = append(out, ln)
			i++
			continue
		}
		out = append(out, indent+"float "+name+" = na")
		out = append(out, indent+header)
		for _, bl := range body[:len(body)-1] {
			out = append(out, bl)
		}
		out = append(out, body[0][:firstIndent]+name+" := "+last)
		i = j
	}
	return strings.Join(out, "\n")
}

// stripInputNamedArgs removes call arguments of the form "name = value" for
// the engine-unsupported input() names, scanning the whole source so string
// literals (which may contain commas, parens, and the stripped words) and
// nested calls are handled atomically.
func stripInputNamedArgs(src string) string {
	del := map[string]bool{"minval": true, "maxval": true, "step": true, "group": true, "display": true, "tooltip": true, "confirm": true, "type": true}
	out := make([]byte, 0, len(src)+16)
	i := 0
	for i < len(src) {
		ch := src[i]
		if ch == '\'' || ch == '"' {
			j := i + 1
			for j < len(src) && src[j] != ch {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(src) {
				j = len(src) - 1
			}
			out = append(out, src[i:j+1]...)
			i = j + 1
			continue
		}
		if ch == '/' && i+1 < len(src) && src[i+1] == '/' {
			// copy the comment through end of line, then keep scanning
			j := i
			for j < len(src) && src[j] != '\n' {
				j++
			}
			out = append(out, src[i:j]...)
			i = j
			continue
		}
		if ch == ',' {
			// after deleting ", name=value" the following comma is kept so it
			// still separates the remaining arguments.
			if maybeNamedArgAt(src, i+1, del) {
				i = skipNamedArgValue(src, i+1, false)
				continue
			}
			out = append(out, ch)
			i++
			continue
		}
	if ch == '(' {
		// only when this paren opens a call: previous non-space is ident
		p := i - 1
		for p >= 0 && (src[p] == ' ' || src[p] == '\t' || src[p] == '\r' || src[p] == '\n') {
			p--
		}
		if p >= 0 && isIdentChar(src[p]) {
			if maybeNamedArgAt(src, i+1, del) {
				out = append(out, '(')
				// first argument: also drop the separator comma after it
				i = skipNamedArgValue(src, i+1, true)
				continue
			}
		}
	}
	out = append(out, ch)
	i++
	}
	return string(out)
}

// maybeNamedArgAt reports whether the src at pos begins (after whitespace)
// with "name =" where name is one of the deletable argument names.
func maybeNamedArgAt(src string, pos int, del map[string]bool) bool {
	j := pos
	for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r' || src[j] == '\n') {
		j++
	}
	start := j
	for j < len(src) && isIdentChar(src[j]) {
		j++
	}
	if j == start {
		return false
	}
	if !del[src[start:j]] {
		return false
	}
	k := j
	for k < len(src) && (src[k] == ' ' || src[k] == '\t') {
		k++
	}
	return k < len(src) && src[k] == '='
}

// skipNamedArgValue advances i past "name = value": the value is consumed
// atomically (a string literal, a balanced (...) group, or a bare token up to
// the next comma / closing paren). When eatTrailingComma is set, the comma
// separator that follows the value is consumed too (used for a deleted first
// argument); otherwise it is left in place to separate the remaining args.
func skipNamedArgValue(src string, pos int, eatTrailingComma bool) int {
	j := pos
	for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r' || src[j] == '\n') {
		j++
	}
	// skip the name
	for j < len(src) && isIdentChar(src[j]) {
		j++
	}
	for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
		j++
	}
	if j < len(src) && src[j] == '=' {
		j++
	}
	for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r' || src[j] == '\n') {
		j++
	}
	if j < len(src) && (src[j] == '\'' || src[j] == '"') {
		q := src[j]
		j++
		for j < len(src) && src[j] != q {
			if src[j] == '\\' {
				j++
			}
			j++
		}
		if j < len(src) {
			j++
		}
	} else if j < len(src) && src[j] == '(' {
		depth := 0
		for j < len(src) {
			c := src[j]
			if c == '\'' || c == '"' {
				q := c
				j++
				for j < len(src) && src[j] != q {
					if src[j] == '\\' {
						j++
					}
					j++
				}
			}
			if j >= len(src) {
				break
			}
			c = src[j]
			if c == '(' {
				depth++
			} else if c == ')' {
				depth--
				if depth == 0 {
					j++
					break
				}
			}
			j++
		}
	} else {
		// Bare token value: consume it string- and paren-aware so a function
		// call value ("timestamp(\"01 Jan 1970 00:00 UTC\")") is taken whole.
		// The old scanner stopped at the first ')' — the nested call's own
		// closer — which then leaked into the output as a stray paren and
		// broke the enclosing input() call.
		depth := 0
		for j < len(src) {
			c := src[j]
			if c == '\'' || c == '"' {
				q := c
				j++
				for j < len(src) && src[j] != q {
					if src[j] == '\\' {
						j++
					}
					j++
				}
				if j < len(src) {
					j++
				}
				continue
			}
			if c == '\n' {
				break
			}
			if c == '(' {
				depth++
			} else if c == ')' {
				if depth == 0 {
					// enclosing call's closer: stop without consuming
					break
				}
				depth--
			} else if c == ',' && depth == 0 {
				break
			}
			j++
		}
	}
	// optionally consume the following comma separator (and the whitespace
	// after it, so a removed first argument does not leave a stray space).
	if eatTrailingComma {
		for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\r' || src[j] == '\n') {
			j++
		}
		if j < len(src) && src[j] == ',' {
			j++
			for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
				j++
			}
		}
	}
	return j
}

var roundPrecisionRe = regexp.MustCompile(`\b(math\.round|round)\(([^()\n]*?,\s*)precision\s*=\s*(-?[0-9.]+)\s*(?:,\s*[^()\n]*)?\)`)

// rewriteIfExprChain converts v5 conditional-expression assignments with
// else-if/else chains into nested ternary expressions:
//   x = if c1\n    e1\nelse if c2\n    e2\nelse\n    e3  ->  x = c1 ? e1 : c2 ? e2 : e3
// Only single-expression branch bodies are handled; a missing else yields a
// trailing ": na" branch. Multi-statement bodies are left for
// rewriteIfExprAssign to convert.
func rewriteIfExprChain(src string) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines)+8)
	i := 0
	startRe := regexp.MustCompile(`^(\s*)([A-Za-z_][A-Za-z0-9_]*)\s*(:=|=)\s*if\s+(.+?)\s*$`)
	elseIfRe := regexp.MustCompile(`^(\s*)else\s+if\s+(.+?)\s*$`)
	for i < len(lines) {
		m := startRe.FindStringSubmatch(lines[i])
		if m == nil {
			out = append(out, lines[i])
			i++
			continue
		}
		indent := m[1]
		name := m[2]
		op := m[3]
		branchConds := []string{m[4]}
		var branchVals []string
		// parse the block at the same indent as the if-line
		j := i + 1
		level := indent
		if len(level) < 2 {
			level = strings.Repeat(" ", len(indent)+2)
		}
		curVal := ""
		okBail := false
		for j < len(lines) {
			t := strings.TrimSpace(lines[j])
			if t == "" {
				j++
				continue
			}
			lineIndent := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
			if em := elseIfRe.FindStringSubmatch(lines[j]); em != nil && lineIndent == len(indent) {
				if curVal == "" {
					okBail = true
					break
				}
				branchVals = append(branchVals, curVal)
				branchConds = append(branchConds, em[2])
				curVal = ""
				j++
				continue
			}
			if t == "else" && lineIndent == len(indent) {
				if curVal == "" {
					okBail = true
					break
				}
				branchVals = append(branchVals, curVal)
				branchConds = append(branchConds, "")
				curVal = ""
				j++
				continue
			}
			if lineIndent <= len(indent) {
				break
			}
			// a branch body line: only single-expression bodies are eligible
			if curVal != "" || strings.HasPrefix(t, "//") {
				// second body line or comment inside body -> bail to existing rules
				okBail = true
				break
			}
			if strings.HasPrefix(t, "if ") || strings.Contains(t, "\n") {
				okBail = true
				break
			}
			curVal = t
			j++
		}
		if curVal != "" {
			branchVals = append(branchVals, curVal)
		}
		if okBail || len(branchVals) != len(branchConds) {
			// Bail: re-emit the header AND every line already consumed
			// (i+1..j-1) verbatim — dropping them amputated the else/elseif
			// arms and branch values from the construct, leaving dangling
			// fragments the structural rewriters could no longer repair.
			out = append(out, lines[i])
			for k := i + 1; k < j; k++ {
				out = append(out, lines[k])
			}
			i = j
			continue
		}
		// build the nested ternary
		var b strings.Builder
		b.WriteString(indent + name + " " + op + " ")
		for k := 0; k < len(branchConds); k++ {
			if k > 0 {
				b.WriteString(" : ")
			}
			if branchConds[k] != "" {
				b.WriteString(branchConds[k])
				b.WriteString(" ? ")
			} else {
				b.WriteString("")
			}
			b.WriteString(branchVals[k])
		}
		if branchConds[len(branchConds)-1] != "" {
			b.WriteString(" : na")
		}
		out = append(out, b.String())
		i = j
	}
	return strings.Join(out, "\n")
}

// rewriteTernarySelfAssign collapses the redundant self-assignment idiom
// "x = (cond ? x = a : x = b)" into "x = (cond ? a : b)".
// fixDroppedTernaryCond repairs the v3-converted RSI idiom whose middle
// ternary condition was dropped by the extractor:
//
//	rsi = down == 0 ? 100 : = 0 ? 0 : 100 - (...)
//
// The ": = 0 ?" fragment is a corrupted "down == 0 ?" (the variable name and
// the leading "==" operand were lost). Rebuild it from the first comparison:
// the leading "X == 0 ?" and the dropped one share the left operand, so
// rewrite ": = 0 ?" back to ": X == 0 ?".
func fixDroppedTernaryCond(src string) string {
	re := regexp.MustCompile(`(?m)^([ \t]*[A-Za-z_][A-Za-z0-9_]*\s*:?=?\s*)([A-Za-z_][A-Za-z0-9_]*)\s*(==|!=|>=|<=|>|<)\s*([^?:\n]+?)\s*\?\s*([^:\n]+?)\s*:\s*=\s*([0-9.]+)\s*\?`)
	return re.ReplaceAllString(src, "${1}${2} ${3} ${4} ? ${5} : ${2} ${3} ${6} ?")
}

func rewriteTernarySelfAssign(src string) string {
	// Both branch matches use "=[^=]" so the first '=' of a comparison
	// ("sum_dir == -3") is never consumed as an assignment — consuming it
	// turned "cond ? sum_dir == -3 : sum_dir == -3 and adxup" into
	// "cond ? = -3 : = -3 and adxup".
	re := regexp.MustCompile(`(?m)^(\s*)([A-Za-z_][A-Za-z0-9_]*)\s*=\s*\((.+?)\s*\?\s*[A-Za-z_][A-Za-z0-9_]*\s*=[^=]\s*(.+?)\s*:\s*[A-Za-z_][A-Za-z0-9_]*\s*=[^=]\s*(.+?)\)\s*$`)
	src = re.ReplaceAllString(src, "${1}${2} = (${3} ? ${4} : ${5})")
	// Half-forms where only one branch is a self-assignment INSIDE parens:
	// "x := (cond ? stopValue = a : b)" -> "x := (cond ? a : b)".
	// Paren-delimited and restricted to non-": " (i.e. "? ") openings and
	// ":" terminators that are not "==" comparisons, so genuine ternary
	// chains ("(quad == 1 ? a[1] : quad == 2 ? b[1] : false)") are never
	// touched — the old half form also matched chains opened by a
	// comparison and stripped their "X ==" condition text.
	half := regexp.MustCompile(`\(\s*[A-Za-z_][A-Za-z0-9_.]*\s+[^():\n]*\?\s*[^():\n]*?\?\s*[A-Za-z_][A-Za-z0-9_]*\s*=[^=]\s*([^:()\n]+?)\s*:\s*[^()\n]*?\)`)
	src = half.ReplaceAllStringFunc(src, func(m string) string {
		inner := m[1 : len(m)-1]
		innerRe := regexp.MustCompile(`\?\s*[A-Za-z_][A-Za-z0-9_]*\s*=[^=]\s*([^:\n]+?)\s*:`)
		return "(" + innerRe.ReplaceAllString(inner, "? $1 :") + ")"
	})
	half2 := regexp.MustCompile(`\(\s*[A-Za-z_][A-Za-z0-9_.]*\s+[^()\n]*?\?\s*[^():\n]*:\s*[A-Za-z_][A-Za-z0-9_]*\s*=[^=]\s*([^)\n]+?)\)`)
	src = half2.ReplaceAllStringFunc(src, func(m string) string {
		inner := m[1 : len(m)-1]
		innerRe := regexp.MustCompile(`:\s*[A-Za-z_][A-Za-z0-9_]*\s*=[^=]\s*([^)\n]+?)`)
		return "(" + innerRe.ReplaceAllString(inner, ": $1") + ")"
	})
	// "(cond ? x = a : b) ? ..." no-match leftovers: assignment directly after
	// "? " where the same variable is the statement target (x := ...):
	// "longStopPrice := (useSL and buy_trend ? stopValue = entry - x : 0)"
	// is covered by `half` above. A bare "... ? v = expr : na" without parens
	// at statement level is handled by the same rewrite.
	return src
}

func isIdentLike(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !unicode.IsLetter(r) && r != '_' {
				return false
			}
		} else if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

// rewriteBoolNAAssign rewrites "name := na" (and "name = na") to false for
// identifiers that the file declares as bool-typed, since the engine rejects
// assigning na to a bool variable.
func rewriteBoolNAAssign(src string) string {
	boolVars := map[string]bool{}
	declRe := regexp.MustCompile(`(?m)^\s*(?:var\s+)?bool\s+([A-Za-z_][A-Za-z0-9_]*)\b`)
	for _, m := range declRe.FindAllStringSubmatch(src, -1) {
		boolVars[m[1]] = true
	}
	if len(boolVars) == 0 {
		return src
	}
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 != 0 {
			continue
		}
		for name := range boolVars {
			// only whole-line "name := na" / "name = na" assignments
			re := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(name) + `\s*(:=|=)\s*na\s*$`)
			p = re.ReplaceAllString(p, "${1}"+name+"${2}false")
		}
		parts[i] = p
	}
	return strings.Join(parts, "")
}

// rewriteRoundToMintick rewrites round_to_mintick(x, m) (v4) into the
// equivalent math.round expression.
func rewriteRoundToMintick(src string) string {
	// 2-arg form -> math.round(x / m) * m
	re := regexp.MustCompile(`\b(?:math\.)?round_to_mintick\s*\(\s*([^,()\n]+?)\s*,\s*([^,()\n]+?)\s*\)`)
	src = re.ReplaceAllString(src, "math.round($1 / $2) * $2")
	// bare 1-arg form -> math.round(x) (v4 round_to_mintick(x) with the
	// mintick implied by the chart; the engine has no mintick notion).
	re1 := regexp.MustCompile(`\b(?:math\.)?round_to_mintick\s*\(\s*([^,()\n]+?)\s*\)`)
	return re1.ReplaceAllString(src, "math.round($1)")
}

// rewriteSingleArgExtrema rewrites v3 one-argument extrema calls into the
// two-argument form, e.g. highest(20) -> highest(high, 20).
func rewriteSingleArgExtrema(src string) string {
	for _, fn := range []struct{ name, src string }{
		{"highest", "high"}, {"lowest", "low"},
	} {
		re := regexp.MustCompile(`\b` + fn.name + `\s*\(\s*([^(),\n]+?)\s*\)`)
		src = re.ReplaceAllString(src, fn.name+"("+fn.src+", $1)")
	}
	return src
}

// stripInputDefvalDupes removes a named defval= argument from input-family
// calls that already pass defval positionally (v3/v4 idiom: input(40,
// defval=40, ...) -> input(40, ...)), which the engine rejects as a duplicate
// argument.
// stripInputDefvalDupes removes a named defval= argument from input-family
// calls that already pass defval positionally (v3/v4 idiom: input(40,
// defval=40, ...) -> input(40, ...)), which the engine rejects as a duplicate
// argument.
func stripInputDefvalDupes(src string) string {
	var out strings.Builder
	inputRe := regexp.MustCompile(`(?:input|input\.[A-Za-z_]+)\s*\(`)
	rest := src
	for {
		loc := inputRe.FindStringIndex(rest)
		if loc == nil {
			out.WriteString(rest)
			break
		}
		// skip matches preceded by a word char (mid-identifier)
		abs := len(src) - len(rest) + loc[0]
		if abs > 0 && isWordChar(rune(src[abs-1])) {
			out.WriteString(rest[:loc[1]])
			rest = rest[loc[1]:]
			continue
		}
		j := abs + loc[1] - loc[0] // index right after '('
		depth := 1
		end := -1
		inStr := false
		esc := false
		n := len(src)
		for k := j; k < n; k++ {
			c := src[k]
			if inStr {
				if esc {
					esc = false
				} else if c == '\\' {
					esc = true					} else if c == '"' {
						inStr = false
					}
					continue
				}
				if c == '"' {
					inStr = true
					continue
				}
				switch c {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = k
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			out.WriteString(rest[:loc[1]])
			rest = rest[loc[1]:]
			continue
		}
		inner := src[j:end]
		segs := splitTopLevelCommas(inner)
		kept := make([]string, 0, len(segs))
		hasPositional := false
		for si, seg := range segs {
			t := strings.TrimSpace(seg)
			if t == "" {
				continue
			}
			if si == 0 {
				hasPositional = !isNamedArg(t)
			}
			if isNamedArg(t) {
				name := strings.TrimSpace(t[:strings.Index(t, "=")])
				// named args the engine's plain input builtin does not accept
				// (values may contain parens or span lines, e.g. tooltip text)
				switch name {
				case "minval", "maxval", "step", "group", "display", "tooltip", "confirm", "options", "inline":
					continue
				}
				if si > 0 && hasPositional && name == "defval" {
					continue
				}
			}
			kept = append(kept, seg)
		}
		// write the untouched prefix, the call name without its paren, then the
		// rebuilt argument list
		out.WriteString(rest[:loc[0]])
		out.WriteString(rest[loc[0] : loc[1]-1])
		out.WriteByte('(')
		out.WriteString(strings.Join(kept, ","))
		out.WriteByte(')')
		rest = src[end+1:]
	}
	return out.String()
}

func isNamedArg(seg string) bool {
	eq := strings.Index(seg, "=")
	if eq <= 0 {
		return false
	}
	return isIdentLike(strings.TrimSpace(seg[:eq]))
}

// fixInputDuplicates rewrites input*(...) calls that carry both a positional
// title and a named title= (duplicate) and strips options=[...] literals.
func fixInputDuplicates(src string) string {
	// 1) drop "title = ..." from input calls whose argument list ends with a
	//    positional quoted string (the v3/v4 positional title), e.g.
	//    input.int(0, "Offset", title="Offset") -> input.int(0, "Offset").
	//    The arg list must contain at least one comma so single-arg calls like
	//    input(close, title="Source") are left alone.
	titleRe := regexp.MustCompile(`(input\.[A-Za-z_]+|\binput)\s*\(([^()\n]*?"[^"]*"\s*,)\s*title\s*=\s*(?:"[^"]*"|'[^']*')\s*(,|\))`)
	src = titleRe.ReplaceAllString(src, "$1($2$3")
	titleRe2 := regexp.MustCompile(`(input\.[A-Za-z_]+|\binput)\s*\(([^()\n]*?'[^']*'\s*,)\s*title\s*=\s*(?:"[^"]*"|'[^']*')\s*(,|\))`)
	src = titleRe2.ReplaceAllString(src, "$1($2$3")

	// 2) strip options=[...] named args entirely (engine cannot parse the
	//    array literal).
	optRe := regexp.MustCompile(`,\s*options\s*=\s*\[[^\]\n]*\]`)
	src = optRe.ReplaceAllString(src, "")
	return src
}

// rewriteBareTRIdent rewrites "ta.tr" series identifiers (v5: ta.rma(ta.tr,
// n)) into tr(1), leaving call forms like ta.tr(true) intact.
// collapseTradeListChains rewrites v3 trade-list method idioms like
// strategy.opentrades()[0].entry_price(0), strategy.closedtrades().profit(n)
// or strategy.opentrades().size(i). The harness registers the lists as
// counts, so the whole chain collapses to a scalar proxy: entry_price maps to
// the position average price, every other method to 0. The argument list is
// scanned with paren balancing so nested calls (e.g.
// entry_price(strategy.opentrades() - 1)) do not swallow the closing paren.
// rewriteTimestampNamedArgs rewrites timestamp(name=value, ...) named-arg
// calls into a positional timestamp(y, m, d, hh, mm, ss) call using a
// fixed 2000 base when the named fields are absent.
func rewriteTimestampNamedArgs(src string) string {
	if !strings.Contains(src, "timestamp(") {
		return src
	}
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			parts[i] = rewriteTimestampNamedArgsIn(p)
		}
	}
	return strings.Join(parts, "")
}

func rewriteTimestampNamedArgsIn(src string) string {
	re := regexp.MustCompile(`\btimestamp\s*\(`)
	var out strings.Builder
	pos := 0
	for {
		loc := re.FindStringIndex(src[pos:])
		if loc == nil {
			out.WriteString(src[pos:])
			break
		}
		start := pos + loc[0]
		openEnd := pos + loc[1]
		// scan to the matching close paren. Start at the '(' itself so depth
		// becomes 1 before the closing paren is seen (starting after it used
		// to make depth go negative and the scan never found its match,
		// silently leaving every named-args timestamp un-rewritten).
		depth := 0
		j := openEnd - 1
		end := -1
		for j < len(src) {
			if src[j] == '(' {
				depth++
			} else if src[j] == ')' {
				depth--
				if depth == 0 {
					end = j
					break
				}
			}
			j++
		}
		if depth != 0 || end < 0 {
			out.WriteString(src[pos:])
			break
		}
		inner := src[openEnd:end]
		// only rewrite when it uses named args (contains "year=" etc.)
		if !strings.Contains(inner, "year=") && !strings.Contains(inner, "month=") && !strings.Contains(inner, "day=") {
			out.WriteString(src[pos : end+1])
			pos = end + 1
			continue
		}
		named := map[string]string{}
		for _, seg := range splitTopLevelCommas(inner) {
			kv := strings.SplitN(seg, "=", 2)
			if len(kv) == 2 {
				named[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
			}
		}
		get := func(k string, def string) string {
			if v, ok := named[k]; ok {
				return v
			}
			return def
		}
		args := []string{
			get("year", "2000"), get("month", "1"), get("day", "1"),
			get("hour", "0"), get("minute", "0"), get("second", "0"),
		}
		out.WriteString(src[pos:start])
		out.WriteString("timestamp(")
		out.WriteString(strings.Join(args, ", "))
		out.WriteString(")")
		pos = end + 1
	}
	return out.String()
}

func collapseTradeListChains(src string) string {
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			parts[i] = collapseTradeListChainsIn(p)
		}
	}
	return strings.Join(parts, "")
}

func collapseTradeListChainsIn(src string) string {
	headRe := regexp.MustCompile(`strategy\.(?:open_trades|opentrades|closedtrades)\s*\(\s*\)`)
	var out strings.Builder
	pos := 0
	for {
		loc := headRe.FindStringIndex(src[pos:])
		if loc == nil {
			out.WriteString(src[pos:])
			break
		}
		start := pos + loc[0]
		headEnd := pos + loc[1]
		out.WriteString(src[pos:start])
		i := headEnd
		// optional [n] trade index
		for i < len(src) && src[i] == ' ' {
			i++
		}
		if i < len(src) && src[i] == '[' {
			j := i + 1
			for j < len(src) && src[j] != ']' {
				j++
			}
			if j < len(src) {
				i = j + 1
			}
		}
		// require '.' method()
		for i < len(src) && src[i] == ' ' {
			i++
		}
		if i >= len(src) || src[i] != '.' {
			out.WriteString(src[start:i])
			pos = i
			continue
		}
		i++
		mStart := i
		for i < len(src) && (src[i] == '_' || src[i] >= 'a' && src[i] <= 'z' || src[i] >= 'A' && src[i] <= 'Z' || src[i] >= '0' && src[i] <= '9') {
			i++
		}
		method := src[mStart:i]
		for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
			i++
		}
		if i >= len(src) || src[i] != '(' {
			out.WriteString(src[start:i])
			pos = i
			continue
		}
		// balanced parens for the arg list
		depth := 0
		j := i
		for j < len(src) {
			if src[j] == '(' {
				depth++
			} else if src[j] == ')' {
				depth--
				if depth == 0 {
					j++
					break
				}
			}
			j++
		}
		if method == "entry_price" {
			out.WriteString("strategy.position_avg_price()")
		} else {
			out.WriteString("0")
		}
		pos = j
	}
	return out.String()
}

func rewriteBareTRIdent(src string) string {
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			parts[i] = rewriteIdentToCallGuard(p, "ta.tr", "tr(1)")
		}
	}
	return strings.Join(parts, "")
}

// rewriteIdentToCallGuard is like rewriteIdentToCall but only rewrites when
// the identifier is not followed by "(" (so call forms survive).
func rewriteIdentToCallGuard(src, name, repl string) string {
	var out strings.Builder
	for {
		idx := strings.Index(src, name)
		if idx < 0 {
			out.WriteString(src)
			break
		}
		if idx > 0 && isWordChar(rune(src[idx-1])) {
			out.WriteString(src[:idx+1])
			src = src[idx+1:]
			continue
		}
		tail := idx + len(name)
		j := tail
		for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
			j++
		}
		if j < len(src) && src[j] == '(' {
			out.WriteString(src[:j])
			src = src[j:]
			continue
		}
		if tail < len(src) && isWordChar(rune(src[tail])) {
			out.WriteString(src[:tail])
			src = src[tail:]
			continue
		}
		out.WriteString(src[:idx])
		out.WriteString(repl)
		src = src[tail:]
	}
	return out.String()
}

// rewriteBareTR rewrites bare "tr" identifiers (the v3 true-range series)
// into tr(1) calls, unless the script assigns its own "tr" variable.
func rewriteBareTR(src string) string {
	if regexp.MustCompile(`(?m)^\s*tr\s*(:=|=)`).MatchString(src) {
		return src
	}
	return rewriteIdentToCall(src, "tr")
}

// renameUserColorVar renames a user variable named "color" (v3 idiom:
// "color = ...", collides with the color.* builtin namespace) to "colorv".
// Only scripts that assign to a top-level "color" variable are rewritten, and
// the definition plus bare references are renamed while named-argument keys
// ("plot(x, color=...)") and dotted access (color.red) are left intact.
func renameUserColorVar(src string) string {
	// quick scan: is there a top-level assignment "color = ..."?
	hasVar := false
	assignRe := regexp.MustCompile(`(?m)^\s*color\s*(:=|=)\s`)
	hasVar = assignRe.MatchString(src)
	if !hasVar {
		return src
	}
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			p = renameUserColorInSegment(p)
			parts[i] = p
		}
	}
	return strings.Join(parts, "")
}

// renameUserColorInSegment renames "color" to "colorv" when used as a bare
// variable, but leaves named-argument keys ("color = ...") intact. The
// top-level definition "color = ..." is itself a named-arg key pattern, so it
// is detected by its position at the start of a line. A line that begins with
// "color =" but continues a multi-line call (previous line ends with ',' or
// '(') is a named argument, not a definition, and must be left intact.
func renameUserColorInSegment(src string) string {
	lines := strings.Split(src, "\n")
	defRe := regexp.MustCompile(`^\s*color\s*(:=|=)`)
	prevContinues := false
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		isContinuation := strings.HasSuffix(t, ",") || strings.HasSuffix(t, "(")
		if defRe.MatchString(ln) && !prevContinues {
			// rename the definition token only, then bare references on the line
			ln = regexp.MustCompile(`^\s*color\s*(:=|=)`).ReplaceAllStringFunc(ln, func(m string) string {
				return strings.Replace(m, "color", "colorv", 1)
			})
			ln = renameBareToken(ln, "color", "colorv")
			lines[i] = ln
		}
		prevContinues = isContinuation
	}
	return strings.Join(lines, "\n")
}

// replaceCallsOutsideStrings rewrites occurrences of a bare function-call
// name (name followed by "(") outside string literals, honoring word
// boundaries so identifiers like "pivothighX" are untouched.
func replaceCallsOutsideStrings(src, name, repl string) string {
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			p = replaceCallToken(p, name, repl)
			parts[i] = p
		}
	}
	return strings.Join(parts, "")
}

func replaceCallToken(src, name, repl string) string {
	var out strings.Builder
	for {
		idx := strings.Index(src, name)
		if idx < 0 {
			out.WriteString(src)
			break
		}
		if idx > 0 && (isWordChar(rune(src[idx-1])) || src[idx-1] == '.') {
			// part of a longer identifier or already namespaced (ta.pivothigh)
			out.WriteString(src[:idx+1])
			src = src[idx+1:]
			continue
		}
		tail := idx + len(name)
		if tail < len(src) && src[tail] == '(' {
			out.WriteString(src[:idx])
			out.WriteString(repl)
			src = src[idx+len(name):]
			continue
		}
		out.WriteString(src[:tail])
		src = src[tail:]
	}
	return out.String()
}

func renameBareToken(src, token, repl string) string {
	var out strings.Builder
	for {
		idx := strings.Index(src, token)
		if idx < 0 {
			out.WriteString(src)
			break
		}
		before := idx == 0 || !isWordChar(rune(src[idx-1]))
		tail := idx + len(token)
		after := tail < len(src) && isWordChar(rune(src[tail]))
		// skip if part of a word, a namespace access (color.red), or a
		// named-argument key ("color=" inside a call)
		dotted := tail < len(src) && src[tail] == '.'
		namedArg := tail < len(src) && src[tail] == '='
		if !before || after || dotted || namedArg {
			out.WriteString(src[:idx+1])
			src = src[idx+1:]
			continue
		}
		out.WriteString(src[:idx])
		out.WriteString(repl)
		src = src[tail:]
	}
	return out.String()
}

// declareSelfReferencingSeries injects "var float NAME = na" declarations for
// top-level series that are referenced with a history offset ([n]) either in
// their own initializer ("s = s[1] + x") or before their first assignment
// ("a = b[1]" where "b = ..." comes later). The engine resolves identifiers
// strictly in order, and these v3-era idioms rely on implicit series state.
func declareSelfReferencingSeries(src string) string {
	assigned := map[string]bool{}
	varDeclared := map[string]bool{}
	lines := strings.Split(src, "\n")
	identRe := regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\s*\[`)
	assignRe := regexp.MustCompile(`^\s*(?:var\s+)?(?:[A-Za-z_][A-Za-z0-9_]*\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=[^=]`)
	for _, ln := range lines {
		t := ln
		if m := assignRe.FindStringSubmatch(t); len(m) == 2 {
			assigned[m[1]] = true
		}
		if strings.Contains(t, "var ") {
			if m := regexp.MustCompile(`^\s*var\s+([A-Za-z_][A-Za-z0-9_]*)\s`).FindStringSubmatch(t); len(m) == 2 {
				varDeclared[m[1]] = true
			}
		}
	}
	// a name needs explicit declaration if it is referenced with [n] AND is
	// assigned somewhere in the file, but is not already var-declared.
	needDecl := map[string]bool{}
	for i, ln := range lines {
		for _, m := range identRe.FindAllStringSubmatch(ln, -1) {
			name := m[1]
			if assigned[name] && !varDeclared[name] {
				needDecl[name] = true
				_ = i
			}
		}
	}
	if len(needDecl) == 0 {
		return src
	}
	names := make([]string, 0, len(needDecl))
	for n := range needDecl {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	// keep any //@version header first; the engine tolerates comments above it
	// but some scripts rely on the header being near the top.
	idx := 0
	for idx < len(lines) && (strings.TrimSpace(lines[idx]) == "" || strings.HasPrefix(strings.TrimSpace(lines[idx]), "//")) {
		idx++
	}
	for _, ln := range lines[:idx] {
		b.WriteString(ln + "\n")
	}
	b.WriteString("// injected: legacy self-referencing series declarations\n")
	for _, n := range names {
		b.WriteString("var float " + n + " = na\n")
	}
	for _, ln := range lines[idx:] {
		b.WriteString(ln + "\n")
	}
	return b.String()
}

// rewriteIdentifiersToCalls rewrites bare identifier references (not already
// followed by an open paren) of the given dotted names into zero-arg calls.
func rewriteIdentifiersToCalls(src string, names []string) string {
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			for _, name := range names {
				p = rewriteIdentToCall(p, name)
			}
		}
		parts[i] = p
	}
	return strings.Join(parts, "")
}

func rewriteIdentToCall(src, name string) string {
	var out strings.Builder
	for {
		idx := strings.Index(src, name)
		if idx < 0 {
			out.WriteString(src)
			break
		}
		// must not be preceded by a word char or a dot ("ta.obv" contains
		// "obv"; rewriting after the dot would double the namespace)
		if idx > 0 && (isWordChar(rune(src[idx-1])) || src[idx-1] == '.') {
			out.WriteString(src[:idx+1])
			src = src[idx+1:]
			continue
		}
		tail := idx + len(name)
		// skip if already a call: next non-space char is '('
		j := tail
		for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
			j++
		}
		if j < len(src) && src[j] == '(' {
			out.WriteString(src[:j])
			src = src[j:]
			continue
		}
		// assignment target ("vwap = ..."): the script defines its own
		// variable, so this is not a call to rewrite. A lone "=" (not "=="
		// or ":=") marks an assignment LHS.
		if j < len(src) && src[j] == '=' && !(j+1 < len(src) && (src[j+1] == '=' || src[j+1] == '>')) {
			out.WriteString(src[:tail])
			src = src[tail:]
			continue
		}
		if j < len(src) && src[j] == ':' && j+1 < len(src) && src[j+1] == '=' {
			out.WriteString(src[:tail])
			src = src[tail:]
			continue
		}
		// must not be followed by a word char
		if tail < len(src) && isWordChar(rune(src[tail])) {
			out.WriteString(src[:tail])
			src = src[tail:]
			continue
		}
		out.WriteString(src[:idx])
		out.WriteString(name + "()")
		src = src[tail:]
	}
	return out.String()
}

// rewriteBareValueCall rewrites a bare zero-arg call "name()" (as produced
// by rewriteIdentifiersToCalls for a plain series use) into the namespaced
// engine hook "ns.name()". Only zero-arg calls are touched so real calls
// with arguments (already namespaced by replaceCallsOutsideStrings) and
// member uses like "ta.obv" inside "ta.obv()" are left alone.
func rewriteBareValueCall(src, name, ns string) string {
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			// Require the name to NOT follow a dot or identifier char, so
			// "ta.obv()" is not mangled into "ta.ta.obv()".
			re := regexp.MustCompile(`([^A-Za-z0-9_.]|\A)` + regexp.QuoteMeta(name) + `\s*\(\s*\)`)
			p = re.ReplaceAllString(p, "${1}"+ns+"()")
			parts[i] = p
		}
	}
	return strings.Join(parts, "")
}

// collapseEmptyArgs removes empty arguments left behind after stripping
// named args (e.g. "input(title=\"x\", , defval=5)" -> "input(title=\"x\", defval=5)").
// Only touches content outside string literals.
func collapseEmptyArgs(src string) string {
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			// Same-line whitespace only ([ \t], NOT \s): the \\s* form crossed
			// newlines and ate the leading comma of each continuation-arg line
			// of a multi-line call ("strategy(title=\"x\"\n       , overlay=true"
			// lost the ","), orphaning the argument lines.
			p = regexp.MustCompile(`\([ \t]*,`).ReplaceAllString(p, "(")
			p = regexp.MustCompile(`,[ \t]*,`).ReplaceAllString(p, ",")
			p = regexp.MustCompile(`,[ \t]*\)`).ReplaceAllString(p, ")")
			parts[i] = p
		}
	}
	return strings.Join(parts, "")
}

// rewriteIff converts every iff(a, b, c) call into (a ? b : c), handling
// nested parens, whitespace after the name ("iff ("), and newlines.
func rewriteIff(src string) string {
	times := 0
	iffRe := regexp.MustCompile(`\biff\s*\(`)
	for {
		loc := iffRe.FindStringIndex(src)
		if loc == nil {
			break
		}
		idx := loc[1] - 1 // position of the '('
		// skip occurrences inside string literals
		if insideString(src, idx) {
			src = src[:loc[0]] + "iffX" + src[loc[1]-1:]
			times++
			if times > 200 {
				break
			}
			continue
		}
		depth := 0
		end := -1
		for i := idx + 1; i < len(src); i++ {
			switch src[i] {
			case '(':
				depth++
			case ')':
				if depth == 0 {
					end = i
				} else {
					depth--
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			src = src[:loc[0]] + "iffX" + src[loc[1]-1:]
			times++
			if times > 200 {
				break
			}
			continue
		}
		inner := src[idx+1 : end]
		args := splitTopLevelArgs(inner)
		if len(args) == 3 {
			repl := "(" + strings.TrimSpace(args[0]) + " ? " + strings.TrimSpace(args[1]) + " : " + strings.TrimSpace(args[2]) + ")"
			// replace from the start of the iff name so no orphaned name (or
			// whitespace before the paren) is re-matched by the next pass
			src = src[:loc[0]] + repl + src[end+1:]
		} else {
			src = src[:loc[0]] + "iffX" + src[loc[1]-1:]
		}
		times++
		if times > 5000 {
			break
		}
	}
	return src
}

func insideString(src string, idx int) bool {
	inStr := false
	esc := false
	for i := 0; i < idx; i++ {
		c := src[i]
		if inStr {
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		} else if c == '"' {
			inStr = true
		}
	}
	return inStr
}

func splitTopLevelArgs(s string) []string {
	var out []string
	depth := 0
	cur := ""
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			cur += string(c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			cur += string(c)
			continue
		}
		switch c {
		case '(':
			depth++
			cur += string(c)
		case ')':
			depth--
			cur += string(c)
		case ',':
			if depth == 0 {
				out = append(out, cur)
				cur = ""
			} else {
				cur += string(c)
			}
		default:
			cur += string(c)
		}
	}
	if strings.TrimSpace(cur) != "" {
		out = append(out, cur)
	}
	return out
}

// replaceColorWord replaces a bare color word token with color.<word> unless
// it is already preceded by a dot or word char (namespaces, identifiers).
func replaceColorWord(src, token string) string {
	re := regexp.MustCompile(`(^|[^.\w])` + token + `\b`)
	return re.ReplaceAllString(src, "${1}color."+token)
}

// ---------------------------------------------------------------------------
// string-aware helpers

// partsReplaceOutsideStrings applies re to the non-string portions of src.
func partsReplaceOutsideStrings(src string, re *regexp.Regexp, repl string) string {
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			parts[i] = re.ReplaceAllString(p, repl)
		}
	}
	return strings.Join(parts, "")
}

func replaceOutsideStrings(src, old, new string) string {
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			parts[i] = strings.ReplaceAll(p, old, new)
		}
	}
	return strings.Join(parts, "")
}

func replaceTokensOutsideStrings(src, token, new string) string {
	parts := splitOutsideStrings(src)
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(token) + `\b`)
	for i, p := range parts {
		if i%2 == 0 {
			parts[i] = re.ReplaceAllString(p, new)
		}
	}
	return strings.Join(parts, "")
}

// replaceTokenOutsideStrings replaces a dotted-full token only when it is
// not followed by a word character, so "plot.style_line" does not match
// inside "plot.style_linebr" and "hl" does not match inside "close".
func replaceTokenOutsideStrings(src, token, new string) string {
	parts := splitOutsideStrings(src)
	for i, p := range parts {
		if i%2 == 0 {
			parts[i] = replaceToken(p, token, new)
		}
	}
	return strings.Join(parts, "")
}

func replaceToken(src, token, new string) string {
	var out strings.Builder
	for {
		idx := strings.Index(src, token)
		if idx < 0 {
			out.WriteString(src)
			break
		}
		tail := idx + len(token)
		if tail < len(src) && isWordChar(rune(src[tail])) {
			// token is a prefix of a longer identifier; keep it verbatim and
			// advance past the token so the remainder can be rescanned
			out.WriteString(src[:tail])
			src = src[tail:]
			continue
		}
		out.WriteString(src[:idx])
		out.WriteString(new)
		src = src[tail:]
	}
	return out.String()
}

func isWordChar(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// splitOutsideStrings splits src into alternating non-string / string parts.
// Even indices are outside string literals, odd indices are inside.
func splitOutsideStrings(src string) []string {
	var parts []string
	var cur strings.Builder
	inStr := false
	quote := byte(0)
	esc := false
	for _, r := range src {
		c := byte(r)
		if r > 127 {
			c = 0
		}
		if inStr {
			cur.WriteRune(r)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == quote {
				inStr = false
				quote = 0
				parts = append(parts, cur.String())
				cur.Reset()
			}
		} else {
			if c == '"' || c == '\'' {
				if cur.Len() > 0 {
					parts = append(parts, cur.String())
					cur.Reset()
				}
				cur.WriteRune(r)
				inStr = true
				quote = c
			} else {
				cur.WriteRune(r)
			}
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// stripLineComments removes inline "// ..." comments (to end of line)
// outside string literals, preserving quoted text like URLs and titles.
func stripLineComments(src string) string {
	var b strings.Builder
	inStr := false
	esc := false
	i, n := 0, len(src)
	for i < n {
		c := src[i]
		if inStr {
			b.WriteByte(c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			i++
			continue
		}
		if c == '/' && i+1 < n && src[i+1] == '/' {
			// comment to end of line
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		if c == '\'' {
			// Single-quoted strings ARE valid Pine string literals: JSON alert
			// messages like message='{"action":"x"} // note') carry a "//" INSIDE
			// the quotes, and stripping it as a comment ate the closing paren.
			// Treat the apostrophe as a string opener only when a matching
			// apostrophe appears later on the SAME line (a comment like "// don't
			// buy" has an unpaired apostrophe and stays a comment).
			lineEnd := i
			for lineEnd < n && src[lineEnd] != '\n' {
				lineEnd++
			}
			pair := -1
			for k := i + 1; k < lineEnd; k++ {
				if src[k] == '\'' {
					if k+1 < lineEnd && src[k+1] == '\'' {
						k++ // escaped '' pair
						continue
					}
					pair = k
					break
				}
			}
			if pair > 0 {
				// copy the string literal verbatim
				for ; i <= pair; i++ {
					b.WriteByte(src[i])
				}
				continue
			}
			b.WriteByte(c)
			i++
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// stripCrossNamedArgs rewrites "(ta.)crossover(source1 = a, source2 = b)"
// and "(ta.)crossunder(...)" named-arg forms into positional calls, since
// the engine builtins accept only positional arguments.
func stripCrossNamedArgs(src string) string {
	// line-based: named args appear as "source1 =", "source2 =". Multi-line
	// calls are already joined by an earlier stage, so each call is on one
	// line.
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		if !strings.Contains(ln, "source1") && !strings.Contains(ln, "source2") {
			continue
		}
		if !strings.Contains(ln, "crossover") && !strings.Contains(ln, "crossunder") {
			continue
		}
		segments := splitTopLevelCommas(ln)
		changed := false
		for _, seg := range segments {
			if strings.Contains(seg, "source1") || strings.Contains(seg, "source2") {
				changed = true
			}
		}
		var out []string
		for _, seg := range segments {
			seg = regexp.MustCompile(`(?i)\bsource[12]\s*=\s*`).ReplaceAllString(seg, "")
			out = append(out, seg)
		}
		if changed {
			lines[i] = strings.Join(out, ",")
		}
	}
	return strings.Join(lines, "\n")
}

// stripStrayTransp drops a trailing ",transp=N" that sits outside a closed
// call paren (an extraction artifact like "plot(...),transp=0").
func stripStrayTransp(src string) string {
	var b strings.Builder
	inStr := false
	esc := false
	i, n := 0, len(src)
	for i < n {
		c := src[i]
		if inStr {
			b.WriteByte(c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			i++
			continue
		}
		if c == ')' && i+2 < n {
			// look ahead for ",transp=<num>" immediately after the paren
			j := i + 1
			k := j
			for k < n && (src[k] == ' ' || src[k] == '\t') {
				k++
			}
			if k+1 < n && src[k] == ',' {
				m := k + 1
				mm := m
				for mm < n && (src[mm] == ' ' || src[mm] == '\t') {
					mm++
				}
				if mm+8 < n && src[mm:mm+7] == "transp=" {
					e := mm + 7
					for e < n && src[e] >= '0' && src[e] <= '9' {
						e++
					}
					// only consume if it was a bare arg (not part of an
					// identifier) and ends cleanly
					if e > mm+7 {
						b.WriteByte(')')
						i = e
						continue
					}
				}
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func stripBlockComments(src string) string {
	var b strings.Builder
	inStr := false
	esc := false
	inBlock := false
	i, n := 0, len(src)
	for i < n {
		c := src[i]
		if inBlock {
			if c == '*' && i+1 < n && src[i+1] == '/' {
				inBlock = false
				i += 2
			} else {
				i++
			}
			continue
		}
		if inStr {
			b.WriteByte(c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			i++
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			i++
			continue
		}
	if c == '/' && i+1 < n && src[i+1] == '/' {
		// Line comment: copy verbatim to end of line. Critical: must win over
		// the /* check below, otherwise '//*****' decorative rules are
		// misread as block-comment openers and corrupt the line.
		for i < n && src[i] != '\n' {
			b.WriteByte(src[i])
			i++
		}
		continue
	}
	if c == '/' && i+1 < n && src[i+1] == '*' {
		inBlock = true
		i += 2
		continue
	}
	b.WriteByte(c)
	i++
	}
	return b.String()
}

func applyLinewise(src string, fn func(string) string) string {
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		lines[i] = fn(ln)
	}
	return strings.Join(lines, "\n")
}