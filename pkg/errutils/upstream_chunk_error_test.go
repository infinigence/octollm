package errutils

import (
	"strings"
	"testing"
)

func TestUpstreamChunkError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *UpstreamChunkError
		want string
	}{
		{
			name: "first chunk",
			err:  &UpstreamChunkError{ChunkBody: []byte(`{"error":{"message":"RPM limit exceeded"}}`)},
			want: `upstream chunk error at chunk 0: {"error":{"message":"RPM limit exceeded"}}`,
		},
		{
			name: "mid-stream chunk",
			err:  &UpstreamChunkError{ChunkBody: []byte(`{"error":{"message":"overloaded"}}`), ChunkIndex: 3},
			want: `upstream chunk error at chunk 3: {"error":{"message":"overloaded"}}`,
		},
		{
			name: "long body is truncated",
			err:  &UpstreamChunkError{ChunkBody: []byte(strings.Repeat("x", 2000)), ChunkIndex: 1},
			want: "upstream chunk error at chunk 1: " + strings.Repeat("x", 1024) + " (truncated)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}
