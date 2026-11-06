package streammerge

import (
	"strings"
	"testing"
)

func TestBadChunkError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *BadChunkError
		want string
	}{
		{
			name: "chunk body",
			err:  &BadChunkError{ChunkBody: []byte(`{"error":{"message":"RPM limit exceeded"}}`)},
			want: `upstream chunk error: {"error":{"message":"RPM limit exceeded"}}`,
		},
		{
			name: "long body is truncated",
			err:  &BadChunkError{ChunkBody: []byte(strings.Repeat("x", 2000))},
			want: "upstream chunk error: " + strings.Repeat("x", 1024) + " (truncated)",
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
