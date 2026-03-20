package ruleengine

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/infinigence/octollm/pkg/octollm"
)

type Matcher interface {
	Match(req *octollm.Request) bool
}

type MatchFunc func(req *octollm.Request) bool

func (f MatchFunc) Match(req *octollm.Request) bool {
	return f(req)
}

type Rule struct {
	Name    string // optional name for logging
	Matcher Matcher
	Engine  octollm.Engine
}

type RuleChain []Rule

type RuleEngine struct {
	Chains map[string]RuleChain
}

var _ octollm.Engine = (*RuleEngine)(nil)

type ruleEngineMetadataKey string

const (
	// matchedRuleName stores the name of the rule that was matched by the rule engine.
	matchedRuleName ruleEngineMetadataKey = "matched_rule_name"
)

// GetMatchedRuleName retrieves the matched rule name from request metadata.
func GetMatchedRuleName(req *octollm.Request) (string, bool) {
	val, ok := req.GetMetadataValue(matchedRuleName)
	if !ok {
		return "", false
	}
	ruleName, ok := val.(string)
	return ruleName, ok
}

func (e *RuleEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	// find default chain
	currChain, ok := e.Chains["default"]
	if !ok {
		currChain, ok = e.Chains[""]
		if !ok {
			return nil, ErrNoRuleChain
		}
	}

	slog.DebugContext(req.Context(), "[rule-engine] executing default rule chain")
	for _, r := range currChain {
		slog.DebugContext(req.Context(), fmt.Sprintf("[rule-engine] going to match rule %s", r.Name))
		if !r.Matcher.Match(req) {
			continue
		}
		req.SetMetadataValue(matchedRuleName, r.Name)
		slog.DebugContext(req.Context(), fmt.Sprintf("[rule-engine] rule %s matched, executing", r.Name))
		resp, err := r.Engine.Process(req)
		if err == nil {
			slog.DebugContext(req.Context(), fmt.Sprintf("[rule-engine] rule %s exec success", r.Name))
			return resp, nil
		}

		slog.ErrorContext(req.Context(), fmt.Sprintf("[rule-engine] rule %s exec error: %s", r.Name, err.Error()))
		eAct := &ErrWithAction{}
		if !errors.As(err, &eAct) {
			return nil, fmt.Errorf("%w: %w", ErrRuleActionError, err)
		}

		switch eAct.Action {
		case RuleEngineActionContinue:
			slog.DebugContext(req.Context(), "[rule-engine] continue to next rule")
			continue
		default:
			return nil, fmt.Errorf("%w: %w", ErrRuleActionError, err)
		}
	}

	return nil, ErrNoRuleMatched
}
