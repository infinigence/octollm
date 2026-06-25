package openai

import (
	"encoding/json"
)

// EmbeddingRequest represents the request structure for OpenAI embeddings API
type EmbeddingRequest struct {
	Input               EmbeddingRequestInputValue `json:"input" binding:"required"`
	Model               string                     `json:"model" binding:"required"`
	NormalizeEmbeddings *bool                      `json:"normalize_embeddings,omitempty"`
}

// EmbeddingRequestInputValue is an interface for input that can be either a string or string array
type EmbeddingRequestInputValue interface {
	isEmbeddingRequestInput()
}

// EmbeddingRequestInputString represents a single string input
type EmbeddingRequestInputString string

func (EmbeddingRequestInputString) isEmbeddingRequestInput() {}

// EmbeddingRequestInputStringArray represents an array of string inputs
type EmbeddingRequestInputStringArray []string

func (EmbeddingRequestInputStringArray) isEmbeddingRequestInput() {}
func (r EmbeddingRequestInputStringArray) GetDataLength() int {
	totalLen := 0
	for _, v := range r {
		totalLen += len(v)
	}
	return totalLen
}

// UnmarshalJSON implements custom JSON unmarshaling for EmbeddingRequest
func (m *EmbeddingRequest) UnmarshalJSON(d []byte) error {
	type Alias EmbeddingRequest
	aux := struct {
		Input embeddingInputField `json:"input"`
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(d, &aux); err != nil {
		return err
	}
	m.Input = aux.Input.Value
	return nil
}

type embeddingInputField struct {
	Value EmbeddingRequestInputValue
}

func (f *embeddingInputField) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		f.Value = EmbeddingRequestInputString(s)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	f.Value = EmbeddingRequestInputStringArray(arr)
	return nil
}
