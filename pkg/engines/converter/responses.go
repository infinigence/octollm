package converter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/infinigence/octollm/pkg/types/openai"
)

// ChatCompletionToResponses converts OpenAI Responses API (POST /v1/responses)
// requests to Chat Completions (POST /v1/chat/completions) before handing them
// to the downstream engine, and converts the Chat Completions responses back to
// the Responses API shape.
//
// It mirrors ChatCompletionToClaudeMessages: the downstream engine natively
// speaks Chat Completions, and this engine adapts Responses traffic onto it.
type ChatCompletionToResponses struct {
	next octollm.Engine // the engine that can handle Chat Completions requests
}

var _ octollm.Engine = (*ChatCompletionToResponses)(nil)

func NewChatCompletionToResponses(next octollm.Engine) *ChatCompletionToResponses {
	return &ChatCompletionToResponses{next: next}
}

func (e *ChatCompletionToResponses) Process(req *octollm.Request) (*octollm.Response, error) {
	slog.InfoContext(req.Context(), "converting request body to ChatCompletions format from Responses")
	newBody, err := e.convertRequestBody(req.Context(), req.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to convert request body: %w", err)
	}
	newReq := req.WithBody(newBody)
	newReq.Format = octollm.APIFormatChatCompletions

	// Call Next Engine
	resp, err := e.next.Process(newReq)
	if err != nil {
		return nil, err
	}

	// Convert Response
	if resp.Stream != nil {
		newStream, err := e.convertStreamResponse(req, resp.Stream)
		if err != nil {
			resp.Stream.Close()
			return nil, fmt.Errorf("failed to convert stream response body: %w", err)
		}
		resp.Stream = newStream
	} else {
		nonStreamResp, err := e.convertNonStreamResponseBody(req.Context(), resp.Body)
		if err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to convert non-stream response body: %w", err)
		}
		resp.Body = nonStreamResp
	}

	return resp, nil
}

// convertRequestBody flattens a Responses request into a Chat Completions request.
//
// The message-flattening rules here are replayed by replayResponsesMessages in
// pkg/engines/rule-engine/feature_responses.go so that features computed before conversion
// (a cache-aware shard key, say) match the ones a downstream service computes from the
// converted body. KEEP THE TWO IN SYNC: nothing checks it automatically.
func (e *ChatCompletionToResponses) convertRequestBody(ctx context.Context, srcBody *octollm.UnifiedBody) (*octollm.UnifiedBody, error) {
	parsed, err := srcBody.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	src, ok := parsed.(*openai.ResponsesRequest)
	if !ok {
		return nil, fmt.Errorf("parsed body is not *openai.ResponsesRequest, got %T", parsed)
	}

	dst := &openai.ChatCompletionRequest{}

	// Stream
	if src.Stream != nil {
		dst.Stream = src.Stream
	}

	// Model
	dst.Model = src.Model

	// MaxOutputTokens -> MaxTokens
	if src.MaxOutputTokens != nil {
		maxTokens := *src.MaxOutputTokens
		dst.MaxTokens = &maxTokens
	}

	// Temperature / TopP
	dst.Temperature = src.Temperature
	dst.TopP = src.TopP

	// Reasoning effort
	if src.Reasoning != nil && src.Reasoning.Effort != "" {
		effort := src.Reasoning.Effort
		dst.ReasoningEffort = &effort
	}

	// Instructions -> system message
	var messages []*openai.Message
	if src.Instructions != "" {
		messages = append(messages, &openai.Message{
			Role:    "system",
			Content: openai.MessageContentString(src.Instructions),
		})
	}

	// Input -> messages
	toolCallIndex := 0
	switch input := src.Input.(type) {
	case openai.ResponsesInputString:
		messages = append(messages, &openai.Message{
			Role:    "user",
			Content: openai.MessageContentString(input),
		})
	case openai.ResponsesInputItemArray:
		for _, item := range input {
			switch it := item.(type) {
			case *openai.ResponsesInputMessage:
				if msg := convertResponsesInputMessage(it); msg != nil {
					messages = append(messages, msg)
				}
			case *openai.ResponsesInputFunctionCall:
				toolCall := &openai.MessageToolCall{
					ID:    it.CallID,
					Index: toolCallIndex,
					Type:  "function",
					Function: &openai.ToolCallFunction{
						Name:      it.Name,
						Arguments: it.Arguments,
					},
				}
				toolCallIndex++
				if last := lastAssistantToolCallMessage(messages); last != nil {
					last.ToolCalls = append(last.ToolCalls, toolCall)
				} else {
					messages = append(messages, &openai.Message{
						Role:      "assistant",
						ToolCalls: []*openai.MessageToolCall{toolCall},
					})
				}
			case *openai.ResponsesInputFunctionCallOutput:
				messages = append(messages, &openai.Message{
					Role:       "tool",
					ToolCallID: it.CallID,
					Content:    openai.MessageContentString(it.Output.ExtractText()),
				})
				// reasoning and unknown items are not mapped: a reasoning item only
				// appears when replaying a prior turn and has no chat-completions
				// equivalent that round-trips cleanly.
			}
		}
	}
	dst.Messages = messages

	// Tools
	for _, tool := range src.Tools {
		if tool == nil || tool.Type != "function" {
			continue
		}
		name := tool.Name
		desc := tool.Description
		dst.Tools = append(dst.Tools, &openai.Tool{
			Type: "function",
			Function: &openai.ToolFunction{
				Name:        &name,
				Description: &desc,
				Parameters:  tool.Parameters,
			},
		})
	}

	// ToolChoice
	if src.ToolChoice != nil {
		dst.ToolChoice = convertResponsesToolChoice(src.ToolChoice)
	}

	return octollm.NewBodyFromParsed(dst, &octollm.JSONParser[openai.ChatCompletionRequest]{}), nil
}

