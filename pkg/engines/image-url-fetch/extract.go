package image_url_fetch

import (
	"strings"

	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// collectOpenAIImageReplaceJobs walks Chat Completions messages[].content / reasoning_content multipart arrays.
func collectOpenAIImageReplaceJobs(req *openai.ChatCompletionRequest) []imageReplaceJob {
	if req == nil {
		return nil
	}
	var jobs []imageReplaceJob
	for msgIndex, msg := range req.Messages {
		if msg == nil {
			continue
		}
		if msg.Content != nil {
			jobs = append(jobs, collectFromOpenAIMessageContent(msgIndex, "content", msg.Content)...)
		}
		if msg.ReasoningContent != nil {
			jobs = append(jobs, collectFromOpenAIMessageContent(msgIndex, "reasoning_content", msg.ReasoningContent)...)
		}
	}
	return jobs
}

// collectFromOpenAIMessageContent collects image_url parts from one message field (multipart array), same role as
// collectFromClaudeContentSlice for a single content array (no nesting in OpenAI multipart).
func collectFromOpenAIMessageContent(msgIndex int, field string, c openai.MessageContent) []imageReplaceJob {
	if c == nil {
		return nil
	}
	arr, ok := c.(openai.MessageContentArray)
	if !ok {
		return nil
	}
	var jobs []imageReplaceJob
	for i, item := range arr {
		if item == nil || item.ImageURL == nil {
			continue
		}
		if item.Type != "" && item.Type != "image_url" {
			continue
		}
		u := item.ImageURL.GetImageUrl()
		if u == "" {
			continue
		}
		_, isObject := item.ImageURL.(*openai.MessageContentItemImageURL)
		jobs = append(jobs, &openaiImageReplaceJob{
			msgIndex:     msgIndex,
			field:        field,
			partIndex:    i,
			url:          u,
			isObjectForm: isObject,
		})
	}
	return jobs
}

// collectClaudeImageReplaceJobs walks Claude messages[].content and nested tool_result.content for
// type=image blocks with source.type=url and a non-data remote URL.
func collectClaudeImageReplaceJobs(req *anthropic.ClaudeMessagesRequest) []imageReplaceJob {
	if req == nil {
		return nil
	}
	var jobs []imageReplaceJob
	for msgIndex, msg := range req.Messages {
		if msg == nil || msg.Content == nil {
			continue
		}
		arr, ok := msg.Content.(anthropic.MessageContentBlockArray)
		if !ok || len(arr) == 0 {
			continue
		}
		jobs = append(jobs, collectFromClaudeContentSlice(msgIndex, nil, arr)...)
	}
	return jobs
}

func collectFromClaudeContentSlice(msgIndex int, prefix []int, items anthropic.MessageContentBlockArray) []imageReplaceJob {
	var jobs []imageReplaceJob
	for i, block := range items {
		if block == nil {
			continue
		}
		pathToBlock := appendCopy(prefix, i)
		switch v := block.(type) {
		case *anthropic.ImageBlockParam:
			if v == nil || v.Source == nil {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(v.Source.Type), "url") {
				u := strings.TrimSpace(v.Source.Url)
				if u != "" && !strings.HasPrefix(strings.ToLower(u), "data:") {
					jobs = append(jobs, &claudeImageReplaceJob{
						msgIndex:       msgIndex,
						contentIndices: append([]int(nil), pathToBlock...),
						url:            u,
					})
				}
			}
		case *anthropic.ToolResultBlockParam:
			if v == nil || v.Content == nil {
				continue
			}
			if nested, ok := v.Content.(anthropic.MessageContentBlockArray); ok && len(nested) > 0 {
				jobs = append(jobs, collectFromClaudeContentSlice(msgIndex, pathToBlock, nested)...)
			}
		default:
			continue
		}
	}
	return jobs
}

func appendCopy(prefix []int, idx int) []int {
	out := make([]int, len(prefix)+1)
	copy(out, prefix)
	out[len(prefix)] = idx
	return out
}
