package main

import (
	"os"
	"testing"
)

// TestDbgFile compiles a preprocessed file directly. Set DBGFILE=<path>.
func TestDbgFile(t *testing.T) {
	path := os.Getenv("DBGFILE")
	if path == "" {
		t.Skip("no DBGFILE")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	sim := NewSim(nil, defDecl())
	runner := &Runner{sim: sim}
	engine, herr := runner.buildEngine()
	if herr != nil {
		t.Fatalf("hook: %v", herr)
	}
	if _, cerr := engine.Compile(string(data)); cerr != nil {
		t.Fatalf("compile: %v", cerr)
	}
}
