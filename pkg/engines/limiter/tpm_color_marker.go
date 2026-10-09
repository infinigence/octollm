package limiter

import (
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	"github.com/infinigence/octollm/pkg/octollm"
)

// TPMColorMarkerEngine assigns a priority to each request from the TPM its
// tenant used on the model over the last 60 seconds, and records the request's
// final usage into that TPM once the response is closed.
//
// It is the TPM counterpart of ConcurrencyColorMarkerEngine: the priority is
// stored under the same nameSpace context key and color metadata, so the
// concurrency and TPM color limiters read it alike.
//
// The recorded usage is the one a usage.CollectorEngine below it reports for
// the response actually returned, so retries and replicated requests are not
// counted twice. Without a collector, nothing is recorded.
type TPMColorMarkerEngine struct {
	redisClient *redis.Client
	key         string
	rates       []int
	nameSpace   string
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
	return e, nil
}

func (e *TPMColorMarkerEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	ctx := req.Context()
	if len(e.rates) == 0 {
		recordMarkerAllow(req, 0)
		return e.next.Process(req)
	}

	tpm, err := readTPM(ctx, e.redisClient, []string{e.key})
	if err != nil {
		slog.ErrorContext(ctx, fmt.Sprintf("[TPMColorMarkerEngine] marker error: %v, key: %s", err, e.key))
		return nil, err
	}
	priority, ok := tpmMarkerPriority(e.rates, tpm[0])
	if !ok {
		slog.WarnContext(ctx, fmt.Sprintf("[TPMColorMarkerEngine] all tiers exhausted, key: %s, tpm: %d, rates: %v", e.key, tpm[0], e.rates))
		recordMarkerDeny(req, 0)
		return nil, ErrTPMLimitReached
	}
	req = req.WithContext(setPriorityToContext(ctx, e.nameSpace, priority))
	recordMarkerAllow(req, priority)
	slog.InfoContext(ctx, fmt.Sprintf("[TPMColorMarkerEngine] marked priority %d, key: %s, tpm: %d, rates: %v", priority, e.key, tpm[0], e.rates))

	resp, err := e.next.Process(req)
	if err != nil || resp == nil {
		return resp, err
	}
	onResponseClose(resp, func() {
		recordTPMUsage(req, e.redisClient, []string{e.key}, "[TPMColorMarkerEngine]")
	})
	return resp, nil
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
