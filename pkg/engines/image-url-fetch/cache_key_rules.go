package image_url_fetch

import (
	"fmt"
	"regexp"

	"github.com/infinigence/octollm/pkg/engines/image-url-fetch/cache"
)

// CacheKeyRule declares a whitelist pattern for canonicalizing remote image URLs
// before computing the SHA-256 cache key. First matching rule wins.
type CacheKeyRule struct {
	// Name identifies the rule in logs and error messages.
	Name string
	// Pattern is a RE2 regex matching URLs whose stable resource ID should be extracted.
	Pattern string
	// KeyTemplate is the canonical key source template; supports $1, $2, etc. from capture groups.
	KeyTemplate string
}

type compiledCacheKeyRule struct {
	name        string
	pattern     *regexp.Regexp
	keyTemplate string
}

func compileCacheKeyRules(rules []CacheKeyRule) ([]compiledCacheKeyRule, error) {
	out := make([]compiledCacheKeyRule, 0, len(rules))
	for _, r := range rules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid cache key rule %q: %w", ErrImageURLFetch, r.Name, err)
		}
		out = append(out, compiledCacheKeyRule{
			name:        r.Name,
			pattern:     re,
			keyTemplate: r.KeyTemplate,
		})
	}
	return out, nil
}

func (e *ImageURLFetchEngine) cacheKeyForURL(rawURL string) string {
	keySource := rawURL
	for _, rule := range e.cacheKeyRules {
		if rule.pattern.MatchString(rawURL) {
			keySource = rule.pattern.ReplaceAllString(rawURL, rule.keyTemplate)
			break
		}
	}
	return cache.KeyForURL(keySource)
}
