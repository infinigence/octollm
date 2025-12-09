package composer

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	loadbalancer "github.com/infinigence/octollm/pkg/engines/load-balancer"
	ruleengine "github.com/infinigence/octollm/pkg/engines/rule-engine"
	"github.com/infinigence/octollm/pkg/errutils"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/openai/openai-go/v3"
	"github.com/sirupsen/logrus"
)

type RuleComposerFileBased struct {
	mu sync.RWMutex

	modelRepo      ModelRepo
	conf           *ConfigFile
	lbRetryTimeout time.Duration
	lbRetryCount   int

	orgModelEngine map[string]map[string]octollm.Engine // orgName -> modelName -> engine
}

func NewRuleRepoFileBased(modelRepo ModelRepo, lbRetryTimeout time.Duration, lbRetryCount int) *RuleComposerFileBased {
	return &RuleComposerFileBased{
		modelRepo:      modelRepo,
		lbRetryTimeout: lbRetryTimeout,
		lbRetryCount:   lbRetryCount,
		orgModelEngine: make(map[string]map[string]octollm.Engine),
	}
}

func (r *RuleComposerFileBased) UpdateFromConfig(conf *ConfigFile) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.conf = conf

	return nil
}

func (r *RuleComposerFileBased) getEngine(orgName, modelName string) (octollm.Engine, error) {
	r.mu.RLock()
	if engine, ok := r.orgModelEngine[orgName][modelName]; ok {
		r.mu.RUnlock()
		return engine, nil
	}

	conf := r.conf
	r.mu.RUnlock()

	model, ok := conf.Models[modelName]
	if !ok {
		return nil, errutils.NewHandlerError(
			fmt.Errorf("model %s not found", modelName),
			http.StatusNotFound, "Model Not Found")
	}

	var orgModelConf *UserOrgModelConfig
	hasOrgModelConf := false
	if orgName != "" {
		if orgConf, ok := conf.Users[orgName]; ok {
			logrus.Debugf("conf.Users: %+v", conf.Users[orgName])
			if v, ok := orgConf.Models[modelName]; ok {
				orgModelConf = v
				hasOrgModelConf = true
			}
		}
	}

	// check if user has access to model
	switch model.Access {
	case ModelAccessInternal:
		if orgName == "" {
			return nil, errutils.NewHandlerError(
				fmt.Errorf("org name is required for internal model %s", modelName),
				http.StatusUnauthorized, "Unauthorized")
		}
	case ModelAccessPrivate:
		if !hasOrgModelConf {
			return nil, errutils.NewHandlerError(
				fmt.Errorf("org %s has no access to model %s", orgName, modelName),
				http.StatusUnauthorized, "Unauthorized")
		}
	}

	finalRules := model.DefaultRules
	if orgModelConf != nil {
		finalRules = append(orgModelConf.Rules, model.DefaultRules...)
	}
	finalOrgLimits := model.DefaultOrgLimits
	if orgModelConf != nil {
		finalOrgLimits = orgModelConf.OrgLimits
	}
	// TODO: org limits not implemented
	_ = finalOrgLimits

	var engine octollm.Engine
	defaultEngine, err := r.buildDefaultEngine(modelName)
	if err != nil {
		logrus.Warnf("failed to build default engine: %v", err)
	}
	if len(finalRules) == 0 {
		if defaultEngine == nil {
			return nil, fmt.Errorf("failed to build default engine: %w", err)
		}
		engine = defaultEngine
	} else {
		var err error
		engine, err = r.buildEngineByRuleList(finalRules, modelName, defaultEngine)
		if err != nil {
			return nil, fmt.Errorf("failed to build engine by rule list: %w", err)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.orgModelEngine[orgName]; !ok {
		r.orgModelEngine[orgName] = make(map[string]octollm.Engine)
	}
	r.orgModelEngine[orgName][modelName] = engine
	return engine, nil
}

func (r *RuleComposerFileBased) buildDefaultEngine(modelName string) (octollm.Engine, error) {
	backendNames := r.modelRepo.GetBackendNamesByModel(modelName)
	if len(backendNames) == 0 {
		return nil, fmt.Errorf("no backend found for model %s", modelName)
	}

	lbItems := make([]loadbalancer.BackendItem, 0, len(backendNames))
	for _, backendName := range backendNames {
		// starts with "default:"
		if !strings.HasPrefix(backendName, "default:") {
			continue
		}
		engine, err := r.modelRepo.GetEngine(modelName, backendName)
		if err != nil {
			logrus.Warnf("failed to get engine for backend %s: %v", backendName, err)
			continue
		}

		lbItems = append(lbItems, loadbalancer.BackendItem{
			Name:   backendName,
			Engine: engine,
			Weight: 100, // all equal weight
		})
	}

	if len(lbItems) == 0 {
		return nil, fmt.Errorf("no default backend found for model %s", modelName)
	}
	lb, err := loadbalancer.NewWeightedRoundRobin(lbItems, r.lbRetryTimeout, r.lbRetryCount)
	if err != nil {
		return nil, fmt.Errorf("failed to build weighted round robin load balancer: %w", err)
	}

	return lb, nil
}

func (r *RuleComposerFileBased) buildEngineByRuleList(ruleConfs RuleList, modelName string, defaultEngine octollm.Engine) (octollm.Engine, error) {
	rules := make(ruleengine.RuleChain, 0, len(ruleConfs))
	for _, ruleConf := range ruleConfs {
		rule, err := r.buildRuleEngineRuleByConfig(ruleConf, modelName, defaultEngine)
		if err != nil {
			return nil, fmt.Errorf("failed to build rule engine rule by config: %w", err)
		}
		rules = append(rules, *rule)
	}

	// add a fallback rule
	rules = append(rules, ruleengine.Rule{
		Name:    "fallback",
		Matcher: ruleengine.AlwaysTrueMatcher,
		Engine:  defaultEngine,
	})

	re := &ruleengine.RuleEngine{
		Chains: map[string]ruleengine.RuleChain{
			"default": rules,
		},
	}

	return re, nil
}

func (r *RuleComposerFileBased) buildRuleEngineRuleByConfig(ruleConf *RuleConfig, modelName string, defaultEngine octollm.Engine) (*ruleengine.Rule, error) {
	var matcher ruleengine.Matcher
	if ruleConf.MatchExpr == "" {
		matcher = ruleengine.AlwaysTrueMatcher
	} else {
		matcher = &ruleengine.ExprMatcher{
			Code:             ruleConf.MatchExpr,
			FeatureExtractor: &ruleengine.SimpleFeatureExtractor{PrefixHashLen: []int{20}, SuffixHashLen: []int{20}},
		}
	}

	if ruleConf.Deny != nil {
		return &ruleengine.Rule{
			Name:    ruleConf.Name,
			Matcher: matcher,
			Engine:  ruleConf.Deny,
		}, nil
	}

	// forward with load balancer
	lbItems := make([]loadbalancer.BackendItem, 0, len(ruleConf.ForwardWeights))
	for backendName, weight := range ruleConf.ForwardWeights {
		engine, err := r.modelRepo.GetEngine(modelName, backendName)
		if err != nil {
			logrus.Warnf("failed to get engine for backend %s: %v", backendName, err)
			continue
		}
		logrus.Infof("successfully get engine for backend %s: %v", backendName, engine)
		lbItems = append(lbItems, loadbalancer.BackendItem{
			Name:   backendName,
			Weight: weight,
			Engine: engine,
		})
	}

	engine := defaultEngine
	if len(lbItems) > 0 {
		lb, err := loadbalancer.NewWeightedRoundRobin(lbItems, r.lbRetryTimeout, r.lbRetryCount)
		if err != nil {
			return nil, fmt.Errorf("failed to build weighted round robin load balancer: %w", err)
		}
		engine = lb
	}

	rule := &ruleengine.Rule{
		Name:    ruleConf.Name,
		Matcher: matcher,
		Engine:  engine,
	}
	return rule, nil
}

func (r *RuleComposerFileBased) GetEngine(userName, orgName, modelName string) *RuleComposerEngine {
	return &RuleComposerEngine{
		RuleComposerFileBased: r,
		Model:                 modelName,
		OrgName:               orgName,
	}
}

type RuleComposerEngine struct {
	*RuleComposerFileBased
	Model   string
	OrgName string
}

var _ octollm.Engine = (*RuleComposerEngine)(nil)

func (r *RuleComposerEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	if r.Model == "" {
		// extract from request
		body, err := req.Body.Parsed()
		if err != nil {
			return nil, fmt.Errorf("failed to parse request body: %w", err)
		}
		switch body := body.(type) {
		case *openai.ChatCompletionNewParams:
			r.Model = body.Model
		default:
			return nil, fmt.Errorf("unsupported model request type: %T", body)
		}
	}

	engine, err := r.getEngine(r.OrgName, r.Model)
	if err != nil {
		return nil, fmt.Errorf("failed to get engine: %w", err)
	}
	return engine.Process(req)
}
