package openai

import (
	"encoding/json"
)

// EmbeddingRequest represents the request structure for OpenAI embeddings API
type EmbeddingRequest struct {
	Input               RequestContentStringOrStringArray `json:"input" binding:"required"`
	Model               string                            `json:"model" binding:"required"`
	NormalizeEmbeddings *bool                             `json:"normalize_embeddings,omitempty"`
}

// RequestContentStringOrStringArray is an interface for input that can be either a string or string array
type RequestContentStringOrStringArray interface {
	isContentStringOrStringArray()
	IsArray() bool
	GetDataLength() int
}

// RequestContentString represents a single string input
type RequestContentString string

func (RequestContentString) isContentStringOrStringArray() {}
func (r RequestContentString) IsArray() bool               { return false }
func (r RequestContentString) GetDataLength() int          { return len(r) }

// RequestContentStringArray represents an array of string inputs
type RequestContentStringArray []string

func (RequestContentStringArray) isContentStringOrStringArray() {}
func (r RequestContentStringArray) IsArray() bool               { return true }
func (r RequestContentStringArray) GetDataLength() int {
	totalLen := 0
	for _, v := range r {
		totalLen += len(v)
	}
	return totalLen
}

// UnmarshalJSON implements custom JSON unmarshaling for EmbeddingRequest
func (m *EmbeddingRequest) UnmarshalJSON(d []byte) error {
	type basicMsg struct {
		Input               json.RawMessage `json:"input"`
		Model               string          `json:"model"`
		NormalizeEmbeddings *bool           `json:"normalize_embeddings,omitempty"`
	}
	bm := &basicMsg{}
	if err := json.Unmarshal(d, bm); err != nil {
		return err
	}
	m.Model = bm.Model
	m.NormalizeEmbeddings = bm.NormalizeEmbeddings

	if bm.Input == nil {
		m.Input = RequestContentString("")
		return nil
	}

	// Try to unmarshal as string first
	cs := ""
	if err := json.Unmarshal(bm.Input, &cs); err == nil {
		m.Input = RequestContentString(cs)
		return nil
	}

	// If not string, must be array
	ca := RequestContentStringArray{}
	if err := json.Unmarshal(bm.Input, &ca); err != nil {
		return err
	}
	m.Input = ca

	return nil
}

// MarshalJSON implements custom JSON marshaling for EmbeddingRequest
func (m EmbeddingRequest) MarshalJSON() ([]byte, error) {
	type Alias struct {
		Input               json.RawMessage `json:"input"`
		Model               string          `json:"model"`
		NormalizeEmbeddings *bool           `json:"normalize_embeddings,omitempty"`
	}

	alias := Alias{
		Model:               m.Model,
		NormalizeEmbeddings: m.NormalizeEmbeddings,
	}

	// Marshal input
	if m.Input != nil {
		inputBytes, err := json.Marshal(m.Input)
		if err != nil {
			return nil, err
		}
		alias.Input = inputBytes
	}

	return json.Marshal(alias)
}
