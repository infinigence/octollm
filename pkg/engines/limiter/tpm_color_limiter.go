package limiter

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

// TPMColorLimiterEngine admits a request on a physical backend by the TPM that
// backend served over the last 60 seconds, at the priority a color marker of
// the same nameSpace assigned.
//
// It is the TPM counterpart of ConcurrencyColorLimiterEngine and shares its
// QoSLimits semantics, with TPM in place of inflight requests: the request's
// QoS index i owns the TPM of key:own_{i+1}, capped by AvailableTotalMax[i],
// and every index shares the TPM of key:public, capped by PriorityLevelMax[i].
// A request is admitted while both are below their caps, which HeadroomRatio
// raises for strong cache hits.
//
// Once the response is closed, the usage a usage.CollectorEngine below it
// recorded is added to both windows. Wrap each backend with its own limiter
// below the load balancer, so every attempt, retries and replicated requests
// included, is counted against the backend that served it.
type TPMColorLimiterEngine struct {
	redisClient *redis.Client
	key         string
	limitsFunc  LimitsFunc
	nameSpace   string
	next        octollm.Engine
}

var _ octollm.Engine = (*TPMColorLimiterEngine)(nil)

// NewTPMColorLimiterEngine creates a limiter with static QoSLimits, in TPM.
// Empty AvailableTotalMax and PriorityLevelMax disable the limiter (pass-through).
func NewTPMColorLimiterEngine(redisClient *redis.Client, key string, limits QoSLimits, nameSpace string, next octollm.Engine) (*TPMColorLimiterEngine, error) {
	if len(limits.AvailableTotalMax) == 0 && len(limits.PriorityLevelMax) == 0 {
		return NewTPMColorLimiterEngineWithLimitsFunc(redisClient, key, nameSpace, nil, next)
	}
	if err := validateQoSLimits(limits); err != nil {
		return nil, err
	}
	cloned := cloneQoSLimits(limits)
	return NewTPMColorLimiterEngineWithLimitsFunc(redisClient, key, nameSpace, func(*octollm.Request) (*QoSLimits, error) {
		return &cloned, nil
	}, next)
}

// NewTPMColorLimiterEngineWithLimitsFunc creates a limiter that resolves QoSLimits, in TPM, per request.
// A nil limitsFunc constructs a disabled pass-through limiter.
func NewTPMColorLimiterEngineWithLimitsFunc(redisClient *redis.Client, key string, nameSpace string, limitsFunc LimitsFunc, next octollm.Engine) (*TPMColorLimiterEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}
	if limitsFunc != nil && redisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}
	return &TPMColorLimiterEngine{
		redisClient: redisClient,
		key:         key,
		limitsFunc:  limitsFunc,
		nameSpace:   nameSpace,
		next:        next,
	}, nil
}

// allow returns the window keys the request's usage is recorded into, or nil
// when the limiter passes the request through.
func (e *TPMColorLimiterEngine) allow(req *octollm.Request) (keys []string, priority int, err error) {
	ctx := req.Context()
	if e.limitsFunc == nil {
		return nil, 0, nil
	}
	limits, err := e.limitsFunc(req)
	if err != nil {
		return nil, 0, err
	}
	if limits == nil || (len(limits.AvailableTotalMax) == 0 && len(limits.PriorityLevelMax) == 0) {
		return nil, 0, nil
	}
	if err := validateQoSLimits(*limits); err != nil {
		return nil, 0, err
	}

	priority, _ = priorityFromNamespace(ctx, e.nameSpace)
	i := qosIndex(priority, len(limits.AvailableTotalMax))
	kind := cacheHitKindFromRequest(req)
	effectiveOwn, effectiveTotal := effectiveOwnAndTotalLimits(limits.AvailableTotalMax[i], limits.PriorityLevelMax[i], limits.HeadroomRatio, kind)

	ownKey := fmt.Sprintf("%s:own_%d", e.key, i+1)
	publicKey := e.key + ":public"
	keys = []string{ownKey, publicKey}

	tpm, err := readTPM(ctx, e.redisClient, keys)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[TPMColorLimiterEngine] %v, ownKey: %s, publicKey: %s", err, ownKey, publicKey))
		return nil, 0, err
	}
	ownTPM, publicTPM := tpm[0], tpm[1]

	if ownTPM >= int64(effectiveOwn) || publicTPM >= int64(effectiveTotal) {
		slog.WarnContext(ctx, fmt.Sprintf("[TPMColorLimiterEngine] tpm limit reached for priority %d (qosIndex %d), ownTPM: %d/%d, publicTPM: %d/%d, hitKind: %s",
			priority, i, ownTPM, effectiveOwn, publicTPM, effectiveTotal, kind))
		return nil, priority, ErrTPMLimitReached
	}

	slog.InfoContext(ctx, fmt.Sprintf("[TPMColorLimiterEngine] tpm allowed for priority %d (qosIndex %d), ownTPM: %d/%d, publicTPM: %d/%d, hitKind: %s",
		priority, i, ownTPM, effectiveOwn, publicTPM, effectiveTotal, kind))
	return keys, priority, nil
}

func (e *TPMColorLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	keys, priority, err := e.allow(req)
	if err != nil {
		slog.ErrorContext(req.Context(), fmt.Sprintf("[TPMColorLimiterEngine] tpm rate limiter error: %v, key: %s", err, e.key))
		if errors.Is(err, ErrRateLimitReached) {
			recordLimiterDeny(req, priority)
		}
		return nil, err
	}
	recordLimiterAllow(req, priority)

	resp, err := e.next.Process(req)
	if err != nil || resp == nil || keys == nil {
		return resp, err
	}
	onResponseClose(resp, func() {
		recordTPMUsage(req, e.redisClient, keys, "[TPMColorLimiterEngine]")
	})
	return resp, nil
}
