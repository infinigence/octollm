package moderator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/infinigence/octollm/pkg/octollm"
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

type moderatorMetadataKey string

const (
	// isSpamKey is the metadata key used to mark a request whose output was blocked by the moderator. Value type: bool.
	isSpamKey moderatorMetadataKey = "is_spam"
)

func GetIsSpam(req *octollm.Request) (bool, bool) {
	if req == nil {
		return false, false
	}
	value, ok := req.GetMetadataValue(isSpamKey)
	if !ok {
		return false, false
	}
	isSpam, ok := value.(bool)
	return isSpam, ok
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
			req.SetMetadataValue(isSpamKey, true)
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

		slog.DebugContext(ctx, fmt.Sprintf("[moderate] begin reading upstream stream"))
		for chunk := range originalChunks.Chan() {
			// slog.DebugContext(ctx, fmt.Sprintf("[moderate] stream chunk"))
			text, err := e.TextModeratorAdapter.ExtractTextFromBody(ctx, chunk.Body)
			if err != nil {
				// stream done 是正常结束信号，不应该视为错误
				if errors.Is(err, octollm.ErrStreamDone) {
					chunkBuffer = append(chunkBuffer, chunk)
					slog.DebugContext(ctx, fmt.Sprintf("stream done, will process remaining chunks"))
					break
				}
				slog.DebugContext(ctx, fmt.Sprintf("extract text from stream chunk error: %s", err))
				moderationFailedErr = fmt.Errorf("%w: %w", ErrModeratorInternalError, err)
				break
			}
			// slog.DebugContext(ctx, fmt.Sprintf("[moderate] extract text from stream chunk: %s", string(text)))
			textBuffer = append(textBuffer, text...)
			if len(textBuffer) > maxRuneLen {
				// truncate text to last max rune len
				textBuffer = textBuffer[len(textBuffer)-maxRuneLen:]
			}
			chunkBuffer = append(chunkBuffer, chunk)
			chunkCountSinceLast++
			if chunkCountSinceLast >= moderateEvery {
				if err := e.ModeratorService.Allow(ctx, textBuffer); err != nil {
					slog.DebugContext(ctx, fmt.Sprintf("moderate stream chunk error: %s", err))
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

		// Handle remaining chunks after stream ends
		if moderationFailedErr == nil && len(chunkBuffer) > 0 {
			slog.DebugContext(ctx, fmt.Sprintf("[moderate] processing %d remaining chunks", len(chunkBuffer)))
			if err := e.ModeratorService.Allow(ctx, textBuffer); err != nil {
				slog.DebugContext(ctx, fmt.Sprintf("moderate remaining stream chunks error: %s", err))
				moderationFailedErr = fmt.Errorf("%w: %w", ErrOutputNotAllowed, err)
			} else {
				// Send remaining chunks if moderation passed
				for _, chunk := range chunkBuffer {
					select {
					case newChunks <- chunk:
					case <-ctx.Done():
						return
					}
				}
			}
		}

		if moderationFailedErr != nil {
			// get replacement chunk
			req.SetMetadataValue(isSpamKey, true)

			originalChunks.Close()
			if len(chunkBuffer) > 0 {
				replacement := e.TextModeratorAdapter.GetReplacementBody(req.Context(), chunkBuffer[0].Body)
				if replacement != nil {
					select {
					case newChunks <- &octollm.StreamChunk{Body: replacement}:
					case <-ctx.Done():
						return
					}
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
