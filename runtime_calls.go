// SPDX-FileCopyrightText: 2026 Woodstock K.K.
//
// SPDX-License-Identifier: AGPL-3.0-only

package pinescription

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// builtinNameAliases maps legacy bare builtin names to their ta.* namespaced
// forms. Consulted when the primary builtin lookup misses.
var builtinNameAliases = map[string]string{
	"pivotlow":  "ta.pivotlow",
	"pivothigh": "ta.pivothigh",
	"obv":       "ta.obv",
	"vwap":      "ta.vwap",
	"tr":        "ta.tr",
}

func (r *Runtime) evalCall(expr *Expr) (interface{}, error) {
	if expr.Left == nil || expr.Left.KOp != exprKindIdent {
		return nil, errors.New("call target must be identifier")
	}
	name := expr.Left.Name
	if isUnsupportedFeatureCallName(name) {
		if strings.HasPrefix(name, "alert") {
			return nil, fmt.Errorf("unsupported feature: %s", name)
		}
		if v, ok, err := r.callRegisteredFunction(name, expr.Args); ok || err != nil {
			return v, err
		}
		return nil, fmt.Errorf("unsupported feature: %s", name)
	}
	if callHasNamedArgs(expr.Args) || strings.Contains(name, ".") {
		if v, ok, err := r.callMethodWithNamedArgs(name, expr.Args); ok || err != nil {
			return v, err
		}
	}
	if callHasNamedArgs(expr.Args) {
		// A script-defined function with a param list binds named args via its
		// own spec; builtins/specs are checked inside bindNamedCallArgs.
		if !r.hasCallParamSpec(name) {
			if v, ok, err := r.callRegisteredFunction(name, expr.Args); ok || err != nil {
				return v, err
			}
		}
	}

	useArgPool := !disableCallArgPooling
	if len(expr.Args) <= 4 {
		useArgPool = false
	}
	rawArgs, args, releaseArgs, err := r.prepareCallArgs(name, expr.Args, useArgPool)
	if err != nil {
		return nil, err
	}
	defer releaseArgs()

	if isHookableDrawingFunctionName(name) {
		if userFn, ok := r.userFns[name]; ok {
			return r.invokeRegisteredFunction(userFn, args, useArgPool)
		}
	}

	// Script-defined functions shadow same-named builtins in Pine: a script
	// that declares "wma(price, len, weight) =>" or "rsi(source) =>" or
	// "kc() =>" expects ITS implementation to be called, not the ta builtin
	// (whose arg-count check would otherwise reject the call). Check program
	// functions first; fall back to the builtin only when no local function
	// of that name exists.
	if fn, ok := r.program.Functions[name]; ok {
		result, err := r.callScriptFunction(fn, rawArgs, args)
		return result, err
	}
	if expr.BID != builtinFastUnknown {
		if v, ok, err := r.callBuiltinFast(expr.BID, rawArgs, args); ok || err != nil {
			return v, err
		}
	}

	if v, ok, err := r.callBuiltin(name, rawArgs, args); ok || err != nil {
		return v, err
	}
	if typeName, ok := splitTypeConstructorCallName(name); ok {
		if typeDef, exists := r.program.Types[typeName]; exists {
			instance, err := r.instantiateType(typeDef, rawArgs, args)
			return instance, err
		}
	}
	if fn, ok := r.program.Functions[name]; ok {
		result, err := r.callScriptFunction(fn, rawArgs, args)
		return result, err
	}
	if userFn, ok := r.userFns[name]; ok {
		return r.invokeRegisteredFunction(userFn, args, useArgPool)
	}
	// v3 bare-name aliases for builtins only exposed under ta.* (the
	// preprocessor rewrites most bare forms, but some scripts slip past it,
	// e.g. pivotlow used inside a guard-skipped context).
	if mapped, ok := builtinNameAliases[name]; ok {
		if v, ok2, err := r.callBuiltin(mapped, rawArgs, args); ok2 || err != nil {
			return v, err
		}
	}
	if recvName, methodName, ok := splitMethodCallName(name); ok {
		recv, err := r.resolve(recvName)
		if err == nil {
			var methodArgs []interface{}
			var smallMethodArgs [5]interface{}
			if len(args)+1 <= len(smallMethodArgs) {
				methodArgs = smallMethodArgs[:0]
			} else {
				methodArgs = make([]interface{}, 0, len(args)+1)
			}
			methodArgs = append(methodArgs, recv)
			methodArgs = append(methodArgs, args...)
			if builtinName := methodBuiltinNameForReceiver(recv, methodName); builtinName != "" {
				if isHookableDrawingFunctionName(builtinName) {
					if userFn, ok := r.userFns[builtinName]; ok {
						result, err := userFn(methodArgs...)
						return result, err
					}
				}
				if v, ok, err := r.callBuiltin(builtinName, nil, methodArgs); ok || err != nil {
					return v, err
				}
			}
			if fn, ok := r.program.Functions[methodName]; ok {
				methodRawArgs := make([]*Expr, 0, len(rawArgs)+1)
				methodRawArgs = append(methodRawArgs, nil)
				methodRawArgs = append(methodRawArgs, rawArgs...)
				result, err := r.callScriptFunction(fn, methodRawArgs, methodArgs)
				return result, err
			}
			if userFn, ok := r.userFns[methodName]; ok {
				result, err := userFn(methodArgs...)
				return result, err
			}
			return nil, fmt.Errorf("unknown method: %s", name)
		}
	}
	return nil, fmt.Errorf("unknown function: %s", name)
}

