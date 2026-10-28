package traffic_replication

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/infinigence/octollm/pkg/octollm"
)

// trafficReplicationMetadataKey is the metadata key used to mark requests
// that have already been replicated, preventing infinite replication loops.
type trafficReplicationMetadataKey string

type isDuplicateRequestContextKey = string

const (
	IsTrafficReplication trafficReplicationMetadataKey = "is_traffic_replication"

	// DefaultReplicationExpiration is used when expiration <= 0.
	DefaultReplicationExpiration = 10 * time.Minute

	// IsDuplicateRequest is stored in the cloned request's context.
	// contextFieldsHandler reads this key to add a "is_duplicate_request" field
	// to log entries, indicating the log came from a replicated (cloned) request.
	IsDuplicateRequest isDuplicateRequestContextKey = "is_duplicate_request"
)

// TrafficReplicationTarget defines a replication target with a sampling ratio.
// Ratio is in (0, 1]; 1 means 100% of traffic is replicated to this target.
type TrafficReplicationTarget struct {
	TrafficReplicationEngine octollm.Engine `json:"traffic_replication_engine"`
	Ratio                    float64        `json:"ratio"`
	ExpirationTime           time.Duration  `json:"expiration_time"` // 0 = 10 min default
}

// TrafficReplicationEngine replicates incoming requests to additional engine targets
// asynchronously based on configurable ratios. The primary request continues through
// the Next engine; replicated requests are fire-and-forget with error logging only.
type TrafficReplicationEngine struct {
	Next    octollm.Engine
	Targets []TrafficReplicationTarget `json:"targets"`
}

var _ octollm.Engine = (*TrafficReplicationEngine)(nil)

func NewTrafficReplicationEngine(targets []TrafficReplicationTarget, next octollm.Engine) (*TrafficReplicationEngine, error) {
	return &TrafficReplicationEngine{
		Next:    next,
		Targets: targets,
	}, nil
}

// CloneForReplication returns a copy of the request suitable for asynchronous replication.
// It builds a fresh Request via [octollm.NewEmptyRequest] (empty metadata, nil URL/Query/Header/Body—
// no pointer sharing with the source for those fields), copies Method and Format from the source,
// sets metadata [IsTrafficReplication] so nested replication engines do not fan out again, then
// deep-copies URL, Query, Header, and Body (body bytes and a new UnifiedBody; Parser is reused).
//
// ctx wraps the source request's context: [IsDuplicateRequest] is set to true, parent cancellation
// is not propagated ([context.WithoutCancel]), and a deadline is applied using expiration (or
// [DefaultReplicationExpiration] if expiration <= 0). Other context values inherited from the
// source remain visible to [context.Context.Value] unless shadowed.
//
// The returned cancel must be called when the clone is no longer needed (e.g. defer cancel() in
// the goroutine that processes the clone) to release the deadline timer.
func CloneForReplication(req *octollm.Request, expiration time.Duration) (clone *octollm.Request, cancel context.CancelFunc) {
	if req == nil {
		return nil, nil
	}
	if expiration <= 0 {
		expiration = DefaultReplicationExpiration
	}
	ctx := context.WithValue(req.Context(), IsDuplicateRequest, true)
	ctx = context.WithoutCancel(ctx)
	ctx, cancel = context.WithDeadline(ctx, time.Now().Add(expiration))

	// only ctx + empty metadata; no shared URL/Header/Body with req until filled below.
	clone = octollm.NewEmptyRequest(ctx)
	clone.Method = req.Method
	clone.Format = req.Format
	// Mark so nested TrafficReplicationEngine on the duplicate chain does not replicate again.
	clone.SetMetadataValue(IsTrafficReplication, true)

	// Deep copy URL
	if req.URL != nil {
		if urlCopy, err := url.Parse(req.URL.String()); err == nil {
			clone.URL = urlCopy
		}
	}

	// Deep copy Query
	if req.Query != nil {
		clone.Query = make(url.Values, len(req.Query))
		for k, v := range req.Query {
			clone.Query[k] = slices.Clone(v)
		}
	}

	// Deep copy Header
	if req.Header != nil {
		clone.Header = make(http.Header, len(req.Header))
		for k, v := range req.Header {
			clone.Header[k] = slices.Clone(v)
		}
	}

	// Deep copy Body (skip if nil or Bytes fails)
	if req.Body != nil {
		if bodyBytes, err := req.Body.Bytes(); err == nil {
			bytesCopy := make([]byte, len(bodyBytes))
			copy(bytesCopy, bodyBytes)
			clone.Body = octollm.NewBodyFromBytes(bytesCopy, req.Body.Parser())
		}
	}

	return clone, cancel
}

// Process forwards the request to Next and optionally replicates it to additional
// targets based on each target's ratio. Replication runs asynchronously; only
// errors are logged. Requests already marked as replicated are skipped to avoid loops.
func (e *TrafficReplicationEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	// Only replicate original requests; skip if already marked (prevents loops when
	// replicated requests flow through chains that include this engine).
	if _, ok := req.GetMetadataValue(IsTrafficReplication); !ok && len(e.Targets) > 0 {
		for _, item := range e.Targets {
			// Sample by ratio: skip if random value exceeds ratio
			if rand.Float64() > item.Ratio {
				continue
			}
			// Clone with separate context and expiration deadline
			reqC, cancel := CloneForReplication(req, item.ExpirationTime)
			if reqC == nil {
				continue
			}

			octollm.SafeGo(reqC, func() {
				defer cancel()
				resp, err := item.TrafficReplicationEngine.Process(reqC)
				// Draining the stream below can take minutes; drop the cloned body so it can be GC'd.
				reqC = reqC.ReleaseBody()
				if err != nil {
					slog.ErrorContext(reqC.Context(), fmt.Sprintf("[TrafficReplication] replication error: %v", err))
					return
				}
				// Close response resources to avoid leaks. For streaming, drain until
				// complete before closing so upstream can finish writing.
				if resp != nil {
					if resp.Stream != nil {
						for chunk := range resp.Stream.Chan() {
							if chunk != nil && chunk.Body != nil {
								_ = chunk.Body.Close()
							}
						}
						resp.Stream.Close()
					} else if resp.Body != nil {
						_ = resp.Body.Close()
					}
				}
			})
		}
	}

	req.SetMetadataValue(IsTrafficReplication, true)
	return e.Next.Process(req)
}
