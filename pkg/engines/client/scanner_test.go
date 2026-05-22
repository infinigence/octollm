package client

import (
	"bufio"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetScannerMaxTokenSize_AppliesToNewScanners(t *testing.T) {
	old := ScannerMaxTokenSize()
	t.Cleanup(func() { SetScannerMaxTokenSize(old) })

	longLine := strings.Repeat("a", bufio.MaxScanTokenSize+1)

	SetScannerMaxTokenSize(bufio.MaxScanTokenSize)
	scanner := newScanner(strings.NewReader(longLine + "\n"))
	require.False(t, scanner.Scan())
	assert.ErrorContains(t, scanner.Err(), "token too long")

	SetScannerMaxTokenSize(200 * 1024)
	scanner = newScanner(strings.NewReader(longLine + "\n"))
	require.True(t, scanner.Scan())
	assert.Equal(t, longLine, string(scanner.Bytes()))
	assert.NoError(t, scanner.Err())
}

func TestNewScanner_ErrorsWhenTokenExceedsSmallLimit(t *testing.T) {
	old := ScannerMaxTokenSize()
	t.Cleanup(func() { SetScannerMaxTokenSize(old) })

	const limit = 4 * 1024
	SetScannerMaxTokenSize(limit)

	// One byte over limit; fills the 4096-byte initial buffer and triggers ErrTooLong on grow.
	line := strings.Repeat("x", limit+1)
	scanner := newScanner(strings.NewReader(line + "\n"))
	require.False(t, scanner.Scan())
	assert.ErrorContains(t, scanner.Err(), "token too long")
}

func TestSetScannerMaxTokenSize_IgnoresNonPositive(t *testing.T) {
	old := ScannerMaxTokenSize()
	t.Cleanup(func() { SetScannerMaxTokenSize(old) })

	SetScannerMaxTokenSize(128 * 1024)
	SetScannerMaxTokenSize(0)
	assert.Equal(t, 128*1024, ScannerMaxTokenSize())
	SetScannerMaxTokenSize(-1)
	assert.Equal(t, 128*1024, ScannerMaxTokenSize())
}
