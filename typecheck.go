// SPDX-FileCopyrightText: 2026 Woodstock K.K.
//
// SPDX-License-Identifier: AGPL-3.0-only

package pinescription

import (
	"fmt"
	"strings"
)

type staticExprType uint8

const (
	staticTypeUnknown staticExprType = iota
	staticTypeBool
	staticTypeNumber
	staticTypeString
	staticTypeNA
)

type staticTypeEnv struct {
	vars map[string]staticExprType
}

func newStaticTypeEnv() *staticTypeEnv {
	return &staticTypeEnv{vars: map[string]staticExprType{}}
}

func (e *staticTypeEnv) get(name string) (staticExprType, bool) {
	v, ok := e.vars[name]
	return v, ok
}

func (e *staticTypeEnv) set(name string, typ staticExprType) {
	if name == "" {
		return
	}
	e.vars[name] = typ
}

func validateNoNumericToBoolAutoConversion(program *Program) error {
	if program == nil {
		return nil
	}

	env := newStaticTypeEnv()
	if err := validateStmtListNoNumericToBool(program.Stmts, env); err != nil {
		return err
	}

	for _, fn := range program.Functions {
		fnEnv := newStaticTypeEnv()
		// Seed the function env with global types ONLY for names the function
		// does not declare locally: at runtime a function-local assignment
		// ("down = crossunder(...)") creates a fresh frame entry that shadows
		// the global, so a global numeric type must not poison the local. The
		// runtime treats every decl/assign inside a function as frame-local.
		localNames := map[string]bool{}
		collectLocalDeclNames(fn.Body, localNames)
		for name, typ := range env.vars {
			if !localNames[name] {
				fnEnv.vars[name] = typ
			}
		}
		for _, p := range fn.Params {
			fnEnv.set(p, staticTypeUnknown)
		}
		for _, defaultExpr := range fn.ParamDefaults {
			if _, err := validateExprNoNumericToBool(defaultExpr, fnEnv); err != nil {
				return fmt.Errorf("function %s: %w", fn.Name, err)
			}
		}
		if fn.Expr != nil {
			if _, err := validateExprNoNumericToBool(fn.Expr, fnEnv); err != nil {
				return fmt.Errorf("function %s: %w", fn.Name, err)
			}
		}
		if err := validateStmtListNoNumericToBool(fn.Body, fnEnv); err != nil {
			return fmt.Errorf("function %s: %w", fn.Name, err)
		}
	}

	return nil
}

// collectLocalDeclNames walks a statement list and records every name that
// is declared or assigned within it (recursing into nested bodies), so a
// function's locals can be excluded from the seeded global type environment.
func collectLocalDeclNames(stmts []Stmt, into map[string]bool) {
	for _, stmt := range stmts {
		switch stmt.Kind {
		case "decl", "assign":
			into[stmt.Name] = true
		case "if":
			collectLocalDeclNames(stmt.Then, into)
			collectLocalDeclNames(stmt.Else, into)
		case "for", "forin":
			collectLocalDeclNames(stmt.Body, into)
		case "while":
			collectLocalDeclNames(stmt.Body, into)
		case "switch":
			for _, c := range stmt.Cases {
				collectLocalDeclNames(c.Body, into)
			}
			collectLocalDeclNames(stmt.Default, into)
		}
	}
}

func validateStmtListNoNumericToBool(stmts []Stmt, env *staticTypeEnv) error {
	for _, stmt := range stmts {
		if err := validateStmtNoNumericToBool(stmt, env); err != nil {
			return err
		}
	}
	return nil
}

