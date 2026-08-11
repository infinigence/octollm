package exprenv

import (
	"encoding/json"
	"net/http"

	"github.com/infinigence/octollm/pkg/octollm"
)

type exprEnv struct {
	ReqEnv *requestExprEnv `expr:"req"`

	// TODO: add more fields if needed, e.g. backend info
}

// Sentinel is a zero-value env for use with expr.Compile type-checking,
// without requiring a live request.
var Sentinel = &exprEnv{ReqEnv: &requestExprEnv{}}

var defaultExtractors = make(map[string]FeatureExtractor)

// RegisterDefaultExtractor registers a feature extractor that will be included in every env
// returned by Get(req). Call from init or main (e.g. in cmd/octollm-server/main.go or cmd/mock/main.go).
// Must not be called concurrently with Get, UnregisterDefaultExtractor, or another RegisterDefaultExtractor.
func RegisterDefaultExtractor(name string, extractor FeatureExtractor) {
	defaultExtractors[name] = extractor
}

// UnregisterDefaultExtractor removes a previously registered default extractor. Useful in tests
// to clean up after RegisterDefaultExtractor.
// Must not be called concurrently with Get, RegisterDefaultExtractor, or another UnregisterDefaultExtractor.
func UnregisterDefaultExtractor(name string) {
	delete(defaultExtractors, name)
}

// Get returns an expr env for the request, built from the current req and globally
// registered default extractors (see RegisterDefaultExtractor). No env is stored in context.
// Must not be called concurrently with RegisterDefaultExtractor or UnregisterDefaultExtractor.
func Get(req *octollm.Request) *exprEnv {
	return &exprEnv{
		ReqEnv: &requestExprEnv{req: req, featureExtractors: defaultExtractors},
	}
}

func (e *exprEnv) WithFeatureExtractor(name string, extractor FeatureExtractor) *exprEnv {
	if e.ReqEnv != nil {
		if e.ReqEnv.featureExtractors == nil {
			e.ReqEnv.featureExtractors = make(map[string]FeatureExtractor)
		}
		e.ReqEnv.featureExtractors[name] = extractor
	}
	return e
}

type requestExprEnv struct {
	req               *octollm.Request
	featureExtractors map[string]FeatureExtractor
	rawReq            map[string]any
}

type FeatureExtractorFunc func(req *octollm.Request) (any, error)

func (f FeatureExtractorFunc) Features(req *octollm.Request) (any, error) {
	return f(req)
}

type FeatureExtractor interface {
	Features(req *octollm.Request) (any, error)
}

// RawReq returns the raw request body as a map[string]any. It caches the result after the first call.
func (r *requestExprEnv) RawReq() map[string]any {
	if r.rawReq != nil {
		return r.rawReq
	}

	b, err := r.req.Body.Bytes()
	if err != nil {
		return nil
	}
	var rawReq map[string]any
	if err := json.Unmarshal(b, &rawReq); err != nil {
		return nil
	}

	r.rawReq = rawReq
	return rawReq
}

// Context allows retrieving values from the request context by key. It returns nil if the key does not exist.
func (r *requestExprEnv) Context(key string) any {
	return r.req.Context().Value(key)
}

// Feature returns the extracted feature value for a given key. It returns nil if the key does not exist or if there is an error during extraction.
func (r *requestExprEnv) Feature(key string) any {
	if extractor, ok := r.featureExtractors[key]; ok {
		val, err := extractor.Features(r.req)
		if err != nil {
			return nil
		}
		return val
	}
	return nil
}

// Header returns the value of the specified header key from the received request headers. It returns an empty string if the header key does not exist.
func (r *requestExprEnv) Header(key string) string {
	recvHeader, ok := octollm.GetCtxValue[http.Header](r.req, octollm.ContextKeyReceivedHeader)
	if !ok {
		return ""
	}
	return recvHeader.Get(key)
}

// OriginalFormat returns the API format the request entered the gateway with,
// e.g. to let a model's rewrite expr pick different thinking defaults for
// Messages vs Chat Completions entry protocols regardless of any conversion
// downstream. It falls back to the request's current Format when the request
// has no context (e.g. the Sentinel env) or the original-format value is
// missing.
func (r *requestExprEnv) OriginalFormat() string {
	if v, ok := octollm.GetCtxValue[octollm.APIFormat](r.req, octollm.ContextKeyOriginalFormat); ok {
		return string(v)
	}
	if r.req == nil {
		return string(octollm.APIFormatUnknown)
	}
	return string(r.req.Format)
}