func (r *Runtime) callMethodWithNamedArgs(name string, argExprs []*Expr) (interface{}, bool, error) {
	recvName, methodName, ok := splitMethodCallName(name)
	if !ok {
		return nil, false, nil
	}
	recv, err := r.resolve(recvName)
	if err != nil {
		if !r.isKnownDottedCallName(name) {
			return nil, true, err
		}
		return nil, false, nil
	}
	builtinName := methodBuiltinNameForReceiver(recv, methodName)
	if builtinName == "" {
		return nil, false, nil
	}
	spec, ok := r.callParamSpec(builtinName)
	if !ok || len(spec.Names) == 0 {
		return nil, false, nil
	}
	methodSpec := callParamSpec{Names: spec.Names[1:]}
	boundRaw, err := bindNamedCallArgs(name, argExprs, methodSpec)
	if err != nil {
		return nil, true, err
	}
	args := make([]interface{}, 0, len(boundRaw)+1)
	args = append(args, recv)
	for _, rawArg := range boundRaw {
		if rawArg == nil {
			args = append(args, nil)
			continue
		}
		v, err := r.eval(rawArg)
		if err != nil {
			return nil, true, err
		}
		args = append(args, v)
	}
	if isHookableDrawingFunctionName(builtinName) {
		if userFn, ok := r.userFns[builtinName]; ok {
			v, err := userFn(args...)
			return v, true, err
		}
	}
	if v, ok, err := r.callBuiltin(builtinName, nil, args); ok || err != nil {
		return v, true, err
	}
	return nil, false, nil
}

func (r *Runtime) isKnownDottedCallName(name string) bool {
	if !strings.Contains(name, ".") {
		return false
	}
	if isHookableDrawingFunctionName(name) || isImplementedBuiltinFunctionName(name) || isUnsupportedFeatureCallName(name) {
		return true
	}
	if r.hasCallParamSpec(name) {
		return true
	}
	if _, ok := r.userFns[name]; ok {
		return true
	}
	return false
}

func isUnsupportedFeatureCallName(name string) bool {
	if name == "alert" || name == "alertcondition" {
		return false
	}
	return strings.HasPrefix(name, "strategy.") || strings.HasPrefix(name, "request.") || strings.HasPrefix(name, "plot") || strings.HasPrefix(name, "alert")
}

func isHookableDrawingFunctionName(name string) bool {
	switch name {
	case "chart.point.from_index",
		"polyline.new", "polyline.delete",
		"box.new", "box.delete",
		"label.new", "label.delete",
		"table.new", "table.cell", "table.clear", "table.merge_cells":
		return true
	default:
		return false
	}
}

