package limiter

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/engines/load-balancer/common"
	"github.com/infinigence/octollm/pkg/octollm"
)

// TPMColorLimiterEngine admits a request on a physical backend by the TPM that
// backend served over the last 60 seconds, at the priority a color marker of
// the same nameSpace assigned.
//
// It is the TPM counterpart of ConcurrencyColorLimiterEngine and shares its
// QoSLimits semantics, with TPM in place of inflight requests: the request's
// QoS index i owns the TPM of key:own_{i+1}, capped by PriorityLevelMax[i],
// and every index shares the TPM of key:public, capped by AvailableTotalMax[i].
// A request is admitted while both are below their caps, which HeadroomRatio
// raises for strong cache hits. Over a cap returns ErrTPMRateLimitReached, or
// ErrTPMStrongCacheRateLimitReached for a strong cache hit.
//
// An admitted request is counted in both windows: with WithTokenEstimate, its
// estimated tokens are reserved right away, so concurrent requests see one
// another before any has finished. The reservation is settled to the usage a
// usage.CollectorEngine below it recorded once the response is closed; it is
// refunded when the next engine fails, and kept when the response closes
// without usage recorded. Wrap each backend with its own limiter below the
// load balancer, so every attempt, retries and replicated requests included,
// is counted against the backend that served it.
type TPMColorLimiterEngine struct {
	redisClient *redis.Client
	key         string
	limitsFunc  LimitsFunc
	nameSpace   string
	estimate    TokenEstimateFunc
	next        octollm.Engine
}

var _ octollm.Engine = (*TPMColorLimiterEngine)(nil)

// NewTPMColorLimiterEngine creates a limiter with static QoSLimits, in TPM.
// limits must be non-nil, and both limit arrays must be non-empty.
func NewTPMColorLimiterEngine(redisClient *redis.Client, key string, limits *QoSLimits, nameSpace string, next octollm.Engine) (*TPMColorLimiterEngine, error) {
	if err := validateQoSLimits(limits); err != nil {
		return nil, err
	}
	cloned := cloneQoSLimits(*limits)
	return NewTPMColorLimiterEngineWithLimitsFunc(redisClient, key, nameSpace, func(*octollm.Request) (*QoSLimits, error) {
		return &cloned, nil
	}, next)
}

// NewTPMColorLimiterEngineWithLimitsFunc creates a limiter that resolves QoSLimits, in TPM, per request.
// limitsFunc must be non-nil. A nil or invalid QoSLimits from the function fails that request.
func NewTPMColorLimiterEngineWithLimitsFunc(redisClient *redis.Client, key string, nameSpace string, limitsFunc LimitsFunc, next octollm.Engine) (*TPMColorLimiterEngine, error) {
	if limitsFunc == nil {
		return nil, fmt.Errorf("limitsFunc must be provided")
	}
	if redisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}
	return &TPMColorLimiterEngine{
		redisClient: redisClient,
		key:         key,
		limitsFunc:  limitsFunc,
		nameSpace:   nameSpace,
		next:        next,
	}, nil
}

// WithTokenEstimate makes the limiter reserve fn's estimate of each admitted
// request in the backend TPM; without it, a request only counts once its
// response is closed. It must be called before the engine serves requests.
func (e *TPMColorLimiterEngine) WithTokenEstimate(fn TokenEstimateFunc) *TPMColorLimiterEngine {
	e.estimate = fn
	return e
}

const tpmLimiterLogPrefix = "[TPMColorLimiterEngine]"

// allow admits the request and returns what it reserved in the backend windows.
func (e *TPMColorLimiterEngine) allow(req *octollm.Request) (res *tpmReservation, priority int, err error) {
	ctx := req.Context()
	limits, err := e.limitsFunc(req)
	if err != nil {
		return nil, 0, err
	}
	if err := validateQoSLimits(limits); err != nil {
		return nil, 0, err
	}

	priority, _ = priorityFromNamespace(ctx, e.nameSpace)
	i := qosIndex(priority, len(limits.AvailableTotalMax))
	kind := common.CacheHitKindFromRequest(req)
	ownBase := limits.PriorityLevelMax[i]
	totalBase := limits.AvailableTotalMax[i]
	effectiveOwn, effectiveTotal := effectiveOwnAndTotalLimits(ownBase, totalBase, limits.HeadroomRatio, kind)

	ownKey := fmt.Sprintf("%s:own_%d", e.key, i+1)
	publicKey := e.key + ":public"
	keys := []string{ownKey, publicKey}
	caps := []int64{int64(effectiveOwn), int64(effectiveTotal)}

	tokens := estimateTokens(req, e.estimate, tpmLimiterLogPrefix)
	res, tpm, err := reserveTPM(ctx, e.redisClient, keys, caps, tokens, tpmLimiterLogPrefix)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("%s %v, ownKey: %s, publicKey: %s", tpmLimiterLogPrefix, err, ownKey, publicKey))
		return nil, 0, err
	}
	ownTPM, publicTPM := tpm[0], tpm[1]

	if res == nil {
		slog.WarnContext(ctx, fmt.Sprintf("%s tpm limit reached for priority %d (qosIndex %d), ownTPM: %d/%d, publicTPM: %d/%d, hitKind: %s",
			tpmLimiterLogPrefix, priority, i, ownTPM, effectiveOwn, publicTPM, effectiveTotal, kind))
		limitErr := ErrTPMRateLimitReached
		if kind == common.CacheHitStrong {
			limitErr = ErrTPMStrongCacheRateLimitReached
		}
		return nil, priority, limitErr
	}

	slog.InfoContext(ctx, fmt.Sprintf("%s tpm allowed for priority %d (qosIndex %d), ownTPM: %d/%d, publicTPM: %d/%d, reserved: %d, hitKind: %s",
		tpmLimiterLogPrefix, priority, i, ownTPM, effectiveOwn, publicTPM, effectiveTotal, tokens, kind))
	return res, priority, nil
}

func (e *TPMColorLimiterEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	res, priority, err := e.allow(req)
	if err != nil {
		slog.ErrorContext(req.Context(), fmt.Sprintf("%s tpm rate limiter error: %v, key: %s", tpmLimiterLogPrefix, err, e.key))
		if errors.Is(err, ErrRateLimitReached) {
			recordLimiterDeny(req, priority)
		}
		return nil, err
	}
	recordLimiterAllow(req, priority)

	return processReserved(req, e.next, res)
}
