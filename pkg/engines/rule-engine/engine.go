package ruleengine

import (
	"errors"
	"fmt"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/sirupsen/logrus"
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

func (e *RuleEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	// find default chain
	currChain, ok := e.Chains["default"]
	if !ok {
		currChain, ok = e.Chains[""]
		if !ok {
			return nil, ErrNoRuleChain
		}
	}

	logrus.WithContext(req.Context()).Debugf("[rule-engine] executing default rule chain")
	for _, r := range currChain {
		logrus.WithContext(req.Context()).Debugf("[rule-engine] going to match rule %s", r.Name)
		if !r.Matcher.Match(req) {
			continue
		}
		logrus.WithContext(req.Context()).Debugf("[rule-engine] rule %s matched, executing", r.Name)
		resp, err := r.Engine.Process(req)
		if err == nil {
			logrus.WithContext(req.Context()).Debugf("[rule-engine] rule %s exec success", r.Name)
			return resp, nil
		}

		logrus.WithContext(req.Context()).Errorf("[rule-engine] rule %s exec error: %s", r.Name, err.Error())
		eAct := &ErrWithAction{}
		if !errors.As(err, &eAct) {
			return nil, fmt.Errorf("%w: %w", ErrRuleActionError, err)
		}

		switch eAct.Action {
		case RuleEngineActionContinue:
			logrus.WithContext(req.Context()).Debugf("[rule-engine] continue to next rule")
			continue
		default:
			return nil, fmt.Errorf("%w: %w", ErrRuleActionError, err)
		}
	}

	return nil, ErrNoRuleMatched
}