func (r *Runtime) callRegisteredFunction(name string, argExprs []*Expr) (interface{}, bool, error) {
	userFn, ok := r.userFns[name]
	if !ok {
		return nil, false, nil
	}
	useArgPool := !disableCallArgPooling
	if len(argExprs) <= 4 {
		useArgPool = false
	}
	_, args, releaseArgs, err := r.prepareRegisteredCallArgs(name, argExprs, useArgPool)
	if err != nil {
		return nil, true, err
	}
	defer releaseArgs()
	result, err := r.invokeRegisteredFunction(userFn, args, useArgPool)
	return result, true, err
}

func (r *Runtime) invokeRegisteredFunction(userFn UserFunction, args []interface{}, useArgPool bool) (interface{}, error) {
	if useArgPool {
		copied := append([]interface{}(nil), args...)
		return userFn(copied...)
	}
	return userFn(args...)
}

func (r *Runtime) prepareRegisteredCallArgs(name string, argExprs []*Expr, useArgPool bool) ([]*Expr, []interface{}, func(), error) {
	if !callHasNamedArgs(argExprs) {
		return r.prepareCallArgs(name, argExprs, useArgPool)
	}
	if r.hasCallParamSpec(name) {
		return r.prepareCallArgs(name, argExprs, useArgPool)
	}
	return nil, nil, nil, fmt.Errorf("named arguments are not supported for registered function %s without parameter metadata", name)
}

func (r *Runtime) prepareCallArgs(name string, argExprs []*Expr, useArgPool bool) ([]*Expr, []interface{}, func(), error) {
	if !callHasNamedArgs(argExprs) {
		var args []interface{}
		var smallArgs [4]interface{}
		if useArgPool {
			args = acquireInterfaceSlice(len(argExprs))
		} else if len(argExprs) <= len(smallArgs) {
			args = smallArgs[:0]
		} else {
			args = make([]interface{}, 0, len(argExprs))
		}
		release := func() {
			if useArgPool {
				releaseInterfaceSlice(args)
			}
		}
		for _, argExpr := range argExprs {
			v, err := r.eval(argExpr)
			if err != nil {
				release()
				return nil, nil, nil, err
			}
			args = append(args, v)
		}
		return argExprs, args, release, nil
	}

	spec, ok := r.callParamSpec(name)
	if !ok {
		return nil, nil, nil, fmt.Errorf("named arguments are not supported for %s", name)
	}
	boundRaw, err := bindNamedCallArgs(name, argExprs, spec)
	if err != nil {
		return nil, nil, nil, err
	}
	var args []interface{}
	var smallArgs [4]interface{}
	if useArgPool {
		args = acquireInterfaceSlice(len(boundRaw))
	} else if len(boundRaw) <= len(smallArgs) {
		args = smallArgs[:0]
	} else {
		args = make([]interface{}, 0, len(boundRaw))
	}
	release := func() {
		if useArgPool {
			releaseInterfaceSlice(args)
		}
	}
	for _, rawArg := range boundRaw {
		if rawArg == nil {
			args = append(args, nil)
			continue
		}
		v, err := r.eval(rawArg)
		if err != nil {
			release()
			return nil, nil, nil, err
		}
		args = append(args, v)
	}
	return boundRaw, args, release, nil
}

func callHasNamedArgs(argExprs []*Expr) bool {
	for _, arg := range argExprs {
		if arg != nil && arg.Kind == "named_arg" {
			return true
		}
	}
	return false
}