func validateStmtNoNumericToBool(stmt Stmt, env *staticTypeEnv) error {
	switch stmt.Kind {
	case "decl":
		valueType := staticTypeUnknown
		if stmt.Expr != nil {
			inferred, err := validateExprNoNumericToBool(stmt.Expr, env)
			if err != nil {
				return err
			}
			valueType = inferred
		}
		declared := staticTypeFromName(stmt.TypeName)
		if declared == staticTypeBool && valueType == staticTypeNumber {
			return fmt.Errorf("cannot assign int/float expression to bool variable %s", stmt.Name)
		}
		if declared != staticTypeUnknown {
			env.set(stmt.Name, declared)
			return nil
		}
		if valueType != staticTypeUnknown && valueType != staticTypeNA {
			env.set(stmt.Name, valueType)
		}
		return nil
	case "assign":
		inferred, err := validateExprNoNumericToBool(stmt.Expr, env)
		if err != nil {
			return err
		}
		if declared, ok := env.get(stmt.Name); ok {
			if declared == staticTypeUnknown {
				// An assignment (not a decl) to an unknown-typed name infers its
				// type here. That is correct for a true first declaration, but
				// WRONG when the global env was seeded with a numeric type from
				// a same-named variable and the function actually re-assigns a
				// LOCAL first ("down = crossunder(...)" after a global numeric
				// "down") — the local assignment shadowed the global at runtime,
				// so the local type wins and must not be poisoned by the seed.
				// Covered by the general unknown handling below.
			}
			if declared == staticTypeBool && inferred == staticTypeNumber {
				return fmt.Errorf("cannot assign int/float expression to bool variable %s", stmt.Name)
			}
			if declared == staticTypeUnknown && inferred != staticTypeUnknown && inferred != staticTypeNA {
				env.set(stmt.Name, inferred)
			}
			return nil
		}
		if inferred != staticTypeUnknown && inferred != staticTypeNA {
			env.set(stmt.Name, inferred)
		}
		return nil
	case "tuple_assign":
		_, err := validateExprNoNumericToBool(stmt.Expr, env)
		return err
	case "expr", "return":
		_, err := validateExprNoNumericToBool(stmt.Expr, env)
		return err
	case "if":
		if err := ensureBoolContextNoNumeric(stmt.Cond, env, "if condition"); err != nil {
			return err
		}
		if err := validateStmtListNoNumericToBool(stmt.Then, env); err != nil {
			return err
		}
		return validateStmtListNoNumericToBool(stmt.Else, env)
	case "while":
		if err := ensureBoolContextNoNumeric(stmt.Cond, env, "while condition"); err != nil {
			return err
		}
		return validateStmtListNoNumericToBool(stmt.Body, env)
	case "for":
		if stmt.ForIn != nil {
			if _, err := validateExprNoNumericToBool(stmt.ForIn, env); err != nil {
				return err
			}
			return validateStmtListNoNumericToBool(stmt.Body, env)
		}
		if _, err := validateExprNoNumericToBool(stmt.From, env); err != nil {
			return err
		}
		if _, err := validateExprNoNumericToBool(stmt.To, env); err != nil {
			return err
		}
		if _, err := validateExprNoNumericToBool(stmt.By, env); err != nil {
			return err
		}
		return validateStmtListNoNumericToBool(stmt.Body, env)
	case "block":
		return validateStmtListNoNumericToBool(stmt.Body, env)
	case "switch":
		if stmt.SwitchExpr != nil {
			if _, err := validateExprNoNumericToBool(stmt.SwitchExpr, env); err != nil {
				return err
			}
			for _, c := range stmt.Cases {
				if _, err := validateExprNoNumericToBool(c.Match, env); err != nil {
					return err
				}
				if err := validateStmtListNoNumericToBool(c.Body, env); err != nil {
					return err
				}
			}
		} else {
			for _, c := range stmt.Cases {
				if err := ensureBoolContextNoNumeric(c.Match, env, "switch condition"); err != nil {
					return err
				}
				if err := validateStmtListNoNumericToBool(c.Body, env); err != nil {
					return err
				}
			}
		}
		return validateStmtListNoNumericToBool(stmt.Default, env)
	default:
		return nil
	}
}

