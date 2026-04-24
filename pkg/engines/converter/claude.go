package converter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

type ChatCompletionToClaudeMessages struct {
	next octollm.Engine // the engine that can handle ChatCompletions requests
}

var _ octollm.Engine = (*ChatCompletionToClaudeMessages)(nil)

func NewChatCompletionToClaudeMessages(next octollm.Engine) *ChatCompletionToClaudeMessages {
	return &ChatCompletionToClaudeMessages{next: next}
}

func (e *ChatCompletionToClaudeMessages) Process(req *octollm.Request) (*octollm.Response, error) {
	slog.InfoContext(req.Context(), "converting request body to ChatCompletions format from ClaudeMessages")
	newBody, err := e.convertRequestBody(req.Context(), req.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to convert request body: %w", err)
	}
	req.Format = octollm.APIFormatChatCompletions
	req.Body = newBody

	// Call Next Engine
	resp, err := e.next.Process(req)
	if err != nil {
		return nil, err
	}

	// Convert Response
	if resp.Stream != nil {
		newStream, err := e.convertStreamResponse(req, resp.Stream)
		if err != nil {
			return nil, fmt.Errorf("failed to convert stream response body: %w", err)
		}
		resp.Stream = newStream
	} else {
		nonStreamResp, err := e.convertNonStreamResponseBody(req.Context(), resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to convert non-stream response body: %w", err)
		}
		resp.Body = nonStreamResp
	}

	return resp, nil
}

