package main

import (
	"strings"
	"testing"
)

func TestSplitCommaAssignments(t *testing.T) {
	cases := []struct{ in, want string }{
		{"src = close, lenrsi = 3, lenupdown = 2\n", "src = close\nlenrsi = 3\nlenupdown = 2\n"},
		{"uptrend = false, dntrend = false\n", "uptrend = false\ndntrend = false\n"},
		{"longStop = 0.0, shortStop = 0.0\n", "longStop = 0.0\nshortStop = 0.0\n"},
		// must NOT split these:
		{"strategy(title = \"x\", overlay=true)\n", "strategy(title = \"x\", overlay=true)\n"},
		{"x = f(a, b)\n", "x = f(a, b)\n"},
		{"color = pos == 1 ? a : b, y = 2\n", "color = pos == 1 ? a : b, y = 2\n"},
	}
	for _, c := range cases {
		got := splitCommaAssignments(c.in)
		if got != c.want {
			t.Errorf("in=%q\n got=%q\nwant=%q", c.in, got, c.want)
		}
	}
}

func TestPreprocessKeepsStudyDecl(t *testing.T) {
	src := "/*backtest\nstart: 2021-05-08 00:00:00\nend: 2022-05-07 23:59:00\nperiod: 4h\nbasePeriod: 15m\nexchanges: [{\"eid\":\"Futures_Binance\",\"currency\":\"BTC_USDT\"}]\n*/\n// This source code is subject to the terms of the Mozilla Public License 2.0 at https://mozilla.org/MPL/2.0/\n\n//@version=4\nstudy(\"Renko count\", overlay=false)\n\n\ncounter_green = iff(close > open, 1, 0)\ncounter_red = iff(close > open, 0, 1)\n\nsize = (bar_index>39 ? 40 : bar_index+1)\n// function to create a rolling sum and return an integer value using series and series objects\npine_sum(x, y) =>\n    sum = 0.0\n    for i = 0 to y - 1\n        sum := sum + x[i]\n    sum\n\nbrick_red = pine_sum(counter_red, size)\nbrick_green = pine_sum(counter_green, size)\nif cross(brick_red, brick_green)\n    label.new(bar_index, brick_red, style = label.style_arrowdown, size = size.normal, xloc =xloc.bar_index, color = color.green)\n\nplot(brick_red, color = color.red)\nplot(brick_green, color = color.green)\nif brick_red\n    alert(\"red\")\nif brick_green\n    alert(\"green\")\n"
	out := preprocess(src)
	if !strings.Contains(out, "study(\"Renko count\", overlay=false)") {
		t.Fatalf("study decl mangled:\n%s", out)
	}
	if strings.Contains(out, "study()\"Renko count\"") {
		t.Fatalf("study decl mangled (paren moved):\n%s", out)
	}
	if !strings.Contains(out, "style = 14") {
		t.Fatalf("label.style_arrowdown not mapped:\n%s", out)
	}
}