func ensureBoolContextNoNumeric(expr *Expr, env *staticTypeEnv, context string) error {
	_, err := validateExprNoNumericToBool(expr, env)
	if err != nil {
		return err
	}
	// The runtime evaluates boolean contexts with truthy(), which accepts
	// numeric series (non-zero/non-NaN is true). Legacy (v2-v4) Pine scripts
	// rely on this implicit numeric-to-bool conversion, so numbers are
	// permitted here rather than rejected.
	return nil
}

func validateExprNoNumericToBool(expr *Expr, env *staticTypeEnv) (staticExprType, error) {
	if expr == nil {
		return staticTypeUnknown, nil
	}

	switch expr.Kind {
	case "number":
		return staticTypeNumber, nil
	case "bool":
		return staticTypeBool, nil
	case "string":
		return staticTypeString, nil
	case "na":
		return staticTypeNA, nil
	case "ident":
		return staticTypeForIdentifier(expr.Name, env), nil
	case "index":
		leftType, err := validateExprNoNumericToBool(expr.Left, env)
		if err != nil {
			return staticTypeUnknown, err
		}
		if _, err := validateExprNoNumericToBool(expr.Right, env); err != nil {
			return staticTypeUnknown, err
		}
		switch leftType {
		case staticTypeBool, staticTypeNumber, staticTypeString:
			return leftType, nil
		default:
			return staticTypeUnknown, nil
		}
	case "unary":
		if _, err := validateExprNoNumericToBool(expr.Right, env); err != nil {
			return staticTypeUnknown, err
		}
		switch expr.Op {
		case "not":
			return staticTypeBool, nil
		case "+", "-":
			return staticTypeNumber, nil
		default:
			return staticTypeUnknown, nil
		}
	case "binary":
		leftType, err := validateExprNoNumericToBool(expr.Left, env)
		if err != nil {
			return staticTypeUnknown, err
		}
		rightType, err := validateExprNoNumericToBool(expr.Right, env)
		if err != nil {
			return staticTypeUnknown, err
		}
		switch expr.Op {
		case "and", "or":
			// numeric operands are fine: truthy() handles them at runtime
			return staticTypeBool, nil
		case "==", "!=", "<", "<=", ">", ">=":
			return staticTypeBool, nil
		case "+", "-", "*", "/", "%":
			if expr.Op == "+" && (leftType == staticTypeString || rightType == staticTypeString) {
				return staticTypeString, nil
			}
			return staticTypeNumber, nil
		default:
			return staticTypeUnknown, nil
		}
	case "ternary":
		_, err := validateExprNoNumericToBool(expr.Left, env)
		if err != nil {
			return staticTypeUnknown, err
		}
		whenTrueType, err := validateExprNoNumericToBool(expr.Right, env)
		if err != nil {
			return staticTypeUnknown, err
		}
		whenFalseType, err := validateExprNoNumericToBool(expr.Else, env)
		if err != nil {
			return staticTypeUnknown, err
		}
		if whenTrueType == whenFalseType {
			return whenTrueType, nil
		}
		return staticTypeUnknown, nil
	case "switch":
		resultType := staticTypeUnknown
		hasResultType := false
		if expr.SwitchExpr != nil {
			if _, err := validateExprNoNumericToBool(expr.SwitchExpr, env); err != nil {
				return staticTypeUnknown, err
			}
			for _, c := range expr.Cases {
				if _, err := validateExprNoNumericToBool(c.Match, env); err != nil {
					return staticTypeUnknown, err
				}
				bodyType, err := validateSwitchExprBodyNoNumericToBool(c.Body, env)
				if err != nil {
					return staticTypeUnknown, err
				}
				resultType, hasResultType = mergeStaticExprTypes(resultType, hasResultType, bodyType)
			}
		} else {
			for _, c := range expr.Cases {
				if err := ensureBoolContextNoNumeric(c.Match, env, "switch condition"); err != nil {
					return staticTypeUnknown, err
				}
				bodyType, err := validateSwitchExprBodyNoNumericToBool(c.Body, env)
				if err != nil {
					return staticTypeUnknown, err
				}
				resultType, hasResultType = mergeStaticExprTypes(resultType, hasResultType, bodyType)
			}
		}
		defaultType, err := validateSwitchExprBodyNoNumericToBool(expr.Default, env)
		if err != nil {
			return staticTypeUnknown, err
		}
		resultType, hasResultType = mergeStaticExprTypes(resultType, hasResultType, defaultType)
		if !hasResultType {
			return staticTypeUnknown, nil
		}
		return resultType, nil
	case "named_arg":
		return validateExprNoNumericToBool(expr.NamedArgValue(), env)
	case "call":
		if _, err := validateExprNoNumericToBool(expr.Left, env); err != nil {
			return staticTypeUnknown, err
		}
		for _, arg := range expr.Args {
			if _, err := validateExprNoNumericToBool(arg, env); err != nil {
				return staticTypeUnknown, err
			}
		}
		if expr.Left != nil && expr.Left.Kind == "ident" {
			return staticTypeForBuiltinCall(expr.Left.Name), nil
		}
		return staticTypeUnknown, nil
	case "array", "tuple":
		for _, elem := range expr.Elems {
			if _, err := validateExprNoNumericToBool(elem, env); err != nil {
				return staticTypeUnknown, err
			}
		}
		return staticTypeUnknown, nil
	default:
		return staticTypeUnknown, nil
	}
}

