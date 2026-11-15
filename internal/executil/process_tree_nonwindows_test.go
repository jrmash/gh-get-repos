//go:build !windows

package executil

import (
	"errors"
	"io"
)

func processConnectionClosed(err error) bool {
	return errors.Is(err, io.EOF)
}