func convertResponsesInputMessage(src *openai.ResponsesInputMessage) *openai.Message {
	msg := &openai.Message{Role: src.Role}
	switch content := src.Content.(type) {
	case openai.ResponsesInputMessageContentString:
		msg.Content = openai.MessageContentString(content)
	case openai.ResponsesInputMessageContentArray:
		var parts []*openai.MessageContentItem
		for _, part := range content {
			if part == nil {
				continue
			}
			switch part.Type {
			case "input_text":
				parts = append(parts, &openai.MessageContentItem{Type: "text", Text: part.Text})
			case "input_image":
				if part.ImageURL != nil {
					parts = append(parts, &openai.MessageContentItem{
						Type:     "image_url",
						ImageURL: &openai.MessageContentItemImageURL{URL: part.ImageURL.GetImageUrl()},
					})
				}
			}
		}
		if len(parts) == 0 {
			return nil
		}
		msg.Content = openai.MessageContentArray(parts)
	default:
		return nil
	}
	return msg
}

func lastAssistantToolCallMessage(messages []*openai.Message) *openai.Message {
	if len(messages) == 0 {
		return nil
	}
	last := messages[len(messages)-1]
	if last == nil || last.Role != "assistant" || len(last.ToolCalls) == 0 {
		return nil
	}
	if last.Content != nil && last.Content.ExtractText() != "" {
		return nil
	}
	return last
}

func convertResponsesToolChoice(tc openai.ResponsesToolChoiceValue) openai.ToolChoiceValue {
	switch v := tc.(type) {
	case openai.ResponsesToolChoiceString:
		return openai.ToolChoiceString(v)
	case openai.ResponsesToolChoiceObject:
		if v.Type == "function" {
			return openai.ToolChoiceObject{
				Type:     "function",
				Function: &openai.ToolChoiceFunction{Name: v.Name},
			}
		}
	}
	return nil
}