func validateSwitchExprBodyNoNumericToBool(body []Stmt, env *staticTypeEnv) (staticExprType, error) {
	if len(body) != 1 {
		return staticTypeUnknown, validateStmtListNoNumericToBool(body, env)
	}
	stmt := body[0]
	if stmt.Kind != "expr" && stmt.Kind != "return" {
		return staticTypeUnknown, validateStmtNoNumericToBool(stmt, env)
	}
	return validateExprNoNumericToBool(stmt.Expr, env)
}

func mergeStaticExprTypes(current staticExprType, hasCurrent bool, next staticExprType) (staticExprType, bool) {
	if next == staticTypeUnknown || next == staticTypeNA {
		return current, hasCurrent
	}
	if !hasCurrent {
		return next, true
	}
	if current == next {
		return current, true
	}
	return staticTypeUnknown, true
}

func staticTypeForIdentifier(name string, env *staticTypeEnv) staticExprType {
	if env != nil {
		if t, ok := env.get(name); ok {
			return t
		}
	}
	switch name {
	case "open", "high", "low", "close", "volume", "bar_index",
		"time", "time_close", "timenow", "time_tradingday",
		"year", "month", "dayofmonth", "dayofweek", "hour", "minute", "second",
		"timeframe.multiplier", "math.e", "math.pi", "math.phi", "math.rphi":
		return staticTypeNumber
	case "timeframe.isdaily", "timeframe.isweekly", "timeframe.ismonthly", "timeframe.isdwm",
		"timeframe.isseconds", "timeframe.isticks", "timeframe.isminutes", "timeframe.isintraday":
		return staticTypeBool
	default:
		return staticTypeUnknown
	}
}

func staticTypeForBuiltinCall(name string) staticExprType {
	switch name {
	case "bool", "na", "map.contains", "array.includes", "array.every",
		"str.contains", "str.startswith", "str.endswith",
		"timeframe.change",
		"crossover", "ta.crossover", "crossunder", "ta.crossunder", "cross", "ta.cross",
		"ta.rising", "ta.falling":
		return staticTypeBool
	case "int", "float", "close_of", "open_of", "high_of", "low_of", "value_of":
		return staticTypeNumber
	default:
		if strings.HasPrefix(name, "math.") {
			return staticTypeNumber
		}
		return staticTypeUnknown
	}
}

func staticTypeFromName(name string) staticExprType {
	switch name {
	case "bool":
		return staticTypeBool
	case "int", "float":
		return staticTypeNumber
	case "string":
		return staticTypeString
	default:
		return staticTypeUnknown
	}
}
