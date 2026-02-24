package ruleengine

import (
	"fmt"
	"log/slog"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/infinigence/octollm/pkg/exprenv"
	"github.com/infinigence/octollm/pkg/octollm"
)

type ExprMatcher struct {
	Code string
	prog *vm.Program
}

var _ Matcher = (*ExprMatcher)(nil)

func (m *ExprMatcher) Match(req *octollm.Request) bool {
	env := exprenv.Get(req)

	var err error
	if m.prog == nil {
		m.prog, err = expr.Compile(m.Code, expr.Env(exprenv.Sentinel))
		if err != nil {
			slog.WarnContext(req.Context(), fmt.Sprintf("compile expr code failed: %v", err))
			return false
		}
	}

	output, err := expr.Run(m.prog, env)
	if err != nil {
		slog.WarnContext(req.Context(), fmt.Sprintf("run expr program failed: %v", err))
		return false
	}

	switch v := output.(type) {
	case bool:
		return v
	case int:
		return v != 0
	case float64:
		return v != 0
	default:
		slog.WarnContext(req.Context(), fmt.Sprintf("Run rule (%s) invalid return type: %T", m.Code, v))
		return false
	}
}
