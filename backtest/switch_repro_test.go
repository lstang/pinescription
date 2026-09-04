package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestSwitchDefaultRepro(t *testing.T) {
	cases := []struct {
		name, src string
	}{
		{"stmt_default_block", `
//@version=5
bull = switch src
    "Close" => close
    "High/Low" => high
    =>
        runtime.error("Invalid source input")
        na
plot(bull)
`},
		{"expr_default_inline", `
f_ma(s, l) =>
    switch s
        "SMA" => ta.sma(l, 2)
        "TEMA" => ta.ema(l, 2)
        => runtime.error("x")
plot(f_ma("SMA", close, 5))
`},
		{"expr_default_block", `
f_ma(s, l) =>
    switch s
        "SMA" => ta.sma(l, 2)
        "TEMA" => ta.ema(l, 2)
        =>
            runtime.error("x")
            0.0
plot(f_ma("SMA", close, 5))
`},
		{"expr_int_arm", `
n = switch tf
    60 => 7
    =>
        int(4)
plot(n)
`},
		{"stmt_default_inline", `
x = switch tf
    60 => 1
    => 0
plot(x)
`},
	}
	for _, c := range cases {
		src := strings.ReplaceAll(c.src, "    ", "\t")
		prepared := preprocess(src)
		sim := NewSim(nil, defDecl())
		runner := &Runner{sim: sim}
		engine, err := runner.buildEngine()
		if err != nil {
			t.Errorf("%s: hook: %v", c.name, err)
			continue
		}
		_, err = engine.Compile(prepared)
		if err != nil {
			t.Errorf("%s: %v | PREP=%q", c.name, err, prepared)
		} else {
			fmt.Printf("%s: OK\n", c.name)
		}
	}
}