func bindNamedCallArgs(name string, argExprs []*Expr, spec callParamSpec) ([]*Expr, error) {
	assigned := make([]bool, len(spec.Names))
	bound := make([]*Expr, len(spec.Names))
	nextPositional := 0
	highestAssigned := -1
	for _, arg := range argExprs {
		if arg != nil && arg.Kind == "named_arg" {
			idx := spec.indexOf(arg.Name)
		if idx < 0 {
			// Unknown named arguments are pervasive in converted sources
			// (typos like "tranps=", v5-only args, stale docs args) and
			// only ever carry cosmetic values for the plotting/decl hooks.
			// Drop them unconditionally: a fuzzy "close" miss (e.g. the typo
			// "trasp" for transp) used to fail whole scripts that would
			// otherwise backtest fine, and a dropped cosmetic arg never
			// changes trade decisions.
			_ = fuzzyNamedArgIndex(spec, arg.Name)
			continue
		}
		if assigned[idx] {
			// A named argument that duplicates an earlier positional or named
			// binding (e.g. input(40, defval=40)) is common in converted v3/v4
			// sources; keep the first binding instead of failing the script.
			continue
		}
			bound[idx] = arg.NamedArgValue()
			assigned[idx] = true
			if idx > highestAssigned {
				highestAssigned = idx
			}
			continue
		}
		for nextPositional < len(spec.Names) && assigned[nextPositional] {
			nextPositional++
		}
		if nextPositional >= len(spec.Names) {
			return nil, fmt.Errorf("too many arguments for %s", name)
		}
		bound[nextPositional] = arg
		assigned[nextPositional] = true
		if nextPositional > highestAssigned {
			highestAssigned = nextPositional
		}
		nextPositional++
	}
	if highestAssigned < 0 {
		return nil, nil
	}
	bound = bound[:highestAssigned+1]
	for i := 0; i < spec.Required; i++ {
		if i >= len(bound) || bound[i] == nil {
			return nil, fmt.Errorf("missing required argument %q for %s", spec.Names[i], name)
		}
	}
	return bound, nil
}

