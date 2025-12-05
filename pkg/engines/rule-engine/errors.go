package ruleengine

import "fmt"

type RuleEngineAction string

const (
	RuleEngineActionContinue RuleEngineAction = "continue"
	// RuleEngineActionReturn   RuleEngineAction = "return"
)

type ErrWithAction struct {
	Action RuleEngineAction
	Err    error
}

func (e *ErrWithAction) Error() string {
	return fmt.Sprintf("rule exec error (action %s): %s", e.Action, e.Err.Error())
}

func ErrorWithAction(err error, action RuleEngineAction) *ErrWithAction {
	return &ErrWithAction{
		Action: action,
		Err:    err,
	}
}

var (
	ErrNoRuleChain     = fmt.Errorf("no rule chain found")
	ErrNoRuleMatched   = fmt.Errorf("no rule matched")
	ErrRuleActionError = fmt.Errorf("rule action error")
)
