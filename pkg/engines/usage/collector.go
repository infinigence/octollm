// Package usage records the token usage an upstream reports for a request on
// the request metadata, so engines that need it (rate limiting, billing,
// logging) do not each re-implement per-format response parsing.
//
// The usage is kept as the upstream reported it, in its own wire type.
// Interpreting it (what counts as billable input, how cache writes are
// priced, ...) is left to the consumer.
package usage

import (
	"context"
	"log/slog"

	"github.com/infinigence/octollm/pkg/octollm"
)

type metadataKey struct{}

// Get returns the usage the CollectorEngine recorded for req, or nil when none
// was recorded: no CollectorEngine processed req, or the upstream reported no
// usage (yet).
//
// For a stream, the value reflects every chunk the collector has forwarded so
// far: an engine wrapping the collector that has received a chunk is guaranteed
// to see that chunk's usage, so reading after the stream ends yields the final
// usage.
func Get(req *octollm.Request) Usage {
	if req == nil {
		return nil
	}
	v, _ := req.GetMetadataValue(metadataKey{})
	u, _ := v.(Usage)
	return u
}

// CollectorEngine records the usage reported by the next engine's response on
// the request metadata; read it with Get. It never alters the request or the
// response.
//
// Place it as close to the upstream client as possible, below retries and
// protocol conversion: each call it wraps is then one physical upstream call,
// and it resets the recorded usage on every Process, so after a retry Get
// reports the attempt whose response was actually returned.
type CollectorEngine struct {
	Next octollm.Engine
}

var _ octollm.Engine = (*CollectorEngine)(nil)

func NewCollectorEngine(next octollm.Engine) *CollectorEngine {
	return &CollectorEngine{Next: next}
}

func (e *CollectorEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	// Clear whatever an earlier attempt on this request recorded.
	req.SetMetadataValue(metadataKey{}, nil)

	resp, err := e.Next.Process(req)
	if err != nil || resp == nil {
		return resp, err
	}

	if resp.Stream != nil {
		e.collectStream(req, resp)
		return resp, nil
	}
	if resp.Body != nil && resp.Body.Parser() != nil {
		parsed, err := resp.Body.Parsed()
		if err != nil {
			// Surfacing the parse error is the business of whoever consumes the body.
			slog.DebugContext(req.Context(), "[usage] failed to parse response body, usage not collected", "err", err)
			return resp, nil
		}
		if u := extract(parsed); u != nil {
			req.SetMetadataValue(metadataKey{}, u)
		}
	}
	return resp, nil
}

func (e *CollectorEngine) collectStream(req *octollm.Request, resp *octollm.Response) {
	src := resp.Stream
	out := make(chan *octollm.StreamChunk)
	ctx, cancel := context.WithCancel(req.Context())

	octollm.SafeGo(req, func() {
		defer close(out)
		var cur Usage
		for chunk := range src.Chan() {
			// Record before forwarding, so an outer engine never observes a chunk
			// ahead of its usage.
			if chunk != nil && chunk.Body != nil && chunk.Body.Parser() != nil {
				if parsed, err := chunk.Body.Parsed(); err == nil {
					if u := extract(parsed); u != nil {
						if cur == nil {
							cur = u
						} else {
							cur = cur.Accumulate(u)
						}
						req.SetMetadataValue(metadataKey{}, cur)
					}
				}
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
	})

	resp.Stream = octollm.NewStreamChan(out, func() {
		src.Close()
		cancel()
	})
}
