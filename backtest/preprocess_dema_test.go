package main

import (
	"strings"
	"testing"
)

func TestDebugDEMAIfExpr(t *testing.T) {
	src := `demaCrossover = if (len2 > 0) and (len3 > 0)
    crossover(demaVal1, demaVal2) and (demaVal3 > demaVal3[1])
else
    if (len2 > 0) and (len3 == 0)
        crossover(demaVal1, demaVal2)
    else
        if (len3 > 0) and (len2 == 0)
            crossover(demaVal1, demaVal3)
        else
            crossover(close, demaVal1)
`
	out := preprocess(src)
	t.Logf("=== OUTPUT ===\n%s=== END ===", out)
	if strings.Contains(out, ": if (") || strings.Contains(out, "if :=") || strings.Contains(out, "if =") {
		t.Errorf("corruption present")
	}
}
