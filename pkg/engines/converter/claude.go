package converter

import (
	"context"
	"encoding/json"
	"fmt"

	anthropicSDK "github.com/anthropics/anthropic-sdk-go"
	openaiSDK "github.com/openai/openai-go/v3"
	"github.com/sirupsen/logrus"

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
	// 1. Parse Input as Anthropic Request
	anthropicReq, err := req.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	anthropicMsgReq, ok := anthropicReq.(*anthropic.MessageNewParams)
	if !ok {
		// Fallback for when parsed type isn't what we expect
		return nil, fmt.Errorf("parsed body is not *anthropic.MessageNewParams, got %T", anthropicReq)
	}

	// 2. Convert to OpenAI Request
	openaiReqStruct := e.convertRequest(anthropicMsgReq)

	// 3. Create new Body with OpenAI Request
	newBody := octollm.NewBodyFromBytes([]byte{}, &octollm.JSONParser[openai.ChatCompletionNewParams]{})
	newBody.SetParsed(openaiReqStruct)

	req.Format = octollm.APIFormatChatCompletions
	req.Body = newBody

	// 4. Call Next Engine
	resp, err := e.next.Process(req)
	if err != nil {
		return nil, err
	}

	// 5. Convert Response
	if resp.Stream != nil {
		return e.handleStreamResponse(req.Context(), resp)
	}
	return e.handleNonStreamResponse(resp)
}

func (e *ChatCompletionsToClaudeMessages) convertRequest(src *anthropic.MessageNewParams) *openai.ChatCompletionNewParams {
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
				}
				// TODO: other block types
			}

			messages = append(messages, openaiSDK.UserMessage(contentParts))

		} else if role == anthropicSDK.MessageParamRoleAssistant {
			var contentParts []openaiSDK.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion
			for _, block := range msg.Content {
				// block is anthropic.ContentBlockParamUnion
				if block.OfText != nil {
					// Text
					part := openaiSDK.ChatCompletionContentPartTextParam{
						Text: block.OfText.Text,
					}
					contentParts = append(contentParts, openaiSDK.ChatCompletionAssistantMessageParamContentArrayOfContentPartUnion{OfText: &part})
				}
				// Assistant messages do not support images in OpenAI
			}

			messages = append(messages, openaiSDK.AssistantMessage(contentParts))
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

	return dst
}

func (e *ChatCompletionsToClaudeMessages) handleNonStreamResponse(resp *octollm.Response) (*octollm.Response, error) {
	// Parse OpenAI Response
	parsed, err := resp.Body.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse upstream response: %w", err)
	}

	// Assert to *openai.ChatCompletion
	openaiResp, ok := parsed.(*openaiSDK.ChatCompletion)
	if !ok {
		return nil, fmt.Errorf("parsed body is not *openai.ChatCompletion, got %T", parsed)
	}

	// Construct Claude Response
	claudeResp := anthropic.MessageSimple{
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

	resp.Body = newBody
	return resp, nil
}

func (e *ChatCompletionsToClaudeMessages) handleStreamResponse(ctx context.Context, resp *octollm.Response) (*octollm.Response, error) {
	inCh := resp.Stream.Chan()
	outCh := make(chan *octollm.StreamChunk)

	intPtr := func(i int) *int { return &i }

	go func() {
		defer close(outCh)
		defer resp.Stream.Close()

		started := false
		msgID := ""
		model := ""

		for chunk := range inCh {
			if ctx.Err() != nil {
				return
			}

			// Parse Chunk
			parsed, err := chunk.Body.Parsed()
			if err != nil {
				logrus.WithContext(ctx).Errorf("failed to parse stream chunk: %v", err)
				continue
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
						ID:    msgID,
						Type:  "message",
						Role:  "assistant",
						Model: model,
						Usage: &anthropic.MessageUsage{InputTokens: 0, OutputTokens: 0}, // Placeholder
					},
				}
				if err := e.sendEvent(outCh, msgStart); err != nil {
					logrus.WithContext(ctx).Errorf("failed to send message_start event: %v", err)
					continue
				}

				// Send content_block_start
				blockStart := &anthropic.MessageStreamEvent{
					Type:  "content_block_start",
					Index: intPtr(0),
					ContentBlock: &anthropic.ContentBlockText{
						Type: "text",
						Text: "",
					},
				}
				e.sendEvent(outCh, blockStart)
				started = true
			}

			// Extract Delta
			var deltaContent string
			var finishReason *string

			if len(openaiChunk.Choices) > 0 {
				choice := openaiChunk.Choices[0]
				deltaContent = choice.Delta.Content
				fr := string(choice.FinishReason)
				if fr != "" {
					finishReason = &fr
				}
			}

			// Send content_block_delta
			if deltaContent != "" {
				deltaEvent := &anthropic.MessageStreamEvent{
					Type:  "content_block_delta",
					Index: intPtr(0),
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

			// Handle Finish
			if finishReason != nil {
				// Send content_block_stop
				blockStop := &anthropic.MessageStreamEvent{
					Type:  "content_block_stop",
					Index: intPtr(0),
				}
				if err := e.sendEvent(outCh, blockStop); err != nil {
					logrus.WithContext(ctx).Errorf("failed to send content_block_stop event: %v", err)
					continue
				}

				// Send message_delta
				mappedFr := e.mapFinishReason(*finishReason)
				msgDelta := &anthropic.MessageStreamEvent{
					Type: "message_delta",
					Delta: &anthropic.MessageDelta{
						StopReason: &mappedFr,
					},
					Usage: &anthropic.MessageUsage{OutputTokens: 0}, // Placeholder as we don't track count yet
				}
				if err := e.sendEvent(outCh, msgDelta); err != nil {
					logrus.WithContext(ctx).Errorf("failed to send message_delta event: %v", err)
					continue
				}

				// Send message_stop
				msgStop := &anthropic.MessageStreamEvent{
					Type: "message_stop",
				}
				if err := e.sendEvent(outCh, msgStop); err != nil {
					logrus.WithContext(ctx).Errorf("failed to send message_stop event: %v", err)
					continue
				}
			}
		}
	}()

	newStream := octollm.NewStreamChan(outCh, nil)
	resp.Stream = newStream
	return resp, nil
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
