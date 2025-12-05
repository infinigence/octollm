package ruleengine

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/openai/openai-go/v3"
)

type SimpleFeatureExtractor struct {
	PrefixHashLen []int
	SuffixHashLen []int
}

func (e *SimpleFeatureExtractor) Features(req *octollm.Request) (map[string]any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionNewParams:
		return e.featuresForChatCompletions(v), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

func (e *SimpleFeatureExtractor) featuresForChatCompletions(req *openai.ChatCompletionNewParams) map[string]any {
	r := make(map[string]any)
	r["promptTextLen"] = e.featurePromptTextLen(req)
	for _, l := range e.PrefixHashLen {
		r["prefix"+fmt.Sprintf("%d", l)] = e.featurePrefixHash(req, l)
	}
	for _, l := range e.SuffixHashLen {
		r["suffix"+fmt.Sprintf("%d", l)] = e.featureSuffixHash(req, l)
	}
	return r
}

func (e *SimpleFeatureExtractor) combinedTextForChatCompletionsMessage(msg *openai.ChatCompletionMessageParamUnion) string {
	switch v := msg.GetContent().AsAny().(type) {
	case *string:
		return *v
	case *[]openai.ChatCompletionContentPartTextParam:
		r := ""
		for _, part := range *v {
			r += part.Text
		}
		return r
	case *[]openai.ChatCompletionContentPartUnionParam:
		r := ""
		for _, part := range *v {
			t := part.GetText()
			if t != nil {
				r += *t
			}
		}
		return r
	case *[]openai.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion:
		r := ""
		for _, part := range *v {
			t := part.GetText()
			if t != nil {
				r += *t
			}
		}
		return r
	default:
		return ""
	}
}

func (e *SimpleFeatureExtractor) featurePrefixHash(req *openai.ChatCompletionNewParams, l int) string {
	if len(req.Messages) == 0 {
		return ""
	}
	msg0txt := e.combinedTextForChatCompletionsMessage(&req.Messages[0])
	msg0txt = strings.TrimSpace(msg0txt)
	// first 20 runes
	prefix := []rune(msg0txt)
	if len(prefix) > l {
		prefix = prefix[:l]
	}

	hasher := fnv.New32a()
	hasher.Write([]byte(req.Model))
	hasher.Write([]byte(string(prefix)))

	biz := fmt.Sprintf("%08x", hasher.Sum32())
	return biz
}

func (e *SimpleFeatureExtractor) featureSuffixHash(req *openai.ChatCompletionNewParams, l int) string {
	if len(req.Messages) == 0 {
		return ""
	}
	msg0txt := e.combinedTextForChatCompletionsMessage(&req.Messages[0])
	msg0txt = strings.TrimSpace(msg0txt)
	// first 20 runes
	suffix := []rune(msg0txt)
	if len(suffix) > l {
		suffix = suffix[len(suffix)-l:]
	}

	hasher := fnv.New32a()
	hasher.Write([]byte(req.Model))
	hasher.Write([]byte(string(suffix)))

	biz := fmt.Sprintf("%08x", hasher.Sum32())
	return biz
}

func (e *SimpleFeatureExtractor) featurePromptTextLen(req *openai.ChatCompletionNewParams) int {
	allMsgTextLen := 0
	for _, msg := range req.Messages {
		allMsgTextLen += len([]rune(e.combinedTextForChatCompletionsMessage(&msg)))
	}
	return allMsgTextLen
}