func (e *ChatCompletionToResponses) convertNonStreamResponseBody(ctx context.Context, srcBody *octollm.UnifiedBody) (*octollm.UnifiedBody, error) {
	parsed, err := srcBody.Parsed()
	if err != nil {
		return nil, fmt.Errorf("failed to parse response body: %w", err)
	}
	srcBody.Close()

	chatResp, ok := parsed.(*openai.ChatCompletionResponse)
	if !ok {
		return nil, fmt.Errorf("parsed body is not *openai.ChatCompletionResponse, got %T", parsed)
	}

	dst := &openai.ResponsesResponse{
		Id:      chatResp.ID,
		Object:  "response",
		Created: chatResp.Created,
		Model:   chatResp.Model,
		Status:  "completed",
	}

	// Usage
	dst.Usage = convertResponsesUsage(chatResp.Usage)

	if len(chatResp.Choices) > 0 {
		choice := chatResp.Choices[0]
		if choice.FinishReason == "length" {
			dst.Status = "incomplete"
			dst.IncompleteDetails = &openai.ResponsesIncompleteDetails{Reason: "max_output_tokens"}
		}

		msg := choice.Message
		if msg != nil {
			outputIndex := 0

			// Reasoning -> reasoning item
			if msg.ReasoningContent != nil {
				if reasoning := msg.ReasoningContent.ExtractText(); reasoning != "" {
					dst.Output = append(dst.Output, &openai.ResponsesOutputItem{
						ID:   fmt.Sprintf("rs_%d", outputIndex),
						Type: "reasoning",
						Summary: []*openai.ResponsesReasoningSummaryPart{
							{Type: "summary_text", Text: reasoning},
						},
					})
					outputIndex++
				}
			}

			// Text -> message item
			if msg.Content != nil {
				if text := msg.Content.ExtractText(); text != "" {
					dst.Output = append(dst.Output, &openai.ResponsesOutputItem{
						ID:   fmt.Sprintf("msg_%d", outputIndex),
						Type: "message",
						Role: "assistant",
						Content: []*openai.ResponsesOutputContentItem{
							{Type: "output_text", Text: text},
						},
					})
					outputIndex++
				}
			}

			// Tool calls -> function_call items
			for _, toolCall := range msg.ToolCalls {
				if toolCall == nil || toolCall.Function == nil {
					continue
				}
				// Arguments is opaque upstream text and can be empty or truncated
				// (e.g. a tool call cut off by max_tokens). It is emitted verbatim as
				// the function_call arguments, so invalid JSON would fail downstream;
				// fall back to an empty object instead.
				arguments := toolCall.Function.Arguments
				if !json.Valid([]byte(arguments)) {
					arguments = "{}"
				}
				dst.Output = append(dst.Output, &openai.ResponsesOutputItem{
					ID:        fmt.Sprintf("fc_%d", outputIndex),
					Type:      "function_call",
					CallID:    toolCall.ID,
					Name:      toolCall.Function.Name,
					Arguments: arguments,
				})
				outputIndex++
			}
		}
	}

	return octollm.NewBodyFromParsed(dst, &octollm.JSONParser[openai.ResponsesResponse]{}), nil
}

// convertResponsesUsage maps Chat Completions usage onto the Responses API usage
// shape (input_tokens / output_tokens / total_tokens plus token details).
func convertResponsesUsage(u *openai.Usage) *openai.ResponsesUsage {
	if u == nil {
		return nil
	}
	usage := &openai.ResponsesUsage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  u.TotalTokens,
	}
	if d := u.PromptTokensDetails; d != nil {
		details := &openai.ResponsesInputTokenDetails{}
		hasInfo := false
		if d.CachedTokens != nil {
			details.CachedTokens = d.CachedTokens
			hasInfo = true
		}
		if d.CacheWriteTokens != nil {
			details.CacheWriteTokens = d.CacheWriteTokens
			hasInfo = true
		}
		if hasInfo {
			usage.InputTokensDetails = details
		}
	}
	if d := u.CompletionTokensDetails; d != nil && d.ReasoningTokens != nil {
		usage.OutputTokensDetails = &openai.ResponsesOutputTokenDetails{
			ReasoningTokens: d.ReasoningTokens,
		}
	}
	return usage
}

