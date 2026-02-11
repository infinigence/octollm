package ruleengine

import (
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// Helper function to extract text from a message
func combinedTextForChatCompletionsMessage(msg *openai.Message) string {
	if msg.Content != nil {
		return msg.Content.ExtractText()
	}
	return ""
}

// PromptTextLenExtractor extracts the total length of all message texts
type PromptTextLenExtractor struct{}

func (e *PromptTextLenExtractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		allMsgTextLen := 0
		for _, msg := range v.Messages {
			allMsgTextLen += len([]rune(combinedTextForChatCompletionsMessage(msg)))
		}
		return allMsgTextLen, nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// PrefixHashExtractor extracts a hash of the prefix of the first message
type PrefixHashExtractor struct {
	Length int
}

func (e *PrefixHashExtractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		if len(v.Messages) == 0 {
			return "", nil
		}
		msg0txt := combinedTextForChatCompletionsMessage(v.Messages[0])
		msg0txt = strings.TrimSpace(msg0txt)
		// first l runes
		prefix := []rune(msg0txt)
		if len(prefix) > e.Length {
			prefix = prefix[:e.Length]
		}

		hasher := fnv.New32a()
		hasher.Write([]byte(v.Model))
		hasher.Write([]byte(string(prefix)))

		return fmt.Sprintf("%08x", hasher.Sum32()), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}

// SuffixHashExtractor extracts a hash of the suffix of the first message
type SuffixHashExtractor struct {
	Length int
}

func (e *SuffixHashExtractor) Features(req *octollm.Request) (any, error) {
	reqBody, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("parse request body failed: %w", err)
	}

	switch v := reqBody.(type) {
	case *openai.ChatCompletionRequest:
		if len(v.Messages) == 0 {
			return "", nil
		}
		msg0txt := combinedTextForChatCompletionsMessage(v.Messages[0])
		msg0txt = strings.TrimSpace(msg0txt)
		// last l runes
		suffix := []rune(msg0txt)
		if len(suffix) > e.Length {
			suffix = suffix[len(suffix)-e.Length:]
		}

		hasher := fnv.New32a()
		hasher.Write([]byte(v.Model))
		hasher.Write([]byte(string(suffix)))

		return fmt.Sprintf("%08x", hasher.Sum32()), nil
	default:
		return nil, fmt.Errorf("unsupported request body type %T", reqBody)
	}
}
