package openai

import (
	"encoding/json"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
)

type ChatCompletionNewParams struct {
	openai.ChatCompletionNewParams

	Stream param.Opt[bool] `json:"stream,omitzero"`
}

func (r ChatCompletionNewParams) MarshalJSON() (data []byte, err error) {
	type shadow openai.ChatCompletionNewParams
	type shadow1 struct {
		shadow
		Stream param.Opt[bool] `json:"stream,omitzero"`
	}
	return param.MarshalObject(r, shadow1{
		shadow: shadow(r.ChatCompletionNewParams),
		Stream: r.Stream,
	})
}

func (r *ChatCompletionNewParams) UnmarshalJSON(data []byte) error {
	err := json.Unmarshal(data, &r.ChatCompletionNewParams)
	if err != nil {
		return err
	}
	type stream struct {
		Stream param.Opt[bool] `json:"stream,omitzero"`
	}
	var s stream
	err = json.Unmarshal(data, &s)
	if err != nil {
		return err
	}
	r.Stream = s.Stream

	return nil
}
