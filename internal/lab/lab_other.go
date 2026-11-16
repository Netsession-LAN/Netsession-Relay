//go:build !linux

package lab

import (
	"errors"

	"github.com/Netsession2024/Netsession-Relay/internal/pkt"
)

var errNotLinux = errors.New("lab: send/expect only work on Linux")

// Send is only available on Linux.
func Send(SendOptions) error { return errNotLinux }

// Expect is only available on Linux.
func Expect(ExpectOptions) ([]pkt.Frame, error) { return nil, errNotLinux }
