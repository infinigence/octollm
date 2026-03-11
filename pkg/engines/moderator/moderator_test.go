package moderator

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/infinigence/octollm/pkg/engines/mock"
	"github.com/infinigence/octollm/pkg/internal/testhelper"
	"github.com/infinigence/octollm/pkg/octollm"
)

type mockTextModeratorService struct {
	// t          *testing.T
	isSpamFunc func(text []rune) bool
	maxRuneLen int
}

func (s *mockTextModeratorService) Allow(ctx context.Context, text []rune) error {
	if s.isSpamFunc != nil && s.isSpamFunc(text) {
		return fmt.Errorf("moderation rejected text")
	}
	return nil
}

func (s *mockTextModeratorService) MaxRuneLen() int {
	return s.maxRuneLen
}

func strContainsFactory(strs ...string) func(text []rune) bool {
	return func(text []rune) bool {
		for _, str := range strs {
			if strings.Contains(string(text), str) {
				return true
			}
		}
		return false
	}
}

const (
	ReplacementText = "[Content removed due to moderation]"
)

var (
	adapterWithConfig = NewUniversalAdapterWithConfig(
		ReplacementText,
		ReplacementText,
		"sensitive",
	)

	adapterWithoutConfig = NewUniversalAdapter()
)

func TestModerator_Process_NonStream(t *testing.T) {
	tests := []struct {
		name               string
		outputText         string
		requestBody        string
		isSpamFunc         func([]rune) bool
		hasReplacement     bool
		wantInputSpam      bool
		wantOutputSpam     bool
		wantOutputReplaced bool
	}{
		{
			name:           "output spam rejected, no replacement",
			outputText:     "you are a piece of {forbidden content}",
			requestBody:    `{"model":"glm-4.7","messages":[{"role":"user","content":"who are you?"}],"stream":false}`,
			isSpamFunc:     strContainsFactory("{forbidden content}"),
			wantOutputSpam: true,
		},
		{
			name:        "clean input and output passes",
			outputText:  "I am a helpful assistant.",
			requestBody: `{"model":"glm-4.7","messages":[{"role":"user","content":"who are you?"}],"stream":false}`,
			isSpamFunc:  strContainsFactory("{forbidden content}"),
		},
		{
			name:          "input spam rejected",
			outputText:    "I am a helpful assistant.",
			requestBody:   `{"model":"glm-4.7","messages":[{"role":"user","content":"{forbidden content}"}],"stream":false}`,
			isSpamFunc:    strContainsFactory("{forbidden content}"),
			wantInputSpam: true,
		},
		{
			name:               "output spam with replacement",
			outputText:         "you are a piece of {forbidden content}",
			requestBody:        `{"model":"glm-4.7","messages":[{"role":"user","content":"who are you?"}],"stream":false}`,
			isSpamFunc:         strContainsFactory("{forbidden content}"),
			hasReplacement:     true,
			wantOutputReplaced: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := adapterWithoutConfig
			if tt.hasReplacement {
				adapter = adapterWithConfig
			}
			moderator := &TextModeratorEngine{
				ModeratorService:     &mockTextModeratorService{isSpamFunc: tt.isSpamFunc, maxRuneLen: 100},
				TextModeratorAdapter: adapter,
				ModerateInput:        true,
				ModerateOutput:       true,
				Next:                 mock.NewWithFixedOutput(tt.outputText, 0, 0),
			}

			req := testhelper.CreateTestRequest(
				testhelper.WithBody(tt.requestBody),
			)
			resp, err := moderator.Process(req)

			switch {
			case tt.wantInputSpam:
				assert.ErrorIs(t, err, ErrInputNotAllowed)
			case tt.wantOutputSpam:
				assert.ErrorIs(t, err, ErrOutputNotAllowed)
			case tt.wantOutputReplaced:
				assert.NoError(t, err)
				assert.NotNil(t, resp.Body)
				isSpam, ok := GetIsSpam(req)
				assert.True(t, ok)
				assert.True(t, isSpam)
				buffer, _ := resp.Body.Bytes()
				assert.Contains(t, string(buffer), ReplacementText)
			default:
				assert.NoError(t, err)
				assert.NotNil(t, resp.Body)
			}
		})
	}
}

func TestModerator_Process_Stream(t *testing.T) {
	tests := []struct {
		name               string
		outputText         string
		requestBody        string
		isSpamFunc         func([]rune) bool
		hasReplacement     bool
		wantInputSpam      bool
		wantOutputSpam     bool
		wantOutputReplaced bool
	}{
		{
			name:        "stream clean output passes all chunks",
			outputText:  "hello",
			requestBody: `{"model":"glm-4.7","messages":[{"role":"user","content":"who are you?"}],"stream":true}`,
			isSpamFunc:  strContainsFactory("{bad}"),
		},
		{
			name:           "stream output spam rejected no replacement",
			outputText:     "{bad}",
			requestBody:    `{"model":"glm-4.7","messages":[{"role":"user","content":"who are you?"}],"stream":true}`,
			isSpamFunc:     strContainsFactory("{bad}"),
			wantOutputSpam: true,
		},
		{
			name:               "stream output spam with replacement",
			outputText:         "{bad}",
			requestBody:        `{"model":"glm-4.7","messages":[{"role":"user","content":"who are you?"}],"stream":true}`,
			isSpamFunc:         strContainsFactory("{bad}"),
			hasReplacement:     true,
			wantOutputReplaced: true,
		},
		{
			name:          "stream input spam rejected",
			outputText:    "I am a helpful assistant.",
			requestBody:   `{"model":"glm-4.7","messages":[{"role":"user","content":"{bad}"}],"stream":true}`,
			isSpamFunc:    strContainsFactory("{bad}"),
			wantInputSpam: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := adapterWithoutConfig
			if tt.hasReplacement {
				adapter = adapterWithConfig
			}

			eng := &TextModeratorEngine{
				ModeratorService:     &mockTextModeratorService{isSpamFunc: tt.isSpamFunc, maxRuneLen: 100},
				TextModeratorAdapter: adapter,
				ModerateInput:        true,
				ModerateOutput:       true,
				Next:                 mock.NewWithFixedOutput(tt.outputText, 0, 0),
			}

			req := testhelper.CreateTestRequest(
				testhelper.WithBody(tt.requestBody),
			)
			resp, err := eng.Process(req)

			if tt.wantInputSpam {
				assert.ErrorIs(t, err, ErrInputNotAllowed)
				return
			}

			assert.NoError(t, err)
			assert.NotNil(t, resp.Stream)

			var chunks []*octollm.StreamChunk
			for chunk := range resp.Stream.Chan() {
				chunks = append(chunks, chunk)
			}

			isSpam, _ := GetIsSpam(req)

			switch {
			case tt.wantOutputReplaced:
				assert.True(t, isSpam)
				assert.Len(t, chunks, 1)
				text, err := adapter.ExtractTextFromBody(context.Background(), chunks[0].Body)
				assert.NoError(t, err)
				assert.Contains(t, string(text), ReplacementText)
			case tt.wantOutputSpam:
				assert.True(t, isSpam)
				assert.Empty(t, chunks)
			default:
				assert.False(t, isSpam)
				assert.NotEmpty(t, chunks)
			}
		})
	}
}
