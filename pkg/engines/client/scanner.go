package client

import (
	"bufio"
	"io"
)

var scannerMaxTokenSize = bufio.MaxScanTokenSize

// SetScannerMaxTokenSize sets the maximum token size for SSE stream scanners created after this call.
// Non-positive values are ignored.
func SetScannerMaxTokenSize(size int) {
	if size <= 0 {
		return
	}
	scannerMaxTokenSize = size
}

// ScannerMaxTokenSize returns the package-level maximum scan token size for SSE scanners.
func ScannerMaxTokenSize() int {
	return scannerMaxTokenSize
}

func newScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 4096)
	scanner.Buffer(buf, scannerMaxTokenSize)
	return scanner
}
