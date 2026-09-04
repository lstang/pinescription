package pinescription

import "testing"

func TestProbeRSINegativeOffset(t *testing.T) {
	src := `RSI = rsi(close, 14)
GoLong = crossunder(RSI, 30)
GoShort = crossover(RSI, 60)
LongStopLoss = barssince(GoLong)<barssince(GoShort) and crossunder(low, valuewhen(GoLong, close, 0)*0.95)
ShortStopLoss = barssince(GoLong)>barssince(GoShort) and crossover(high, valuewhen(GoShort, close, 0)*1.05)
x = LongStopLoss ? 1 : ShortStopLoss ? -1 : 0`
	e := NewEngine()
	e.RegisterMarketDataProvider(providerWithClose("TEST", 10, 11, 12, 11, 10, 9, 8, 9, 10, 11, 12, 13, 14, 15, 16, 15, 14, 13, 12, 11))
	e.SetDefaultSymbol("TEST")
	b, err := e.Compile(src)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}
	_, err = e.Execute(b)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	t.Log("OK")
}