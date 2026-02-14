package exprenv

import "github.com/infinigence/octollm/pkg/octollm"

type ExprEnv struct {
	Req any `expr:"req"`
}

type ReqEnv struct {
}

func GetExprEnvFromRequest(ctx *octollm.Request) {
}
