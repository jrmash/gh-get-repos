package executil

import (
	"errors"
	"io"
	"syscall"
)

func processConnectionClosed(err error) bool {
	// TerminateJobObject can abort sockets instead of closing them gracefully.
	return errors.Is(err, io.EOF) || errors.Is(err, syscall.WSAECONNRESET)
}
