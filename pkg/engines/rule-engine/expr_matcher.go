package ruleengine

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/infinigence/octollm/pkg/octollm"
)

type FeatureExtractor interface {
	Features(req *octollm.Request) (any, error)
}

type FeatureExtractorFunc func(req *octollm.Request) (any, error)

func (f FeatureExtractorFunc) Features(req *octollm.Request) (any, error) {
	return f(req)
}

type ExprMatcher struct {
	Code              string
	FeatureExtractors map[string]FeatureExtractor
	prog              *vm.Program
}

type ExprMatcherEnv struct {
	RawReq            map[string]any
	featureExtractors map[string]FeatureExtractor
	Req               *octollm.Request
}

var _ Matcher = (*ExprMatcher)(nil)

func (m *ExprMatcherEnv) ExtractFeature(feature string) any {
	if extractor, ok := m.featureExtractors[feature]; ok {
		value, err := extractor.Features(m.Req)
		if err != nil {
			slog.WarnContext(m.Req.Context(), "extract feature %s failed: %v", feature, err)
			return nil
		}
		return value
	}
	return nil
}

func (m *ExprMatcher) Match(req *octollm.Request) bool {
	env, err := m.buildEnvFor(req)
	if err != nil {
		slog.WarnContext(req.Context(), fmt.Sprintf("build env for request failed: %v", err))
	}

	if m.prog == nil {
		m.prog, err = expr.Compile(m.Code)
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

func (m *ExprMatcher) buildEnvFor(req *octollm.Request) (*ExprMatcherEnv, error) {
	mapBody := make(map[string]any)
	b, err := req.Body.Bytes()
	if err != nil {
		return nil, fmt.Errorf("read request body failed: %w", err)
	}
	if err := json.Unmarshal(b, &mapBody); err != nil {
		return nil, fmt.Errorf("unmarshal request body failed: %w", err)
	}

	env := &ExprMatcherEnv{
		RawReq:            mapBody,
		featureExtractors: m.FeatureExtractors,
		Req:               req,
	}
	return env, nil
}
