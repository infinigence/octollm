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

func Get(req *octollm.Request) *exprEnv {
	env, ok := octollm.GetCtxValue[*exprEnv](req, octollm.ContextKeyExprEnv)
	if ok {
		return env
	}

	return &exprEnv{
		ReqEnv: &requestExprEnv{req: req},
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
