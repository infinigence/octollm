package ruleengine

import "github.com/infinigence/octollm/pkg/octollm"

type FixedMatcher bool

var _ Matcher = (FixedMatcher)(false)

func (m FixedMatcher) Match(req *octollm.Request) bool {
	return bool(m)
}

const (
	AlwaysTrueMatcher  = FixedMatcher(true)
	AlwaysFalseMatcher = FixedMatcher(false)
)
