package image_url_fetch

import (
	"encoding/json"
	"fmt"
	"strings"
)

var _ imageReplaceJob = (*openaiImageReplaceJob)(nil)
var _ imageReplaceJob = (*claudeImageReplaceJob)(nil)

// imageReplaceJob describes one protocol-specific image replacement in the raw JSON body.
type imageReplaceJob interface {
	remoteURL() string
	inlineReplacement(mediaType, rawBase64 string) (path []string, encoded []byte, err error)
	normalizeExistingURL() (path []string, encoded []byte, ok bool, err error)
}

// openaiImageReplaceJob is one OpenAI Chat Completions remote image_url to inline (full path in request JSON).
type openaiImageReplaceJob struct {
	msgIndex     int
	field        string // "content" or "reasoning_content"
	partIndex    int
	url          string
	isObjectForm bool // true when JSON had "image_url": {"url":"..."}; false when "image_url": "https://...".
}

func (j *openaiImageReplaceJob) jsonParserPath() []string {
	if j == nil {
		return nil
	}
	p := []string{
		"messages",
		fmt.Sprintf("[%d]", j.msgIndex),
		j.field,
		fmt.Sprintf("[%d]", j.partIndex),
	}
	if j.isObjectForm {
		return append(p, "image_url", "url")
	}
	return append(p, "image_url")
}

func (j *openaiImageReplaceJob) remoteURL() string {
	if j == nil {
		return ""
	}
	return strings.TrimSpace(j.url)
}

func (j *openaiImageReplaceJob) inlineReplacement(mediaType, rawBase64 string) ([]string, []byte, error) {
	if j == nil {
		return nil, nil, fmt.Errorf("image replace job is nil")
	}
	dataURL := fmt.Sprintf("data:%s;base64,%s", mediaType, rawBase64)
	if j.isObjectForm {
		encoded, err := json.Marshal(dataURL)
		return j.jsonParserPath(), encoded, err
	}

	encoded, err := json.Marshal(map[string]string{"url": dataURL})
	return j.jsonParserPath(), encoded, err
}

func (j *openaiImageReplaceJob) normalizeExistingURL() ([]string, []byte, bool, error) {
	if j == nil {
		return nil, nil, false, fmt.Errorf("image replace job is nil")
	}
	if j.isObjectForm {
		return nil, nil, false, nil
	}
	u := j.remoteURL()
	if u == "" {
		return nil, nil, false, nil
	}

	encoded, err := json.Marshal(map[string]string{"url": u})
	return j.jsonParserPath(), encoded, true, err
}

// claudeImageReplaceJob is one Anthropic Messages remote image URL to inline (full path in request JSON).
// After fetch, the entire "source" object is replaced per Messages API: type base64, media_type, data.
// contentIndices walks nested content arrays: first index is into messages[MsgIndex].content, each further
// index is into a tool_result.content array (messages[MsgIndex].content[i].content[j]...).
type claudeImageReplaceJob struct {
	msgIndex       int
	contentIndices []int
	url            string
}

// jsonParserPathToSource returns buger/jsonparser keys through the image block's "source" key (value replaced in full).
func (j *claudeImageReplaceJob) jsonParserPathToSource() []string {
	if j == nil {
		return nil
	}
	p := []string{"messages", fmt.Sprintf("[%d]", j.msgIndex)}
	for _, idx := range j.contentIndices {
		p = append(p, "content", fmt.Sprintf("[%d]", idx))
	}
	p = append(p, "source")
	return p
}

func (j *claudeImageReplaceJob) remoteURL() string {
	if j == nil {
		return ""
	}
	return strings.TrimSpace(j.url)
}

func (j *claudeImageReplaceJob) inlineReplacement(mediaType, rawBase64 string) ([]string, []byte, error) {
	if j == nil {
		return nil, nil, fmt.Errorf("image replace job is nil")
	}
	// https://docs.anthropic.com/en/api/messages — image with base64 source
	src := map[string]string{
		"type":       "base64",
		"media_type": mediaType,
		"data":       rawBase64,
	}
	encoded, err := json.Marshal(src)
	return j.jsonParserPathToSource(), encoded, err
}

func (j *claudeImageReplaceJob) normalizeExistingURL() ([]string, []byte, bool, error) {
	if j == nil {
		return nil, nil, false, fmt.Errorf("image replace job is nil")
	}
	return nil, nil, false, nil
}
