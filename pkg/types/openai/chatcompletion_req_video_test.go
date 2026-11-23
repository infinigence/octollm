package openai

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageContentItem_VideoURL(t *testing.T) {
	testCases := []struct {
		Name         string
		JSON         string
		ExpectedURL  string
		ExpectedText string
		ExpectNil    bool
	}{
		{
			Name:         "object form",
			JSON:         `{"role":"user","content":[{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}}]}`,
			ExpectedURL:  "https://example.com/a.mp4",
			ExpectedText: "[video:https://example.com/a.mp4]",
		},
		{
			Name:         "string form",
			JSON:         `{"role":"user","content":[{"type":"video_url","video_url":"https://example.com/b.mov"}]}`,
			ExpectedURL:  "https://example.com/b.mov",
			ExpectedText: "[video:https://example.com/b.mov]",
		},
		{
			Name:         "text and video",
			JSON:         `{"role":"user","content":[{"type":"text","text":"describe "},{"type":"video_url","video_url":{"url":"https://example.com/c.mkv"}}]}`,
			ExpectedURL:  "https://example.com/c.mkv",
			ExpectedText: "describe [video:https://example.com/c.mkv]",
		},
		{
			Name:         "null video_url",
			JSON:         `{"role":"user","content":[{"type":"video_url","video_url":null}]}`,
			ExpectedText: "",
			ExpectNil:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			var msg Message
			require.NoError(t, json.Unmarshal([]byte(tc.JSON), &msg))

			arr, ok := msg.Content.(MessageContentArray)
			require.True(t, ok)
			item := arr[len(arr)-1]
			if tc.ExpectNil {
				assert.Nil(t, item.VideoURL)
			} else {
				require.NotNil(t, item.VideoURL)
				assert.Equal(t, tc.ExpectedURL, item.VideoURL.GetVideoUrl())
			}
			assert.Equal(t, tc.ExpectedText, msg.Content.ExtractText())
		})
	}
}

func TestMessageContentItem_VideoURL_MarshalRoundTrip(t *testing.T) {
	objForm := `{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}}`
	strForm := `{"type":"video_url","video_url":"https://example.com/a.mp4"}`

	for _, in := range []string{objForm, strForm} {
		var item MessageContentItem
		require.NoError(t, json.Unmarshal([]byte(in), &item))
		out, err := json.Marshal(item)
		require.NoError(t, err)
		assert.JSONEq(t, in, string(out))
	}
}