func (e *ChatCompletionToResponses) convertStreamResponse(req *octollm.Request, src *octollm.StreamChan) (*octollm.StreamChan, error) {
	inCh := src.Chan()
	outCh := make(chan *octollm.StreamChunk)
	ctx, cancel := context.WithCancel(req.Context())

	intPtr := func(i int) *int { return &i }

	octollm.SafeGo(req, func() {
		defer close(outCh)
		defer src.Close()
		defer cancel()

		started := false
		msgID := ""
		model := ""
		created := 0

		type itemKind int
		const (
			kindNone itemKind = iota
			kindReasoning
			kindText
			kindTool
		)
		currentKind := kindNone
		outputIndex := -1 // last assigned output item index

		var textBuffer string
		var reasoningBuffer string

		// tool state keyed by the Chat Completions tool-call index.
		type toolState struct {
			outputIndex int
			itemID      string
			callID      string
			name        string
			args        string
		}
		tools := map[int]*toolState{}
		currentToolIndex := -1

		var pendingFinishReason string
		var pendingUsage *openai.Usage

		// finalOutput accumulates completed items for the response.completed snapshot.
		var finalOutput []*openai.ResponsesOutputItem

		closeCurrent := func() {
			switch currentKind {
			case kindText:
				textItem := &openai.ResponsesOutputItem{
					ID:   fmt.Sprintf("msg_%d", outputIndex),
					Type: "message",
					Role: "assistant",
					Content: []*openai.ResponsesOutputContentItem{
						{Type: "output_text", Text: textBuffer},
					},
				}
				_ = e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
					Type: "response.output_text.done", Text: textBuffer, OutputIdx: intPtr(outputIndex), ContentIdx: intPtr(0),
				})
				_ = e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
					Type: "response.content_part.done", OutputIdx: intPtr(outputIndex), ContentIdx: intPtr(0),
				})
				_ = e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
					Type: "response.output_item.done", OutputIdx: intPtr(outputIndex), Item: textItem,
				})
				finalOutput = append(finalOutput, textItem)
				textBuffer = ""
			case kindReasoning:
				reasoningItem := &openai.ResponsesOutputItem{
					ID:   fmt.Sprintf("rs_%d", outputIndex),
					Type: "reasoning",
					Summary: []*openai.ResponsesReasoningSummaryPart{
						{Type: "summary_text", Text: reasoningBuffer},
					},
				}
				_ = e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
					Type: "response.reasoning_summary_text.done", Text: reasoningBuffer, ItemID: fmt.Sprintf("rs_%d", outputIndex), OutputIdx: intPtr(outputIndex),
				})
				_ = e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
					Type: "response.output_item.done", OutputIdx: intPtr(outputIndex), Item: reasoningItem,
				})
				finalOutput = append(finalOutput, reasoningItem)
				reasoningBuffer = ""
			case kindTool:
				if ts := tools[currentToolIndex]; ts != nil {
					arguments := ts.args
					if !json.Valid([]byte(arguments)) {
						arguments = "{}"
					}
					toolItem := &openai.ResponsesOutputItem{
						ID:        ts.itemID,
						Type:      "function_call",
						CallID:    ts.callID,
						Name:      ts.name,
						Arguments: arguments,
					}
					_ = e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
						Type: "response.function_call_arguments.done", ItemID: ts.itemID, OutputIdx: intPtr(ts.outputIndex), Arguments: arguments,
					})
					_ = e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
						Type: "response.output_item.done", OutputIdx: intPtr(ts.outputIndex), Item: toolItem,
					})
					finalOutput = append(finalOutput, toolItem)
				}
			}
			currentKind = kindNone
		}

		for chunk := range inCh {
			if ctx.Err() != nil {
				break
			}

			parsed, err := chunk.Body.Parsed()
			if err != nil {
				if !errors.Is(err, octollm.ErrStreamDone) {
					slog.ErrorContext(ctx, fmt.Sprintf("failed to parse stream chunk: %v", err))
					continue
				}

				// [DONE]
				closeCurrent()

				status := "completed"
				if pendingFinishReason == "length" {
					status = "incomplete"
				}
				completedResp := &openai.ResponsesResponse{
					Id:      msgID,
					Object:  "response",
					Created: created,
					Status:  status,
					Model:   model,
					Output:  finalOutput,
					Usage:   convertResponsesUsage(pendingUsage),
				}
				if status == "incomplete" {
					completedResp.IncompleteDetails = &openai.ResponsesIncompleteDetails{Reason: "max_output_tokens"}
				}
				if err := e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
					Type: "response.completed", Response: completedResp,
				}); err != nil {
					slog.ErrorContext(ctx, fmt.Sprintf("failed to send response.completed event: %v", err))
				}
				break
			}

			openaiChunk, ok := parsed.(*openai.ChatCompletionStreamChunk)
			if !ok {
				slog.ErrorContext(ctx, fmt.Sprintf("parsed stream chunk is not *openai.ChatCompletionStreamChunk, got %T", parsed))
				continue
			}

			if !started {
				msgID = openaiChunk.ID
				model = openaiChunk.Model
				created = openaiChunk.Created
				if err := e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
					Type: "response.created",
					Response: &openai.ResponsesResponse{
						Id:      msgID,
						Object:  "response",
						Created: created,
						Status:  "in_progress",
						Model:   model,
						Usage:   &openai.ResponsesUsage{InputTokens: 0, OutputTokens: 0, TotalTokens: 0},
					},
				}); err != nil {
					slog.ErrorContext(ctx, fmt.Sprintf("failed to send response.created event: %v", err))
					continue
				}
				started = true
			}

			var deltaContent string
			var reasoningContent string
			var toolCalls []*openai.MessageToolCall

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
				if fr := choice.FinishReason; fr != "" {
					pendingFinishReason = fr
				}
			}
			if openaiChunk.Usage != nil {
				pendingUsage = openaiChunk.Usage
			}

			// Reasoning delta -> reasoning item + summary text delta.
			if reasoningContent != "" {
				if currentKind != kindReasoning {
					closeCurrent()
					outputIndex++
					itemID := fmt.Sprintf("rs_%d", outputIndex)
					if err := e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
						Type: "response.output_item.added", OutputIdx: intPtr(outputIndex),
						Item: &openai.ResponsesOutputItem{ID: itemID, Type: "reasoning", Summary: []*openai.ResponsesReasoningSummaryPart{}},
					}); err != nil {
						slog.ErrorContext(ctx, fmt.Sprintf("failed to send output_item.added event: %v", err))
						continue
					}
					currentKind = kindReasoning
				}
				reasoningBuffer += reasoningContent
				if err := e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
					Type: "response.reasoning_summary_text.delta", ItemID: fmt.Sprintf("rs_%d", outputIndex), OutputIdx: intPtr(outputIndex), Delta: reasoningContent,
				}); err != nil {
					slog.ErrorContext(ctx, fmt.Sprintf("failed to send reasoning_summary_text.delta event: %v", err))
				}
			}

			// Text delta -> message item + content part + output_text delta.
			if deltaContent != "" {
				if currentKind != kindText {
					closeCurrent()
					outputIndex++
					itemID := fmt.Sprintf("msg_%d", outputIndex)
					if err := e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
						Type: "response.output_item.added", OutputIdx: intPtr(outputIndex),
						Item: &openai.ResponsesOutputItem{ID: itemID, Type: "message", Role: "assistant", Content: []*openai.ResponsesOutputContentItem{}},
					}); err != nil {
						slog.ErrorContext(ctx, fmt.Sprintf("failed to send output_item.added event: %v", err))
						continue
					}
					if err := e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
						Type: "response.content_part.added", OutputIdx: intPtr(outputIndex), ContentIdx: intPtr(0),
						Part: &openai.ResponsesOutputContentItem{Type: "output_text", Text: ""},
					}); err != nil {
						slog.ErrorContext(ctx, fmt.Sprintf("failed to send content_part.added event: %v", err))
						continue
					}
					currentKind = kindText
				}
				textBuffer += deltaContent
				if err := e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
					Type: "response.output_text.delta", OutputIdx: intPtr(outputIndex), ContentIdx: intPtr(0), Delta: deltaContent,
				}); err != nil {
					slog.ErrorContext(ctx, fmt.Sprintf("failed to send output_text.delta event: %v", err))
				}
			}

			// Tool call delta -> function_call item + arguments delta.
			for _, tc := range toolCalls {
				if tc == nil {
					continue
				}
				idx := tc.Index
				needNew := currentKind != kindTool || currentToolIndex != idx
				if needNew {
					closeCurrent()
					outputIndex++
					ts := tools[idx]
					if ts == nil {
						ts = &toolState{}
						tools[idx] = ts
					}
					ts.outputIndex = outputIndex
					ts.itemID = fmt.Sprintf("fc_%d", outputIndex)
					ts.callID = tc.ID
					ts.name = ""
					if tc.Function != nil {
						ts.name = tc.Function.Name
					}
					if ts.callID == "" {
						ts.callID = fmt.Sprintf("call_%d", idx)
					}
					if err := e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
						Type: "response.output_item.added", OutputIdx: intPtr(ts.outputIndex),
						Item: &openai.ResponsesOutputItem{ID: ts.itemID, Type: "function_call", CallID: ts.callID, Name: ts.name, Arguments: ""},
					}); err != nil {
						slog.ErrorContext(ctx, fmt.Sprintf("failed to send output_item.added event: %v", err))
						continue
					}
					currentKind = kindTool
					currentToolIndex = idx
				}
				if tc.Function != nil && tc.Function.Arguments != "" {
					ts := tools[idx]
					if ts == nil {
						continue
					}
					ts.args += tc.Function.Arguments
					if err := e.sendResponsesChunk(ctx, outCh, &openai.ResponseStreamChunk{
						Type: "response.function_call_arguments.delta", ItemID: ts.itemID, OutputIdx: intPtr(ts.outputIndex), Delta: tc.Function.Arguments,
					}); err != nil {
						slog.ErrorContext(ctx, fmt.Sprintf("failed to send function_call_arguments.delta event: %v", err))
					}
				}
			}
		}
	})

	newStream := octollm.NewStreamChan(outCh, cancel)
	return newStream, nil
}

func (e *ChatCompletionToResponses) sendResponsesChunk(ctx context.Context, ch chan<- *octollm.StreamChunk, chunk *openai.ResponseStreamChunk) error {
	bytes, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("failed to marshal responses stream chunk: %w", err)
	}
	body := octollm.NewBodyFromBytes(bytes, &octollm.JSONParser[openai.ResponseStreamChunk]{})
	select {
	case ch <- &octollm.StreamChunk{Body: body, Metadata: map[string]string{"event": chunk.Type}}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
