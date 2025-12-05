package engines

import (
	"context"
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

type RewritePolicy struct {
	SetKeys       map[string]any    `json:"set_keys" yaml:"set_keys"`
	SetKeysByExpr map[string]string `json:"set_keys_by_expr" yaml:"set_keys_by_expr"`
	RemoveKeys    []string          `json:"remove_keys" yaml:"remove_keys"`
}

func (p *RewritePolicy) Merge(other *RewritePolicy) *RewritePolicy {
	if other == nil {
		return p
	}
	if p == nil {
		return other
	}

	merged := &RewritePolicy{
		SetKeys:       make(map[string]any),
		SetKeysByExpr: make(map[string]string),
		RemoveKeys:    append([]string{}, p.RemoveKeys...),
	}

	for k, v := range p.SetKeys {
		merged.SetKeys[k] = v
	}
	for k, v := range other.SetKeys {
		merged.SetKeys[k] = v
	}

	for k, v := range p.SetKeysByExpr {
		merged.SetKeysByExpr[k] = v
	}
	for k, v := range other.SetKeysByExpr {
		merged.SetKeysByExpr[k] = v
	}

	merged.RemoveKeys = append(merged.RemoveKeys, other.RemoveKeys...)

	return merged
}

type llmJSONRewriter struct {
	policy  *RewritePolicy
	ctx     context.Context
	exprEnv any
}

// RewriteJSON 重写JSON字符串
// 先执行 RemoveKeys，后执行 SetKeys
func (r *llmJSONRewriter) RewriteJSON(reqBody []byte) []byte {
	if r.policy == nil {
		return reqBody
	}

	var err error
	for _, k := range r.policy.RemoveKeys {
		reqBody, err = sjson.DeleteBytes(reqBody, k)
		if err != nil {
			logrus.WithContext(r.ctx).Warnf("[llmJSONRewriter.RewriteJSON] delete key (%s) body error: %s", k, err)
		}
	}

	for k, v := range r.policy.SetKeys {
		reqBody, err = sjson.SetBytes(reqBody, k, v)
		if err != nil {
			logrus.WithContext(r.ctx).Warnf("[llmJSONRewriter.RewriteJSON] set key (%s) error: %s", k, err)
		}
	}

	for k, code := range r.policy.SetKeysByExpr {
		prog, err := expr.Compile(code, expr.Env(r.exprEnv))
		if err != nil {
			logrus.WithContext(r.ctx).Warnf("[llmJSONRewriter.RewriteJSON] compile expr (%s) error: %s", code, err)
			continue
		}
		// 执行表达式
		v, err := expr.Run(prog, r.exprEnv)
		if err != nil {
			logrus.WithContext(r.ctx).Warnf("[llmJSONRewriter.RewriteJSON] run expr (%s) error: %s", code, err)
			continue
		}
		// 把输出结果设置到json中
		if v != nil {
			reqBody, err = sjson.SetBytes(reqBody, k, v)
			if err != nil {
				logrus.WithContext(r.ctx).Warnf("[llmJSONRewriter.RewriteJSON] set key (%s) error: %s", k, err)
			}
			logrus.WithContext(r.ctx).Debugf("[llmJSONRewriter.RewriteJSON] set key (%s) value (%v)", k, v)
		} else {
			logrus.WithContext(r.ctx).Debugf("[llmJSONRewriter.RewriteJSON] skip setting key (%s) because expr result is nil", k)
		}
	}

	return reqBody
}

type RewriteEngine struct {
	RequestRewrite           *RewritePolicy
	NonstreamResponseRewrite *RewritePolicy
	StreamChunkRewrite       *RewritePolicy

	Next octollm.Engine
}

var _ octollm.Engine = (*RewriteEngine)(nil)

func NewRewriteEngine(next octollm.Engine, requestRewrite, nonstreamResponseRewrite, streamChunkRewrite *RewritePolicy) *RewriteEngine {
	return &RewriteEngine{
		RequestRewrite:           requestRewrite,
		NonstreamResponseRewrite: nonstreamResponseRewrite,
		StreamChunkRewrite:       streamChunkRewrite,
		Next:                     next,
	}
}

func (e *RewriteEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	if e.RequestRewrite != nil {
		reqRewriter := &llmJSONRewriter{
			policy: e.RequestRewrite,
			ctx:    req.Context(),
			// exprEnv: req.Meta,
		}
		b, err := req.Body.Bytes()
		if err != nil {
			return nil, fmt.Errorf("get request body bytes error: %w", err)
		}
		req.Body.SetBytes(reqRewriter.RewriteJSON(b))
		logrus.WithContext(req.Context()).Debugf("[RewriteEngine.Run] request body rewritten")
	}
	if e.Next == nil {
		return nil, fmt.Errorf("next engine is nil")
	}
	resp, err := e.Next.Process(req)
	if err != nil {
		return nil, fmt.Errorf("underlying engine run error: %w", err)
	}

	if resp.Stream != nil {
		if e.StreamChunkRewrite == nil {
			return resp, nil
		}

		chunkRewriter := &llmJSONRewriter{
			policy: e.StreamChunkRewrite,
			ctx:    req.Context(),
			// exprEnv: req.Meta,
		}
		rewritenChunk := make(chan *octollm.StreamChunk)
		originalStream := resp.Stream
		ctx, cancel := context.WithCancel(req.Context())
		go func() {
			defer close(rewritenChunk)
			for chunk := range originalStream.Chan() {
				b, err := chunk.Body.Bytes()
				if err != nil {
					logrus.WithContext(ctx).Warnf("read stream chunk error: %s", err)
					continue
				}
				chunk.Body.SetBytes(chunkRewriter.RewriteJSON(b))
				select {
				case rewritenChunk <- chunk:
				case <-ctx.Done():
					logrus.WithContext(ctx).Debugf("stream chunk rewrite context canceled")
					return
				}
			}
		}()
		logrus.WithContext(req.Context()).Debugf("[RewriteEngine.Run] stream chunk rewritten")
		resp.Stream = octollm.NewStreamChan(rewritenChunk, func() {
			originalStream.Close()
			cancel()
		})
	} else {
		if e.NonstreamResponseRewrite == nil {
			return resp, nil
		}
		respRewriter := &llmJSONRewriter{
			policy: e.NonstreamResponseRewrite,
			ctx:    req.Context(),
			// exprEnv: req.Meta,
		}
		b, err := resp.Body.Bytes()
		if err != nil {
			return nil, fmt.Errorf("read response body error: %w", err)
		}
		resp.Body.SetBytes(respRewriter.RewriteJSON(b))
		logrus.WithContext(req.Context()).Debugf("[RewriteEngine.Run] non-stream response body rewritten")
	}

	return resp, nil
}
