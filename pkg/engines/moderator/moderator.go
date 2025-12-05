package moderator

import (
	"context"
	"errors"
	"fmt"

	"github.com/infinigence/octollm/pkg/octollm"
	"github.com/sirupsen/logrus"
)

var (
	ErrInputNotAllowed        = errors.New("input not allowed")
	ErrOutputNotAllowed       = errors.New("output not allowed")
	ErrModeratorInternalError = errors.New("moderator internal error")
)

type TextModeratorService interface {
	Allow(ctx context.Context, text []rune) error
	MaxRuneLen() int
}

type TextModeratorAdapter interface {
	ExtractTextFromBody(ctx context.Context, body *octollm.UnifiedBody) ([]rune, error)
	GetReplacementBody(ctx context.Context, body *octollm.UnifiedBody) *octollm.UnifiedBody
}

type TextModeratorEngine struct {
	ModeratorService     TextModeratorService
	TextModeratorAdapter TextModeratorAdapter

	ModerateInput       bool
	ModerateOutput      bool
	ModerateStreamEvery int

	Next octollm.Engine
}

var _ octollm.Engine = (*TextModeratorEngine)(nil)

func (e *TextModeratorEngine) Process(req *octollm.Request) (*octollm.Response, error) {
	maxRuneLen := e.ModeratorService.MaxRuneLen()

	if e.ModerateInput {
		text, err := e.TextModeratorAdapter.ExtractTextFromBody(req.Context(), req.Body)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrModeratorInternalError, err)
		}
		if len(text) > maxRuneLen {
			// truncate text to last max rune len
			text = text[len(text)-maxRuneLen:]
		}
		if err := e.ModeratorService.Allow(req.Context(), text); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInputNotAllowed, err)
		}
	}

	resp, err := e.Next.Process(req)
	if err != nil {
		return nil, err
	}
	if !e.ModerateOutput {
		return resp, nil
	}

	// output moderation
	if resp.Body != nil {
		// non-stream response
		text, err := e.TextModeratorAdapter.ExtractTextFromBody(req.Context(), resp.Body)
		if err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("%w: %w", ErrModeratorInternalError, err)
		}
		if len(text) > maxRuneLen {
			// truncate text to last max rune len
			text = text[len(text)-maxRuneLen:]
		}
		if err := e.ModeratorService.Allow(req.Context(), text); err != nil {
			replacement := e.TextModeratorAdapter.GetReplacementBody(req.Context(), resp.Body)
			resp.Body.Close()
			if replacement != nil {
				resp.Body = replacement
				return resp, nil
			}
			return nil, fmt.Errorf("%w: %w", ErrOutputNotAllowed, err)
		}
		return resp, nil
	}

	// stream response
	newChunks := make(chan *octollm.StreamChunk)
	originalChunks := resp.Stream
	ctx, cancel := context.WithCancel(req.Context())
	go func() {
		defer close(newChunks)

		moderateEvery := e.ModerateStreamEvery
		if moderateEvery <= 0 {
			moderateEvery = 10
		}
		var moderationFailedErr error
		textBuffer := make([]rune, 0)
		chunkBuffer := make([]*octollm.StreamChunk, 0, moderateEvery)
		chunkCountSinceLast := 0

		logrus.WithContext(ctx).Debugf("[moderate] begin reading upstream stream")
		for chunk := range originalChunks.Chan() {
			logrus.WithContext(ctx).Debugf("[moderate] stream chunk")
			text, err := e.TextModeratorAdapter.ExtractTextFromBody(ctx, chunk.Body)
			if err != nil {
				logrus.WithContext(ctx).Debugf("extract text from stream chunk error: %s", err)
				moderationFailedErr = fmt.Errorf("%w: %w", ErrModeratorInternalError, err)
				break
			}
			logrus.WithContext(ctx).Debugf("[moderate] extract text from stream chunk: %s", string(text))
			textBuffer = append(textBuffer, text...)
			if len(textBuffer) > maxRuneLen {
				// truncate text to last max rune len
				textBuffer = textBuffer[len(textBuffer)-maxRuneLen:]
			}
			chunkBuffer = append(chunkBuffer, chunk)
			chunkCountSinceLast++
			if chunkCountSinceLast >= moderateEvery {
				if err := e.ModeratorService.Allow(ctx, textBuffer); err != nil {
					logrus.WithContext(ctx).Debugf("moderate stream chunk error: %s", err)
					moderationFailedErr = fmt.Errorf("%w: %w", ErrOutputNotAllowed, err)
					break
				}
				chunkCountSinceLast = 0
				for _, chunk := range chunkBuffer {
					select {
					case newChunks <- chunk:
					case <-ctx.Done():
						return
					}
				}
				chunkBuffer = make([]*octollm.StreamChunk, 0, moderateEvery)
			}
		}
		if moderationFailedErr != nil {
			// get replacement chunk
			originalChunks.Close()
			replacement := e.TextModeratorAdapter.GetReplacementBody(req.Context(), chunkBuffer[0].Body)
			if replacement != nil {
				select {
				case newChunks <- &octollm.StreamChunk{Body: replacement}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	resp.Stream = octollm.NewStreamChan(newChunks, func() {
		originalChunks.Close()
		cancel()
	})
	return resp, nil
}
