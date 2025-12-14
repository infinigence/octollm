package anthropic

import (
	"encoding/json"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

type MessageNewParams struct {
	anthropic.MessageNewParams

	Stream param.Opt[bool] `json:"stream,omitzero"`
}

func (r MessageNewParams) MarshalJSON() (data []byte, err error) {
	type shadow anthropic.MessageNewParams
	type shadow1 struct {
		shadow
		Stream param.Opt[bool] `json:"stream,omitzero"`
	}
	return param.MarshalObject(r, shadow1{
		shadow: shadow(r.MessageNewParams),
		Stream: r.Stream,
	})
}

func (r *MessageNewParams) UnmarshalJSON(data []byte) error {
	err := json.Unmarshal(data, &r.MessageNewParams)
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
