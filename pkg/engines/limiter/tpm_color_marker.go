package limiter

import (
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

// TPMColorMarkerEngine assigns a priority to each request from the TPM its
// tenant used on the model over the last 60 seconds, and counts the request's
// usage into that TPM.
//
// It is the TPM counterpart of ConcurrencyColorMarkerEngine: the priority is
// stored under the same nameSpace context key and color metadata, so the
// concurrency and TPM color limiters read it alike.
//
// With WithTokenEstimate, an admitted request reserves its estimated tokens
// right away, so concurrent requests see one another before any has finished.
// The reservation is settled to the usage a usage.CollectorEngine below it
// reports for the response actually returned, so retries and replicated
// requests are not counted twice; it is refunded when the next engine fails,
// and kept when the response closes without usage recorded.
type TPMColorMarkerEngine struct {
	redisClient *redis.Client
	key         string
	rates       []int
	ceiling     int64
	nameSpace   string
	estimate    TokenEstimateFunc
	next        octollm.Engine
}

var _ octollm.Engine = (*TPMColorMarkerEngine)(nil)

// NewTPMColorMarkerEngine creates a multi-tier TPM-based marker with priority assignment.
//
// Parameters:
//   - redisClient: Redis client.
//   - key: Redis key of the tenant x model TPM window.
//   - rates: Per-tier TPM bands, highest priority first. Bands stack: tier i covers the TPM
//     range [sum(rates[:i]), sum(rates[:i+1])), and the sum is the total TPM the marker admits.
//     Negative values are clamped to 0; a zero band is skipped. Only an empty rates slice
//     disables the marker (pass-through); e.g. rates=[0] rejects all traffic.
//     Priority assigned to a request: len(rates) - 1 - tierIdx, so tier 0 is the highest priority.
//   - nameSpace: Namespace for isolating priority between marker/limiter pairs. Marker and limiter sharing
//     a namespace can communicate via context.
//   - next: Next engine in the chain.
//
// Example: rates=[1000, 600, 300]
//   - TPM below 1000 -> priority 2; below 1600 -> priority 1; below 1900 -> priority 0; otherwise rejected.
func NewTPMColorMarkerEngine(redisClient *redis.Client, key string, rates []int, nameSpace string, next octollm.Engine) (*TPMColorMarkerEngine, error) {
	if next == nil {
		return nil, fmt.Errorf("next engine must not be nil")
	}
	e := &TPMColorMarkerEngine{
		redisClient: redisClient,
		key:         key,
		nameSpace:   nameSpace,
		next:        next,
	}
	if len(rates) == 0 {
		return e, nil
	}
	if redisClient == nil {
		return nil, fmt.Errorf("redisClient must be provided")
	}

	normalizedRates, normalized := normalizeConcurrencyMarkerRates(rates)
	if normalized {
		slog.Warn(fmt.Sprintf("tpm marker rates normalized from %v to %v", rates, normalizedRates))
	}
	e.rates = normalizedRates
	for _, rate := range normalizedRates {
		e.ceiling += int64(rate)
	}
	return e, nil
}

// WithTokenEstimate makes the marker reserve fn's estimate of each admitted
// request in the tenant TPM; without it, a request only counts once its
// response is closed. It must be called before the engine serves requests.
func (e *TPMColorMarkerEngine) WithTokenEstimate(fn TokenEstimateFunc) *TPMColorMarkerEngine {
	e.estimate = fn
	return e
}

func (e *TPMColorMarkerEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()
	if len(e.rates) == 0 {
		recordMarkerAllow(req, 0)
		return e.next.Process(req)
	}

	const logPrefix = "[TPMColorMarkerEngine]"
	tokens := estimateTokens(req, e.estimate, logPrefix)
	res, tpm, err := reserveTPM(ctx, e.redisClient, []string{e.key}, []int64{e.ceiling}, tokens, logPrefix)
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("%s marker error: %v, key: %s", logPrefix, err, e.key))
		return nil, err
	}
	if res == nil {
		slog.WarnContext(ctx, fmt.Sprintf("%s all tiers exhausted, key: %s, tpm: %d, rates: %v", logPrefix, e.key, tpm[0], e.rates))
		recordMarkerDeny(req, 0)
		return nil, ErrTPMRateLimitReached
	}
	// Admitted means tpm is below the sum of rates, so a band is found.
	priority, _ := tpmMarkerPriority(e.rates, tpm[0])
	req = req.WithContext(setPriorityToContext(ctx, e.nameSpace, priority))
	recordMarkerAllow(req, priority)
	slog.InfoContext(ctx, fmt.Sprintf("%s marked priority %d, key: %s, tpm: %d, reserved: %d, rates: %v", logPrefix, priority, e.key, tpm[0], tokens, e.rates))

	return processReserved(req, e.next, res)
}

// tpmMarkerPriority returns the priority of the first band whose cumulative
// ceiling is above tpm. ok is false when tpm has used up every band.
func tpmMarkerPriority(rates []int, tpm int64) (priority int, ok bool) {
	var ceiling int64
	for tierIdx, rate := range rates {
		if rate <= 0 {
			continue
		}
		ceiling += int64(rate)
		if tpm < ceiling {
			return len(rates) - 1 - tierIdx, true
		}
	}
	return 0, false
}