func (e *ChatCompletionToClaudeMessages) convertRequestBody(ctx context.Context, srcBody *octollm.UnifiedBody) (*octollm.UnifiedBody, error) {
	// Parse Input as Anthropic Request
	anthropicReq, err := srcBody.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	src, ok := anthropicReq.(*anthropic.ClaudeMessagesRequest)
	if !ok {
		return nil, fmt.Errorf("parsed body is not *anthropic.ClaudeMessagesRequest, got %T", anthropicReq)
	}

	dst := &openai.ChatCompletionRequest{}

	// Stream
	if src.Stream != nil {
		dst.Stream = src.Stream
	}

	if src.Thinking != nil {
		if src.Thinking.Type == "enabled" {
			dst.Thinking = &openai.Thinking{
				Type: "enabled",
			}
		} else {
			dst.Thinking = &openai.Thinking{
				Type: "disabled",
			}
		}
	}

	// Model
	dst.Model = src.Model

	// MaxTokens
	if src.MaxTokens > 0 {
		maxTokens := int(src.MaxTokens)
		dst.MaxTokens = &maxTokens
	}

	// Temperature
	if src.Temperature != nil {
		dst.Temperature = src.Temperature
	}

	// TopP
	if src.TopP != nil {
		dst.TopP = src.TopP
	}

	// TopK
	if src.TopK != nil {
		topK := int(*src.TopK)
		dst.TopK = &topK
	}

	// Stop Sequences
	if len(src.StopSequences) > 0 {
		dst.Stop = &openai.StopUnion{Array: src.StopSequences}
	}

	// Convert System Prompt to Messages
	var messages []*openai.Message
	if src.System != nil {
		switch sys := src.System.(type) {
		case anthropic.SystemString:
			messages = append(messages, &openai.Message{
				Role:    anthropic.MessageParamRoleSystem,
				Content: openai.MessageContentString(sys),
			})
		case anthropic.SystemBlocks:
			for i, block := range sys {
				if i == 0 && strings.HasPrefix(block.Text, "x-anthropic-billing-header:") {
					continue
				}
				messages = append(messages, &openai.Message{
					Role:    anthropic.MessageParamRoleSystem,
					Content: openai.MessageContentString(block.Text),
				})
			}
		}
	}

	// Convert Messages
	for _, msg := range src.Messages {
		role := msg.Role
		if role == anthropic.MessageParamRoleUser {
			var contentParts []*openai.MessageContentItem
			for _, block := range msg.Content {
				contentBlock, ok := block.(*anthropic.MessageContentBlock)
				if !ok {
					// Handle MessageContentString case
					if str, ok := block.(anthropic.MessageContentString); ok {
						contentParts = append(contentParts, &openai.MessageContentItem{
							Type: anthropic.MessageContentTextType,
							Text: string(str),
						})
					}
					continue
				}

				switch contentBlock.Type {
				case anthropic.MessageContentTextType:
					if contentBlock.Text != nil {
						contentParts = append(contentParts, &openai.MessageContentItem{
							Type: anthropic.MessageContentTextType,
							Text: *contentBlock.Text,
						})
					}
				case anthropic.MessageContentImageType:
					if contentBlock.Source != nil {
						url := ""
						if contentBlock.Source.Type == "base64" && len(contentBlock.Source.Data) > 0 {
							mediaType := "image/jpeg"
							if contentBlock.Source.MediaType != "" {
								mediaType = contentBlock.Source.MediaType
							}
							var dataStr string
							if err := json.Unmarshal(contentBlock.Source.Data, &dataStr); err == nil {
								url = fmt.Sprintf("data:%s;base64,%s", mediaType, dataStr)
							}
						} else if contentBlock.Source.Type == "url" {
							url = contentBlock.Source.Url
						}
						contentParts = append(contentParts, &openai.MessageContentItem{
							Type: "image_url",
							ImageURL: &openai.MessageContentItemImageURL{
								URL: url,
							},
						})
					}
				case anthropic.MessageContentToolResultType:
					// Tool result should be converted to tool message
					if contentBlock.MessageContentToolResult != nil && contentBlock.ToolUseID != nil {
						var resultText string
						if len(contentBlock.MessageContentToolResult.Content) > 0 {
							for _, c := range contentBlock.MessageContentToolResult.Content {
								resultText += c.ExtractText()
							}
						}
						messages = append(messages, &openai.Message{
							Role:       anthropic.MessageParamRoleTool,
							Content:    openai.MessageContentString(resultText),
							ToolCallID: *contentBlock.ToolUseID,
						})
					}
				}
			}

			if len(contentParts) > 0 {
				messages = append(messages, &openai.Message{
					Role:    anthropic.MessageParamRoleUser,
					Content: openai.MessageContentArray(contentParts),
				})
			}

		} else if role == anthropic.MessageParamRoleAssistant {
			var contentParts []*openai.MessageContentItem
			var toolCalls []*openai.ToolCall
			toolCallIndex := 0

			for _, block := range msg.Content {
				contentBlock, ok := block.(*anthropic.MessageContentBlock)
				if !ok {
					// Handle MessageContentString case
					if str, ok := block.(anthropic.MessageContentString); ok {
						contentParts = append(contentParts, &openai.MessageContentItem{
							Type: anthropic.MessageContentTextType,
							Text: string(str),
						})
					}
					continue
				}

				if contentBlock.Type == "text" && contentBlock.Text != nil {
					contentParts = append(contentParts, &openai.MessageContentItem{
						Type: anthropic.MessageContentTextType,
						Text: *contentBlock.Text,
					})
				} else if contentBlock.Type == "tool_use" && contentBlock.MessageContentToolUse != nil {
					inputs, err := json.Marshal(contentBlock.MessageContentToolUse.Input)
					if err != nil {
						return nil, fmt.Errorf("failed to marshal tool use input: %w", err)
					}
					toolCalls = append(toolCalls, &openai.ToolCall{
						ID:    contentBlock.MessageContentToolUse.ID,
						Index: toolCallIndex,
						Type:  "function",
						Function: &openai.ToolCallFunction{
							Name:      contentBlock.MessageContentToolUse.Name,
							Arguments: string(inputs),
						},
					})
					toolCallIndex++
				}
			}

			assistantMsg := &openai.Message{
				Role: anthropic.MessageParamRoleAssistant,
			}
			if len(contentParts) > 0 {
				assistantMsg.Content = openai.MessageContentArray(contentParts)
			}
			if len(toolCalls) > 0 {
				assistantMsg.ToolCalls = toolCalls
			}
			messages = append(messages, assistantMsg)
		}
	}
	dst.Messages = messages

	// Convert Tools
	for _, tool := range src.Tools {
		if tool.Name == "" {
			continue
		}
		dst.Tools = append(dst.Tools, &openai.Tool{
			Type: "function",
			Function: openai.ToolFunction{
				Name:        &tool.Name,
				Description: &tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}

	// Convert ToolChoice
	if src.ToolChoice != nil {
		switch src.ToolChoice.Type {
		case "none":
			none := "none"
			dst.ToolChoice = &openai.ToolChoice{String: &none}
		case "auto":
			auto := "auto"
			dst.ToolChoice = &openai.ToolChoice{String: &auto}
		case "any":
			required := "required"
			dst.ToolChoice = &openai.ToolChoice{String: &required}
		case "tool":
			if src.ToolChoice.Name != nil {
				dst.ToolChoice = &openai.ToolChoice{
					Function: &openai.ToolChoiceFunction{
						Type: "function",
						Function: &openai.ToolChoiceFunctionParam{
							Name: *src.ToolChoice.Name,
						},
					},
				}
			}
		}
	}

	// Convert to UnifiedBody
	newBody := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionRequest]{})
	newBody.SetParsed(dst)

	return newBody, nil
}

func (e *ChatCompletionToClaudeMessages) convertNonStreamResponseBody(ctx context.Context, srcBody *octollm.UnifiedBody) (*octollm.UnifiedBody, error) {
	// Parse Input as OpenAI Response
	parsed, err := srcBody.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse response body: %w", err)
	}

	openaiResp, ok := parsed.(*openai.ChatCompletionResponse)
	if !ok {
		return nil, fmt.Errorf("parsed body is not *openai.ChatCompletionResponse, got %T", parsed)
	}

	// Construct Claude Response
	claudeResp := &anthropic.ClaudeMessagesResponse{
		ID:    openaiResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: openaiResp.Model,
	}

	// Usage
	if openaiResp.Usage != nil {
		cached := int64(0)
		// Handle cached tokens if available
		if openaiResp.Usage.PromptTokensDetails != nil && openaiResp.Usage.PromptTokensDetails.CachedTokens > 0 {
			cached = int64(openaiResp.Usage.PromptTokensDetails.CachedTokens)
		}
		claudeResp.Usage = &anthropic.Usage{
			InputTokens:  int64(openaiResp.Usage.PromptTokens) - cached,
			OutputTokens: int64(openaiResp.Usage.CompletionTokens),
		}
		if cached > 0 {
			claudeResp.Usage.CacheReadInputTokens = &cached
		}
	}

	// Choices
	if len(openaiResp.Choices) > 0 {
		choice := openaiResp.Choices[0]
		// Finish Reason
		claudeResp.StopReason = e.mapFinishReason(choice.FinishReason)

		msg := choice.Message
		if msg != nil {
			if msg.ReasoningContent != nil {
				claudeResp.Content = append(claudeResp.Content, &anthropic.MessageContentBlock{
					Type: "thinking",
					MessageContentThinking: &anthropic.MessageContentThinking{
						Thinking: msg.ReasoningContent.ExtractText(),
					},
				})
			}
			// Content
			if msg.Content != nil {
				text := msg.Content.ExtractText()
				if text != "" {
					claudeResp.Content = append(claudeResp.Content, &anthropic.MessageContentBlock{
						Type: "text",
						Text: &text,
					})
				}
			}

			// Tool Calls
			for _, toolCall := range msg.ToolCalls {
				if toolCall.Function == nil {
					continue
				}
				claudeResp.Content = append(claudeResp.Content, &anthropic.MessageContentBlock{
					Type: "tool_use",
					MessageContentToolUse: &anthropic.MessageContentToolUse{
						ID:    toolCall.ID,
						Name:  toolCall.Function.Name,
						Input: json.RawMessage(toolCall.Function.Arguments),
					},
				})
			}
		}
	}

	// Marshal Claude Response
	claudeBytes, err := json.Marshal(claudeResp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal claude response: %w", err)
	}

	newBody := octollm.NewBodyFromBytes(claudeBytes, &octollm.JSONParser[anthropic.ClaudeMessagesResponse]{})

	return newBody, nil
}

