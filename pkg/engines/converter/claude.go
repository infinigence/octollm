package converter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	anthropicSDK "github.com/anthropics/anthropic-sdk-go"
	openaiSDK "github.com/openai/openai-go/v3"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/anthropic"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// ChatCompletionToClaudeMessages is an engine that handles ClaudeMessages requests with an underlying ChatCompletions engine.
type ChatCompletionsToClaudeMessages struct {
	next octollm.Engine // the engine that can handle ChatCompletions requests
}

var _ octollm.Engine = (*ChatCompletionsToClaudeMessages)(nil)

func NewChatCompletionsToClaudeMessages(next octollm.Engine) *ChatCompletionsToClaudeMessages {
	return &ChatCompletionsToClaudeMessages{next: next}
}

func (e *ChatCompletionsToClaudeMessages) Process(req *octollm.Request) (*octollm.Response, error) {
	newBody, err := e.convertRequestBody(req.Context(), req.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to convert request body: %w", err)
	}
	req.Format = octollm.APIFormatChatCompletions
	req.Body = newBody

	// 4. Call Next Engine
	resp, err := e.next.Process(req)
	if err != nil {
		return nil, err
	}

	// 5. Convert Response
	if resp.Stream != nil {
		newStream, err := e.convertStreamResponse(req.Context(), resp.Stream)
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

func (e *ChatCompletionsToClaudeMessages) convertRequestBody(ctx context.Context, srcBody *octollm.UnifiedBody) (*octollm.UnifiedBody, error) {
	// Parse Input as Anthropic Request
	anthropicReq, err := srcBody.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	src, ok := anthropicReq.(*anthropic.MessageNewParams)
	if !ok {
		// Fallback for when parsed type isn't what we expect
		return nil, fmt.Errorf("parsed body is not *anthropic.MessageNewParams, got %T", anthropicReq)
	}

	dst := &openai.ChatCompletionNewParams{}

	if src.Stream.Valid() {
		dst.Stream = openaiSDK.Bool(src.Stream.Value)
	}

	// Model
	dst.Model = string(src.Model)

	// MaxTokens
	dst.MaxTokens = openaiSDK.Int(src.MaxTokens)

	// Temperature
	if src.Temperature.Valid() {
		dst.Temperature = openaiSDK.Float(src.Temperature.Value)
	}

	// TopP
	if src.TopP.Valid() {
		dst.TopP = openaiSDK.Float(src.TopP.Value)
	}

	// Stop Sequences - Skipped for now due to complex union type mapping
	// if len(src.StopSequences) > 0 { ... }

	// System Prompt -> Messages
	var messages []openaiSDK.ChatCompletionMessageParamUnion
	for _, sysBlock := range src.System {
		// sysBlock is anthropic.TextBlockParam
		// Text field is string.
		// System Message
		msg := openaiSDK.SystemMessage(sysBlock.Text)
		messages = append(messages, msg)
	}

	// Messages
	for _, msg := range src.Messages {
		role := msg.Role // anthropic.MessageParamRole

		if role == anthropicSDK.MessageParamRoleUser {
			var contentParts []openaiSDK.ChatCompletionContentPartUnionParam
			for _, block := range msg.Content {
				// block is anthropic.ContentBlockParamUnion
				if block.OfText != nil {
					// Text
					contentParts = append(contentParts, openaiSDK.TextContentPart(block.OfText.Text))
				} else if block.OfImage != nil {
					// Image
					src := block.OfImage.Source
					var url string
					if src.OfBase64 != nil {
						mediaType := src.OfBase64.MediaType
						data := src.OfBase64.Data
						url = fmt.Sprintf("data:%s;base64,%s", mediaType, data)
					} else if src.OfURL != nil {
						url = src.OfURL.URL
					}
					part := openaiSDK.ChatCompletionContentPartImageImageURLParam{
						URL: url,
					}
					contentParts = append(contentParts, openaiSDK.ImageContentPart(part))
				} else if block.OfToolResult != nil {
					// Tool Result
					var contentParts []openaiSDK.ChatCompletionContentPartTextParam
					for _, content := range block.OfToolResult.Content {
						if content.OfText == nil {
							return nil, fmt.Errorf("tool result content is not text")
						}
						part := openaiSDK.ChatCompletionContentPartTextParam{
							Text: content.OfText.Text,
						}
						contentParts = append(contentParts, part)
					}
					messages = append(messages, openaiSDK.ToolMessage(contentParts, block.OfToolResult.ToolUseID))
				}
				// TODO: other block types
			}

			if len(contentParts) > 0 {
				messages = append(messages, openaiSDK.UserMessage(contentParts))
			}

		} else if role == anthropicSDK.MessageParamRoleAssistant {
			var contentParts []openaiSDK.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion
			var toolCalls []openaiSDK.ChatCompletionMessageToolCallUnionParam
			for _, block := range msg.Content {
				// block is anthropic.ContentBlockParamUnion
				if block.OfText != nil {
					// Text
					part := openaiSDK.ChatCompletionContentPartTextParam{
						Text: block.OfText.Text,
					}
					contentParts = append(contentParts, openaiSDK.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion{OfText: &part})
				} else if block.OfToolUse != nil {
					inputs, err := json.Marshal(block.OfToolUse.Input)
					if err != nil {
						return nil, fmt.Errorf("failed to marshal tool use input: %w", err)
					}
					// Tool Use
					toolCall := &openaiSDK.ChatCompletionMessageFunctionToolCallParam{
						ID: block.OfToolUse.ID,
						Function: openaiSDK.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      block.OfToolUse.Name,
							Arguments: string(inputs),
						},
					}
					toolCalls = append(toolCalls, openaiSDK.ChatCompletionMessageToolCallUnionParam{OfFunction: toolCall})
				}
				// Assistant messages do not support images in OpenAI
			}

			assistantMsg := openaiSDK.AssistantMessage(contentParts)
			if len(toolCalls) > 0 {
				assistantMsg.OfAssistant.ToolCalls = toolCalls
			}
			messages = append(messages, assistantMsg)
		}
	}
	dst.Messages = messages

	// Tools
	for _, tool := range src.Tools {
		if tool.OfTool == nil {
			continue
		}
		// Function
		fdp := openaiSDK.FunctionDefinitionParam{
			Name:        tool.OfTool.Name,
			Description: openaiSDK.String(tool.OfTool.Description.Value),
			Parameters: openaiSDK.FunctionParameters{
				"type":       tool.OfTool.InputSchema.Type,
				"properties": tool.OfTool.InputSchema.Properties,
				"required":   tool.OfTool.InputSchema.Required,
			},
		}
		dst.Tools = append(dst.Tools, openaiSDK.ChatCompletionFunctionTool(fdp))
	}

	// Convert to UnifiedBody
	newBody := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionNewParams]{})
	newBody.SetParsed(dst)

	return newBody, nil
}