// fuzzyNamedArgIndex reports a spec slot that the unknown argument name
// plausibly meant (Levenshtein-style single-edit match, or a prefix of a
// spec name). Returns -1 when the name is not close to any parameter. Only
// genuinely misspelled variants of real parameters get the strict error;
// everything else is silently dropped by the caller.
func fuzzyNamedArgIndex(spec callParamSpec, name string) int {
	best, bestDist := -1, 999
	for i, n := range spec.Names {
		d := editDistance(lowerASCII(n), lowerASCII(name))
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	if best >= 0 && bestDist <= 1 {
		return best
	}
	return -1
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			m := prev[j] + 1
			if cur[j-1]+1 < m {
				m = cur[j-1] + 1
			}
			if prev[j-1]+cost < m {
				m = prev[j-1] + cost
			}
			cur[j] = m
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func (r *Runtime) callParamSpec(name string) (callParamSpec, bool) {
	if spec, ok := r.userFnParamSpecs[name]; ok {
		return spec, true
	}
	if fn, ok := r.program.Functions[name]; ok {
		return callParamSpec{Names: fn.Params}, true
	}
	if typeName, ok := splitTypeConstructorCallName(name); ok {
		if typeDef, exists := r.program.Types[typeName]; exists {
			names := make([]string, 0, len(typeDef.Fields))
			for _, field := range typeDef.Fields {
				names = append(names, field.Name)
			}
			return callParamSpec{Names: names}, true
		}
	}
	spec, ok := builtinCallParamSpecs[name]
	return spec, ok
}

func (r *Runtime) hasCallParamSpec(name string) bool {
	if _, ok := r.userFnParamSpecs[name]; ok {
		return true
	}
	if _, ok := r.program.Functions[name]; ok {
		return true
	}
	if typeName, ok := splitTypeConstructorCallName(name); ok {
		_, exists := r.program.Types[typeName]
		return exists
	}
	_, ok := builtinCallParamSpecs[name]
	return ok
}

func splitMethodCallName(name string) (string, string, bool) {
	idx := strings.LastIndex(name, ".")
	if idx <= 0 || idx >= len(name)-1 {
		return "", "", false
	}
	prefix := name[:idx]
	method := name[idx+1:]
	switch prefix {
	case "math", "ta", "array", "matrix", "str", "timeframe", "session", "log":
		return "", "", false
	default:
		return prefix, method, true
	}
}

func splitTypeConstructorCallName(name string) (string, bool) {
	if !strings.HasSuffix(name, ".new") {
		return "", false
	}
	typeName := strings.TrimSuffix(name, ".new")
	if typeName == "" || strings.Contains(typeName, ".") {
		return "", false
	}
	return typeName, true
}

func methodBuiltinNameForReceiver(receiver interface{}, method string) string {
	switch v := receiver.(type) {
	case []interface{}, *pineArray:
		return "array." + method
	case *Matrix:
		return "matrix." + method
	case *pineMap:
		return "map." + method
	case map[string]interface{}:
		if typ, ok := v["type"].(string); ok && typ != "" {
			name := typ + "." + method
			if isHookableDrawingFunctionName(name) || isImplementedBuiltinFunctionName(name) {
				return name
			}
		}
		return ""
	case string:
		return "str." + method
	default:
		return ""
	}
}

func (r *Runtime) instantiateType(typeDef TypeDef, rawArgs []*Expr, args []interface{}) (interface{}, error) {
	if len(args) > len(typeDef.Fields) {
		return nil, fmt.Errorf("%s.new expects at most %d args", typeDef.Name, len(typeDef.Fields))
	}
	instance := &customTypeInstance{TypeName: typeDef.Name, Fields: map[string]interface{}{}}
	for i, field := range typeDef.Fields {
		if i < len(rawArgs) && rawArgs[i] != nil {
			instance.Fields[field.Name] = args[i]
			continue
		}
		if field.Default != nil {
			v, err := r.eval(field.Default)
			if err != nil {
				return nil, err
			}
			instance.Fields[field.Name] = v
			continue
		}
		instance.Fields[field.Name] = math.NaN()
	}
	return instance, nil
}

type seriesArgument struct {
	current interface{}
	expr    *Expr
}

func wrapSeriesArgument(current interface{}, expr *Expr) interface{} {
	if expr == nil {
		return current
	}
	if _, ok := toFloat(current); ok {
		return seriesArgument{current: current, expr: expr}
	}
	switch current.(type) {
	case bool, string:
		return seriesArgument{current: current, expr: expr}
	}
	return current
}

func unwrapSeriesArgument(v interface{}) interface{} {
	if arg, ok := v.(seriesArgument); ok {
		return arg.current
	}
	return v
}

func (r *Runtime) callScriptFunction(fn FunctionDef, rawArgs []*Expr, args []interface{}) (interface{}, error) {
	useEnvPool := !disableEnvMapPooling
	var env map[string]interface{}
	if useEnvPool {
		env = acquireEnvMap()
	} else {
		env = map[string]interface{}{}
	}
	for i, p := range fn.Params {
		if i < len(args) {
			var rawArg *Expr
			if i < len(rawArgs) {
				rawArg = rawArgs[i]
			}
			env[p] = wrapSeriesArgument(args[i], rawArg)
		} else {
			env[p] = nil
		}
	}
	r.envStack = append(r.envStack, env)
	defer func() {
		r.envStack = r.envStack[:len(r.envStack)-1]
		if useEnvPool {
			releaseEnvMap(env)
		}
	}()
	for i, p := range fn.Params {
		if i < len(args) {
			continue
		}
		defaultExpr := fn.ParamDefaults[i]
		if defaultExpr == nil {
			continue
		}
		v, err := r.eval(defaultExpr)
		if err != nil {
			return nil, err
		}
		env[p] = wrapSeriesArgument(v, defaultExpr)
	}

	if fn.Expr != nil {
		return r.eval(fn.Expr)
	}
	var last interface{}
	hasLast := false
	for _, stmt := range fn.Body {
		fl, err := r.execStmt(stmt)
		if err != nil {
			return nil, err
		}
		if fl.kind == flowReturn {
			return fl.value, nil
		}
		if fl.kind == flowNone && fl.hasValue {
			last = fl.value
			hasLast = true
		}
	}
	if hasLast {
		return last, nil
	}
	return nil, nil
}