func (e *ChatCompletionToClaudeMessages) convertStreamResponse(req *octollm.Request, src *octollm.StreamChan) (*octollm.StreamChan, error) {
	inCh := src.Chan()
	outCh := make(chan *octollm.StreamChunk)

	intPtr := func(i int) *int { return &i }

	octollm.SafeGo(req, func() {
		ctx := req.Context()
		defer close(outCh)
		defer src.Close()

		started := false
		msgID := ""
		model := ""
		currentBlockIndex := -1 // Start at -1, will increment to 0 for first block

		// Track current block state
		type blockType int
		const (
			blockTypeNone blockType = iota
			blockTypeThinking
			blockTypeText
			blockTypeTool
		)
		currentBlockType := blockTypeNone
		currentToolCallIndex := -1 // Track which OpenAI tool call index is in current block

		var pendingFinishReason *string
		var pendingUsage *openai.Usage

		for chunk := range inCh {
			if ctx.Err() != nil {
				break
			}

			// Parse Chunk
			parsed, err := chunk.Body.Parsed()
			if err != nil {
				if !errors.Is(err, octollm.ErrStreamDone) {
					slog.ErrorContext(ctx, fmt.Sprintf("failed to parse stream chunk: %v", err))
					continue
				}

				// [DONE]

				// Send content_block_stop for current block if one exists
				if currentBlockType != blockTypeNone {
					blockStop := &anthropic.ClaudeMessagesStreamEvent{
						Type:  "content_block_stop",
						Index: intPtr(currentBlockIndex),
					}
					if err := e.sendEvent(outCh, blockStop); err != nil {
						slog.ErrorContext(ctx, fmt.Sprintf("failed to send content_block_stop event: %v", err))
						break
					}
					currentBlockType = blockTypeNone
				}

				// When we have both finish_reason and usage, send message_delta and message_stop
				if pendingFinishReason != nil {
					mappedFr := e.mapFinishReason(*pendingFinishReason)
					msgDelta := &anthropic.ClaudeMessagesStreamEvent{
						Type: "message_delta",
					}
					deltaData := &anthropic.ApiMessageDelta{
						StopReason: &mappedFr,
					}
					deltaBytes, _ := json.Marshal(deltaData)
					msgDelta.DeltaRaw = deltaBytes

					if pendingUsage != nil {
						cached := int64(0)
						if pendingUsage.PromptTokensDetails != nil && pendingUsage.PromptTokensDetails.CachedTokens > 0 {
							cached = int64(pendingUsage.PromptTokensDetails.CachedTokens)
						}
						msgDelta.Usage = &anthropic.Usage{
							InputTokens:  int64(pendingUsage.PromptTokens) - cached,
							OutputTokens: int64(pendingUsage.CompletionTokens),
						}
						if cached > 0 {
							msgDelta.Usage.CacheReadInputTokens = &cached
						}
					}
					if err := e.sendEvent(outCh, msgDelta); err != nil {
						slog.ErrorContext(ctx, fmt.Sprintf("failed to send message_delta event: %v", err))
						break
					}
					pendingFinishReason = nil
				}

				// Send message_stop
				msgStop := &anthropic.ClaudeMessagesStreamEvent{
					Type: "message_stop",
				}
				if err := e.sendEvent(outCh, msgStop); err != nil {
					slog.ErrorContext(ctx, fmt.Sprintf("failed to send message_stop event: %v", err))
				}
				break
			}

			openaiChunk, ok := parsed.(*openai.ChatCompletionStreamChunk)
			if !ok {
				slog.ErrorContext(ctx, fmt.Sprintf("parsed stream chunk is not *openai.ChatCompletionStreamResponse, got %T", parsed))
				continue
			}

			if !started {
				msgID = openaiChunk.ID
				model = openaiChunk.Model
				// Send message_start
				msgStart := &anthropic.ClaudeMessagesStreamEvent{
					Type: "message_start",
					Message: &anthropic.ClaudeMessagesResponse{
						ID:      msgID,
						Type:    "message",
						Role:    "assistant",
						Model:   model,
						Content: []anthropic.MessageContent{},
						Usage:   &anthropic.Usage{InputTokens: 0, OutputTokens: 0}, // Placeholder
					},
				}
				if err := e.sendEvent(outCh, msgStart); err != nil {
					slog.ErrorContext(ctx, fmt.Sprintf("failed to send message_start event: %v", err))
					continue
				}
				started = true
			}

			// Extract Delta
			var deltaContent string
			var reasoningContent string
			var toolCalls []*openai.ToolCall

			if len(openaiChunk.Choices) > 0 {
				choice := openaiChunk.Choices[0]
				if choice.Delta != nil {
					if choice.Delta.Content != nil {
						deltaContent = choice.Delta.Content.ExtractText()
					}
					if choice.Delta.ReasoningContent != nil {
						reasoningContent = choice.Delta.ReasoningContent.ExtractText()
					}
					toolCalls = choice.Delta.ToolCalls
				}
				if finishReason := choice.FinishReason; finishReason != "" {
					pendingFinishReason = &choice.FinishReason
				}
			}

			// Check if this chunk has usage info
			if openaiChunk.Usage != nil {
				pendingUsage = openaiChunk.Usage
			}

			// Handle reasoning content (thinking)
			if reasoningContent != "" {
				// Check if we need to start a new block
				needNewBlock := false
				switch currentBlockType {
				case blockTypeNone:
					needNewBlock = true
				case blockTypeText:
					needNewBlock = true
				case blockTypeTool:
					needNewBlock = true
				}

				if needNewBlock {
					// Close previous block if exists
					if currentBlockType != blockTypeNone {
						blockStop := &anthropic.ClaudeMessagesStreamEvent{
							Type:  "content_block_stop",
							Index: intPtr(currentBlockIndex),
						}
						if err := e.sendEvent(outCh, blockStop); err != nil {
							slog.ErrorContext(ctx, fmt.Sprintf("failed to send content_block_stop event: %v", err))
							continue
						}
					}

					// Create new thinking block
					currentBlockIndex++

					// Start thinking block - manually construct JSON to include empty thinking field
					blockStart := &anthropic.ClaudeMessagesStreamEvent{
						Type:  "content_block_start",
						Index: &currentBlockIndex,
						ContentBlock: &anthropic.MessageContentBlock{
							Type: "thinking",
							MessageContentThinking: &anthropic.MessageContentThinking{
								Thinking:  "",
								Signature: "",
							},
						},
					}
					if err := e.sendEvent(outCh, blockStart); err != nil {
						slog.ErrorContext(ctx, fmt.Sprintf("failed to send content_block_start event: %v", err))
						continue
					}
					currentBlockType = blockTypeThinking
				}

				// Send thinking delta
				deltaEvent := &anthropic.ClaudeMessagesStreamEvent{
					Type:  "content_block_delta",
					Index: intPtr(currentBlockIndex),
				}
				deltaData := &anthropic.ApiContentBlockDelta{
					Type:     "thinking_delta",
					Thinking: &reasoningContent,
				}
				deltaBytes, _ := json.Marshal(deltaData)
				deltaEvent.DeltaRaw = deltaBytes

				if err := e.sendEvent(outCh, deltaEvent); err != nil {
					slog.ErrorContext(ctx, fmt.Sprintf("failed to send content_block_delta event: %v", err))
					continue
				}
			}

			// Handle text content
			if deltaContent != "" {
				// Check if we need to start a new block
				needNewBlock := false
				switch currentBlockType {
				case blockTypeNone:
					needNewBlock = true
				case blockTypeThinking:
					needNewBlock = true
				case blockTypeTool:
					needNewBlock = true
				}

				if needNewBlock {
					// Close previous block if exists
					if currentBlockType != blockTypeNone {
						blockStop := &anthropic.ClaudeMessagesStreamEvent{
							Type:  "content_block_stop",
							Index: intPtr(currentBlockIndex),
						}
						if err := e.sendEvent(outCh, blockStop); err != nil {
							slog.ErrorContext(ctx, fmt.Sprintf("failed to send content_block_stop event: %v", err))
							continue
						}
					}

					// Create new text block
					currentBlockIndex++

					// Start text block
					emptyText := ""
					blockStart := &anthropic.ClaudeMessagesStreamEvent{
						Type:  "content_block_start",
						Index: intPtr(currentBlockIndex),
						ContentBlock: &anthropic.MessageContentBlock{
							Type: "text",
							Text: &emptyText,
						},
					}
					if err := e.sendEvent(outCh, blockStart); err != nil {
						slog.ErrorContext(ctx, fmt.Sprintf("failed to send content_block_start for text event: %v", err))
						continue
					}
					currentBlockType = blockTypeText
				}

				// Send text delta
				deltaEvent := &anthropic.ClaudeMessagesStreamEvent{
					Type:  "content_block_delta",
					Index: intPtr(currentBlockIndex),
				}
				deltaData := &anthropic.ApiContentBlockDelta{
					Type: "text_delta",
					Text: &deltaContent,
				}
				deltaBytes, _ := json.Marshal(deltaData)
				deltaEvent.DeltaRaw = deltaBytes

				if err := e.sendEvent(outCh, deltaEvent); err != nil {
					slog.ErrorContext(ctx, fmt.Sprintf("failed to send content_block_delta event: %v", err))
					continue
				}
			}

			// Handle tool calls
			if len(toolCalls) > 0 {
				for _, toolCall := range toolCalls {
					// Check if we need to start a new block
					needNewBlock := false
					switch currentBlockType {
					case blockTypeNone:
						needNewBlock = true
					case blockTypeText:
						needNewBlock = true
					case blockTypeTool:
						if currentToolCallIndex != toolCall.Index {
							needNewBlock = true
						}
					}

					if needNewBlock {
						// Close previous block if exists
						if currentBlockType != blockTypeNone {
							blockStop := &anthropic.ClaudeMessagesStreamEvent{
								Type:  "content_block_stop",
								Index: intPtr(currentBlockIndex),
							}
							if err := e.sendEvent(outCh, blockStop); err != nil {
								slog.ErrorContext(ctx, fmt.Sprintf("failed to send content_block_stop event: %v", err))
								continue
							}
						}

						// Create new block for this tool call
						currentBlockIndex++

						// Start tool_use block
						blockStart := &anthropic.ClaudeMessagesStreamEvent{
							Type:  "content_block_start",
							Index: intPtr(currentBlockIndex),
							ContentBlock: &anthropic.MessageContentBlock{
								Type: "tool_use",
								MessageContentToolUse: &anthropic.MessageContentToolUse{
									ID:    toolCall.ID,
									Name:  toolCall.Function.Name,
									Input: json.RawMessage("{}"),
								},
							},
						}
						if err := e.sendEvent(outCh, blockStart); err != nil {
							slog.ErrorContext(ctx, fmt.Sprintf("failed to send content_block_start for tool_use event: %v", err))
							continue
						}
						currentBlockType = blockTypeTool
						currentToolCallIndex = toolCall.Index
					}

					// Send input_json_delta for tool call arguments
					if toolCall.Function != nil && toolCall.Function.Arguments != "" {
						deltaEvent := &anthropic.ClaudeMessagesStreamEvent{
							Type:  "content_block_delta",
							Index: intPtr(currentBlockIndex),
						}
						deltaData := &anthropic.ApiContentBlockDelta{
							Type:        "input_json_delta",
							PartialJSON: &toolCall.Function.Arguments,
						}
						deltaBytes, _ := json.Marshal(deltaData)
						deltaEvent.DeltaRaw = deltaBytes

						if err := e.sendEvent(outCh, deltaEvent); err != nil {
							slog.ErrorContext(ctx, fmt.Sprintf("failed to send input_json_delta event: %v", err))
							continue
						}
					}
				}
			}
		}
	})

	newStream := octollm.NewStreamChan(outCh, nil)
	return newStream, nil
}

func (e *ChatCompletionToClaudeMessages) sendEvent(ch chan<- *octollm.StreamChunk, event *anthropic.ClaudeMessagesStreamEvent) error {
	bytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal claude stream event: %w", err)
	}
	body := octollm.NewBodyFromBytes(bytes, &octollm.JSONParser[anthropic.ClaudeMessagesStreamEvent]{})
	ch <- &octollm.StreamChunk{Body: body, Metadata: map[string]string{"event": event.Type}}
	return nil
}

func (e *ChatCompletionToClaudeMessages) mapFinishReason(fr string) string {
	switch fr {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	default:
		return fr // Fallback
	}
}
