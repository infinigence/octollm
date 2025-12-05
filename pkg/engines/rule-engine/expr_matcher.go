package ruleengine

import (
	"encoding/json"
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/sirupsen/logrus"
)

type FeatureExtractor interface {
	Features(req *octollm.Request) (map[string]any, error)
}

type FeatureExtractorFunc func(req *octollm.Request) (map[string]any, error)

func (f FeatureExtractorFunc) Features(req *octollm.Request) (map[string]any, error) {
	return f(req)
}

type ExprMatcher struct {
	Code             string
	FeatureExtractor FeatureExtractor

	prog *vm.Program
}

type ExprMatcherEnv struct {
	RawReq   map[string]any
	Features map[string]any
	req      *octollm.Request
}

var _ Matcher = (*ExprMatcher)(nil)

func (m *ExprMatcher) Match(req *octollm.Request) bool {
	env, err := m.buildEnvFor(req)
	if err != nil {
		logrus.WithContext(req.Context()).Warnf("build env for request failed: %v", err)
	}

	if m.prog == nil {
		m.prog, err = expr.Compile(m.Code)
		if err != nil {
			logrus.WithContext(req.Context()).Warnf("compile expr code failed: %v", err)
			return false
		}
	}

	output, err := expr.Run(m.prog, env)
	if err != nil {
		logrus.WithContext(req.Context()).Warnf("run expr program failed: %v", err)
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
		logrus.WithContext(req.Context()).Warningf("Run rule (%s) invalid return type: %T", m.Code, v)
		return false
	}
}

func (env *ExprMatcherEnv) CtxValue(key any) any {
	return env.req.Context().Value(key)
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

	var features map[string]any
	if m.FeatureExtractor != nil {
		features, err = m.FeatureExtractor.Features(req)
		if err != nil {
			return nil, fmt.Errorf("extract features failed: %w", err)
		}
		logrus.WithContext(req.Context()).Debugf("[expr-matcher] extracted features: %v", features)
	}

	env := &ExprMatcherEnv{
		RawReq:   mapBody,
		Features: features,
		req:      req,
	}
	return env, nil
}
