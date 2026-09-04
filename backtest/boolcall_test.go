package main

import "testing"

func TestBoolCallForms(t *testing.T) {
	src := "longCond = bool(na)\njustcontinue = bool(true)\nplot(longCond ? 1 : 0)\n"
	prepared := preprocess(src)
	sim := NewSim(nil, defDecl())
	runner := &Runner{sim: sim}
	engine, herr := runner.buildEngine()
	if herr != nil {
		t.Fatalf("hook: %v", herr)
	}
	if _, err := engine.Compile(prepared); err != nil {
		t.Fatalf("compile: %v\nprepared:\n%s", err, prepared)
	}
}

func TestTupleReturnLastStmt(t *testing.T) {
	src := "getBB(pos) =>\n    [mBB, uBB, lBB] = bb(close[pos], 20, 2.0)\n\nplot(getBB(0)[1])\n"
	prepared := preprocess(src)
	sim := NewSim(nil, defDecl())
	runner := &Runner{sim: sim}
	engine, herr := runner.buildEngine()
	if herr != nil {
		t.Fatalf("hook: %v", herr)
	}
	if _, err := engine.Compile(prepared); err != nil {
		t.Fatalf("compile: %v\nprepared:\n%s", err, prepared)
	}
}