func (e *ChatCompletionsToClaudeMessages) convertNonStreamResponseBody(ctx context.Context, srcBody *octollm.UnifiedBody) (*octollm.UnifiedBody, error) {
	// Parse Input as OpenAI Response
	parsed, err := srcBody.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	openaiResp, ok := parsed.(*openaiSDK.ChatCompletion)
	if !ok {
		return nil, fmt.Errorf("parsed body is not *openaiSDK.ChatCompletion, got %T", parsed)
	}

	// Construct Claude Response
	claudeResp := &anthropic.MessageSimple{
		ID:    openaiResp.ID,
		Type:  "message",
		Role:  "assistant",
		Model: openaiResp.Model,
		Usage: &anthropic.MessageUsage{
			InputTokens:  int(openaiResp.Usage.PromptTokens),
			OutputTokens: int(openaiResp.Usage.CompletionTokens),
		},
	}

	// Choices
	if len(openaiResp.Choices) > 0 {
		choice := openaiResp.Choices[0]
		// Finish Reason
		fr := string(choice.FinishReason)
		mappedFr := e.mapFinishReason(fr)
		claudeResp.StopReason = &mappedFr

		msg := choice.Message
		// Content
		if msg.JSON.Content.Valid() {
			claudeResp.Content = append(claudeResp.Content, anthropic.ContentBlockText{
				Type: "text",
				Text: msg.Content,
			})
		}
		for _, toolCall := range msg.ToolCalls {
			if !gjson.Valid(toolCall.Function.Arguments) {
				logrus.WithContext(ctx).Warnf("invalid tool call arguments: %s", toolCall.Function.Arguments)
				continue
			}
			claudeResp.Content = append(claudeResp.Content, anthropic.ContentBlockToolUse{
				Type:  "tool_use",
				ID:    toolCall.ID,
				Name:  toolCall.Function.Name,
				Input: json.RawMessage(toolCall.Function.Arguments),
			})
		}
	}

	// Marshal Claude Response
	claudeBytes, err := json.Marshal(claudeResp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal claude response: %w", err)
	}

	newBody := octollm.NewBodyFromBytes(claudeBytes, &octollm.JSONParser[anthropicSDK.Message]{})

	return newBody, nil
}

