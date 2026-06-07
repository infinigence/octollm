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
		jobs = append(jobs, openaiImageReplaceJob{
			MsgIndex:     msgIndex,
			Field:        field,
			PartIndex:    i,
			URL:          u,
			IsObjectForm: isObject,
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
		if msg == nil || len(msg.Content) == 0 {
			continue
		}
		jobs = append(jobs, collectFromClaudeContentSlice(msgIndex, nil, msg.Content)...)
	}
	return jobs
}

func collectFromClaudeContentSlice(msgIndex int, prefix []int, items []anthropic.MessageContent) []imageReplaceJob {
	var jobs []imageReplaceJob
	for i, mc := range items {
		if mc == nil {
			continue
		}
		pathToBlock := appendCopy(prefix, i)
		switch v := mc.(type) {
		case anthropic.MessageContentString:
			continue
		case *anthropic.MessageContentBlock:
			if v == nil {
				continue
			}
			if v.Type == anthropic.MessageContentImageType && v.Source != nil &&
				strings.EqualFold(strings.TrimSpace(v.Source.Type), "url") {
				u := strings.TrimSpace(v.Source.Url)
				if u != "" && !strings.HasPrefix(strings.ToLower(u), "data:") {
					jobs = append(jobs, claudeImageReplaceJob{
						MsgIndex:       msgIndex,
						ContentIndices: append([]int(nil), pathToBlock...),
						URL:            u,
					})
				}
			}
			if v.Type == anthropic.MessageContentToolResultType && v.MessageContentToolResult != nil &&
				len(v.MessageContentToolResult.Content) > 0 {
				jobs = append(jobs, collectFromClaudeContentSlice(msgIndex, pathToBlock, v.MessageContentToolResult.Content)...)
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
