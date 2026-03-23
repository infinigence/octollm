package traffic_replication

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
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

// CloneForReplication returns a deep copy of the request for fire-and-forget replication
// with its own separate context. Exported fields (URL, Query, Header, Body) are copied.
// Unexported fields: metadata is shared with the original (not deep-copied);
// ctx is replaced with a new context that has a deadline.
// If expiration <= 0, 10 minutes is used.
// The returned cancel must be called when the clone is no longer needed to release the
// context's timer (e.g., defer cancel() in the goroutine that processes the clone).
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
	clone = req.WithContext(ctx)

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
			clone.Query[k] = append([]string(nil), v...)
		}
	}

	// Deep copy Header
	if req.Header != nil {
		clone.Header = make(http.Header, len(req.Header))
		for k, v := range req.Header {
			clone.Header[k] = append([]string(nil), v...)
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

			go func(engine octollm.Engine, r *octollm.Request, cancel context.CancelFunc) {
				defer cancel()
				resp, err := engine.Process(r)
				if err != nil {
					slog.ErrorContext(r.Context(), fmt.Sprintf("[TrafficReplication] replication error: %v", err))
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
			}(item.TrafficReplicationEngine, reqC, cancel)
		}
	}

	req.SetMetadataValue(IsTrafficReplication, true)
	return e.Next.Process(req)
}
