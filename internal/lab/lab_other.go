//go:build !linux

package lab

import (
	"errors"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

var errNotLinux = errors.New("lab: send/expect only work on Linux")

// Send is only available on Linux.
func Send(SendOptions) error { return errNotLinux }

// SendRaw is only available on Linux.
func SendRaw(string, int, int, func(pkt.MAC) []byte) error { return errNotLinux }

// A2SServer is only available on Linux.
func A2SServer(A2SOptions) (int, error) { return 0, errNotLinux }

// Expect is only available on Linux.
func Expect(ExpectOptions) ([]pkt.Frame, error) { return nil, errNotLinux }
