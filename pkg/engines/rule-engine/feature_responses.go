package ruleengine

import (
	"github.com/infinigence/octollm/pkg/types/openai"
)

// replayedMessage is one chat-completions message as the Responses-to-ChatCompletions
// converter would have produced it, reduced to the two texts the feature extractors read.
//
// contentText equals the converted openai.Message's Content.ExtractText(), and
// firstToolCallArgs equals its ToolCalls[0].Function.Arguments ("" when it has no tool calls).
type replayedMessage struct {
	contentText       string
	firstToolCallArgs string
}

// replayResponsesMessages reproduces the message list that
// converter.ChatCompletionToResponses builds from a Responses request, so features computed
// before conversion (e.g. a cache-aware load-balancing shard key) match the ones computed
// after conversion by a downstream service that only sees chat/completions.
//
// KEEP IN SYNC with convertRequestBody in pkg/engines/converter/responses.go.
//
// Only structure is replayed here: item ordering, which items are dropped, and how
// function_call items merge. The text of an item is taken from its own ExtractText(), which
// is defined to render exactly like the converted chat message's content.
func replayResponsesMessages(r *openai.ResponsesRequest) []replayedMessage {
	if r == nil {
		return nil
	}

	var msgs []replayedMessage
	// lastWasToolCall tracks whether the previously emitted message came from a function_call
	// item, i.e. whether a following function_call would be appended to it rather than starting
	// a new message.
	lastWasToolCall := false

	emit := func(contentText, firstToolCallArgs string, fromToolCall bool) {
		msgs = append(msgs, replayedMessage{contentText: contentText, firstToolCallArgs: firstToolCallArgs})
		lastWasToolCall = fromToolCall
	}

	// Instructions become the leading system message, so they occupy the first hash slot.
	if r.Instructions != "" {
		emit(r.Instructions, "", false)
	}

	switch input := r.Input.(type) {
	case openai.ResponsesInputString:
		emit(string(input), "", false)
	case openai.ResponsesInputItemArray:
		for _, item := range input {
			switch it := item.(type) {
			case *openai.ResponsesInputMessage:
				if !responsesMessageIsConverted(it) {
					continue
				}
				emit(it.ExtractText(), "", false)
			case *openai.ResponsesInputFunctionCall:
				// The converter appends this tool call to the preceding assistant message when
				// that message already carries tool calls. Only ToolCalls[0] is ever hashed, so
				// a run of function_call items contributes exactly one text: the first one's
				// arguments.
				//
				// The converter's extra "previous message has no content text" guard cannot
				// fail here: a message it built from a function_call never has content, and one
				// it built from a message item never has tool calls. So "the previous emitted
				// message came from a function_call" is an exact restatement of the merge rule.
				if lastWasToolCall {
					continue
				}
				emit("", it.Arguments, true)
			case *openai.ResponsesInputFunctionCallOutput:
				emit(it.ExtractText(), "", false)
				// reasoning and unrecognized items have no chat-completions equivalent and are
				// dropped by the converter.
			}
		}
	}

	return msgs
}

// responsesMessageIsConverted reports whether the converter would turn this message item into
// a chat message. It drops a message whose content is neither a string nor an array, and one
// whose content array holds no part it can map (only input_text and input_image are mapped,
// and an input_image without an image_url is skipped).
func responsesMessageIsConverted(msg *openai.ResponsesInputMessage) bool {
	if msg == nil {
		return false
	}
	switch content := msg.Content.(type) {
	case openai.ResponsesInputMessageContentString:
		return true
	case openai.ResponsesInputMessageContentArray:
		for _, part := range content {
			if part == nil {
				continue
			}
			switch part.Type {
			case "input_text":
				return true
			case "input_image":
				if part.ImageURL != nil {
					return true
				}
			}
		}
		return false
	default:
		return false
	}
}

// computeResponsesMessageNHashesV2 applies the v2 hashing of computeMessageNHashesV2 to the
// messages the converter would derive from a Responses request.
func computeResponsesMessageNHashesV2(r *openai.ResponsesRequest, n int) []string {
	if n <= 0 {
		return nil
	}
	hasher := newMessageNHasherV2(n)
	msgs := replayResponsesMessages(r)
	for i := 0; i < len(msgs) && hasher.count() < n; i++ {
		hasher.add(messageTextForHashV2(msgs[i].contentText, msgs[i].firstToolCallArgs))
	}
	return hasher.hashes
}

// computeResponsesMessage5Hashes applies the v1 hashing of computeMessage5Hashes to the
// messages the converter would derive from a Responses request.
func computeResponsesMessage5Hashes(r *openai.ResponsesRequest) []string {
	hasher := newMessage5HasherV1()
	msgs := replayResponsesMessages(r)
	for i := 0; i < len(msgs) && hasher.count() < 5; i++ {
		hasher.add(textForHash(msgs[i].contentText, msgs[i].firstToolCallArgs))
	}
	return hasher.hashes
}
