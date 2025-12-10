package composer

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
	"github.com/infinigence/octollm/pkg/engines"
)

const (
	ModelAccessPublic   = "public"
	ModelAccessInternal = "internal"
	ModelAccessPrivate  = "private"
)

type Model struct {
	// Name     string              `json:"name" yaml:"name"`
	Access           string              `json:"access" yaml:"access"` // public(w/o authN), internal(authN required), private(authN+Z)
	Backends         map[string]*Backend `json:"backends" yaml:"backends"`
	DefaultOrgLimits *LimitsConfig       `json:"default_org_limits" yaml:"default_org_limits"` // only for internal or private
	DefaultRules     RuleList            `json:"default_rules" yaml:"default_rules"`

	// rewrites effective for all backends
	RequestRewrites     *engines.RewritePolicy `json:"request_rewrites" yaml:"request_rewrites"`
	ResponseRewrites    *engines.RewritePolicy `json:"response_rewrites" yaml:"response_rewrites"`
	StreamChunkRewrites *engines.RewritePolicy `json:"stream_chunk_rewrites" yaml:"stream_chunk_rewrites"`
}

type Backend struct {
	Use                     string            `json:"use" yaml:"use"` // references a global backend config
	BaseURL                 string            `json:"base_url" yaml:"base_url"`
	HTTPProxy               *string           `json:"http_proxy" yaml:"http_proxy"`
	APIKey                  *string           `json:"api_key" yaml:"api_key"`
	AnthropicAPIKeyAsBearer *bool             `json:"anthropic_api_key_as_bearer" yaml:"anthropic_api_key_as_bearer"`
	ExtraHeaders            map[string]string `json:"extra_headers" yaml:"extra_headers"`
	URLPathChat             *string           `json:"url_path_chat" yaml:"url_path_chat"`
	URLPathMessages         *string           `json:"url_path_messages" yaml:"url_path_messages"`
	URLPathVertex           *string           `json:"url_path_vertex" yaml:"url_path_vertex"`

	RequestRewrites     *engines.RewritePolicy `json:"request_rewrites" yaml:"request_rewrites"`
	ResponseRewrites    *engines.RewritePolicy `json:"response_rewrites" yaml:"response_rewrites"`
	StreamChunkRewrites *engines.RewritePolicy `json:"stream_chunk_rewrites" yaml:"stream_chunk_rewrites"`
}

type RuleList []*RuleConfig

type RuleConfig struct {
	Name           string              `json:"name" yaml:"name"`
	MatchExpr      string              `json:"match" yaml:"match"`
	AddTags        map[string]string   `json:"add_tags" yaml:"add_tags"`
	Deny           *engines.DenyEngine `json:"deny" yaml:"deny"`
	RuleLimits     *LimitsConfig       `json:"rule_limits" yaml:"rule_limits"`
	ForwardWeights map[string]int      `json:"forward_weights" yaml:"forward_weights"`
}

type LimitsConfig struct {
	TPM               int  `json:"tpm" yaml:"tpm"`
	RPM               int  `json:"rpm" yaml:"rpm"`
	TPD               int  `json:"tpd" yaml:"tpd"`
	RPD               int  `json:"rpd" yaml:"rpd"`
	Concurrency       int  `json:"concurrency" yaml:"concurrency"`
	DenyWhenExceeding bool `json:"deny_when_exceeding" yaml:"deny_when_exceeding"` // only apply to rule_limits
}

type UserOrg struct {
	APIKeys map[string]string              `json:"api_keys" yaml:"api_keys"`
	Models  map[string]*UserOrgModelConfig `json:"models" yaml:"models"`
}

type UserOrgModelConfig struct {
	OrgLimits *LimitsConfig `json:"org_limits" yaml:"org_limits"`
	Rules     RuleList      `json:"rules" yaml:"rules"`
}

type ConfigFile struct {
	GlobalBackends map[string]*Backend `json:"backends" yaml:"backends"`
	Models         map[string]*Model   `json:"models" yaml:"models"`
	Users          map[string]*UserOrg `json:"users" yaml:"users"`
}

func ReadConfigFile(path string) (*ConfigFile, error) {
	yamlFile, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg := &ConfigFile{}
	if err := yaml.Unmarshal(yamlFile, cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config file: %w", err)
	}

	return cfg, nil
}