func (e *ChatCompletionsToClaudeMessages) convertStreamResponse(ctx context.Context, src *octollm.StreamChan) (*octollm.StreamChan, error) {
	inCh := src.Chan()
	outCh := make(chan *octollm.StreamChunk)

	intPtr := func(i int) *int { return &i }

	go func() {
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
			blockTypeText
			blockTypeTool
		)
		currentBlockType := blockTypeNone
		currentToolCallIndex := int64(-1) // Track which OpenAI tool call index is in current block

		var pendingFinishReason *string
		var pendingUsage *openaiSDK.CompletionUsage

		for chunk := range inCh {
			if ctx.Err() != nil {
				break
			}

			// Parse Chunk
			parsed, err := chunk.Body.Parsed()
			if err != nil {
				if !errors.Is(err, octollm.ErrStreamDone) {
					logrus.WithContext(ctx).Errorf("failed to parse stream chunk: %v", err)
					continue
				}

				// [DONE]

				// Send content_block_stop for current block if one exists
				if currentBlockType != blockTypeNone {
					blockStop := &anthropic.MessageStreamEvent{
						Type:  "content_block_stop",
						Index: intPtr(currentBlockIndex),
					}
					if err := e.sendEvent(outCh, blockStop); err != nil {
						logrus.WithContext(ctx).Errorf("failed to send content_block_stop event: %v", err)
						break
					}
					currentBlockType = blockTypeNone
				}

				// When we have both finish_reason and usage, send message_delta and message_stop
				if pendingFinishReason != nil {
					mappedFr := e.mapFinishReason(*pendingFinishReason)
					msgDelta := &anthropic.MessageStreamEvent{
						Type: "message_delta",
						Delta: &anthropic.MessageDelta{
							StopReason: &mappedFr,
						},
					}
					if pendingUsage != nil {
						msgDelta.Usage = &anthropic.MessageUsage{
							InputTokens:  int(pendingUsage.PromptTokens),
							OutputTokens: int(pendingUsage.CompletionTokens),
						}
					}
					if err := e.sendEvent(outCh, msgDelta); err != nil {
						logrus.WithContext(ctx).Errorf("failed to send message_delta event: %v", err)
						break
					}
					pendingFinishReason = nil
				}

				// Send message_stop
				msgStop := &anthropic.MessageStreamEvent{
					Type: "message_stop",
				}
				if err := e.sendEvent(outCh, msgStop); err != nil {
					logrus.WithContext(ctx).Errorf("failed to send message_stop event: %v", err)
				}
				break
			}

			// Assert to *openaiSDK.ChatCompletionChunk
			openaiChunk, ok := parsed.(*openaiSDK.ChatCompletionChunk)
			if !ok {
				logrus.WithContext(ctx).Errorf("parsed stream chunk is not *openai.ChatCompletionChunk, got %T", parsed)
				continue
			}

			if !started {
				msgID = openaiChunk.ID
				model = openaiChunk.Model
				// Send message_start
				msgStart := &anthropic.MessageStreamEvent{
					Type: "message_start",
					Message: &anthropic.MessageSimple{
						ID:      msgID,
						Type:    "message",
						Role:    "assistant",
						Model:   model,
						Content: []anthropic.ContentBlock{},
						Usage:   &anthropic.MessageUsage{InputTokens: 0, OutputTokens: 0}, // Placeholder
					},
				}
				if err := e.sendEvent(outCh, msgStart); err != nil {
					logrus.WithContext(ctx).Errorf("failed to send message_start event: %v", err)
					continue
				}
				started = true
			}

			// Extract Delta
			var deltaContent string
			var toolCalls []openaiSDK.ChatCompletionChunkChoiceDeltaToolCall

			if len(openaiChunk.Choices) > 0 {
				choice := openaiChunk.Choices[0]
				deltaContent = choice.Delta.Content
				toolCalls = choice.Delta.ToolCalls
				if finishReason := choice.FinishReason; finishReason != "" {
					// record first finish reason
					pendingFinishReason = &finishReason
				}
			}

			// Check if this chunk has usage info
			if openaiChunk.JSON.Usage.Valid() && openaiChunk.Usage.JSON.PromptTokens.Valid() {
				pendingUsage = &openaiChunk.Usage
			}

			// Handle text content
			if deltaContent != "" {
				// Check if we need to start a new block
				needNewBlock := false
				switch currentBlockType {
				case blockTypeNone:
					// No current block, need to start one
					needNewBlock = true
				case blockTypeTool:
					// Switching from tool to text, need new block
					needNewBlock = true
				}
				// If currentBlockType == blockTypeText, continue with existing text block

				if needNewBlock {
					// Close previous block if exists
					if currentBlockType != blockTypeNone {
						blockStop := &anthropic.MessageStreamEvent{
							Type:  "content_block_stop",
							Index: intPtr(currentBlockIndex),
						}
						if err := e.sendEvent(outCh, blockStop); err != nil {
							logrus.WithContext(ctx).Errorf("failed to send content_block_stop event: %v", err)
							continue
						}
					}

					// Create new text block
					currentBlockIndex++

					// Start text block
					blockStart := &anthropic.MessageStreamEvent{
						Type:  "content_block_start",
						Index: intPtr(currentBlockIndex),
						ContentBlock: &anthropic.ContentBlockText{
							Type: "text",
							Text: "",
						},
					}
					if err := e.sendEvent(outCh, blockStart); err != nil {
						logrus.WithContext(ctx).Errorf("failed to send content_block_start for text event: %v", err)
						continue
					}
					currentBlockType = blockTypeText
				}

				// Send text delta
				deltaEvent := &anthropic.MessageStreamEvent{
					Type:  "content_block_delta",
					Index: intPtr(currentBlockIndex),
					Delta: &anthropic.ContentBlockTextDelta{
						Type: "text_delta",
						Text: deltaContent,
					},
				}
				if err := e.sendEvent(outCh, deltaEvent); err != nil {
					logrus.WithContext(ctx).Errorf("failed to send content_block_delta event: %v", err)
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
						// No current block, need to start one
						needNewBlock = true
					case blockTypeText:
						// Switching from text to tool, need new block
						needNewBlock = true
					case blockTypeTool:
						if currentToolCallIndex != toolCall.Index {
							// Switching to different tool call, need new block
							needNewBlock = true
						}
					}

					if needNewBlock {
						// Close previous block if exists
						if currentBlockType != blockTypeNone {
							blockStop := &anthropic.MessageStreamEvent{
								Type:  "content_block_stop",
								Index: intPtr(currentBlockIndex),
							}
							if err := e.sendEvent(outCh, blockStop); err != nil {
								logrus.WithContext(ctx).Errorf("failed to send content_block_stop event: %v", err)
								continue
							}
						}

						// Create new block for this tool call
						currentBlockIndex++

						// Start tool_use block
						blockStart := &anthropic.MessageStreamEvent{
							Type:  "content_block_start",
							Index: intPtr(currentBlockIndex),
							ContentBlock: &anthropic.ContentBlockToolUse{
								Type:  "tool_use",
								ID:    toolCall.ID,
								Name:  toolCall.Function.Name,
								Input: json.RawMessage("{}"),
							},
						}
						if err := e.sendEvent(outCh, blockStart); err != nil {
							logrus.WithContext(ctx).Errorf("failed to send content_block_start for tool_use event: %v", err)
							continue
						}
						currentBlockType = blockTypeTool
						currentToolCallIndex = toolCall.Index
					}

					// Send input_json_delta for tool call arguments
					if toolCall.Function.Arguments != "" {
						deltaEvent := &anthropic.MessageStreamEvent{
							Type:  "content_block_delta",
							Index: intPtr(currentBlockIndex),
							Delta: &anthropic.ContentBlockInputJSONDelta{
								Type:        "input_json_delta",
								PartialJSON: toolCall.Function.Arguments,
							},
						}
						if err := e.sendEvent(outCh, deltaEvent); err != nil {
							logrus.WithContext(ctx).Errorf("failed to send input_json_delta event: %v", err)
							continue
						}
					}
				}
			}
		}
	}()

	newStream := octollm.NewStreamChan(outCh, nil)
	return newStream, nil
}

func (e *ChatCompletionsToClaudeMessages) sendEvent(ch chan<- *octollm.StreamChunk, event *anthropic.MessageStreamEvent) error {
	bytes, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal claude stream event: %w", err)
	}
	body := octollm.NewBodyFromBytes(bytes, &octollm.JSONParser[anthropicSDK.BetaRawMessageStreamEventUnion]{})
	ch <- &octollm.StreamChunk{Body: body, Metadata: map[string]string{"event": event.Type}}
	return nil
}

func (e *ChatCompletionsToClaudeMessages) mapFinishReason(fr string) string {
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
