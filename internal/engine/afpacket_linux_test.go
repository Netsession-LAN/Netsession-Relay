//go:build linux

package engine

import (
	"errors"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// pipeSock wraps the write end of a pipe as a packetSock: write and close do not care what kind of descriptor
// it is, and a pipe needs no privileges.
func pipeSock(t *testing.T) (s *packetSock, readEnd int) {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(p[0]) })
	return &packetSock{fd: p[1], name: "test"}, p[0]
}

// A socket is closed by an apply while receivers that still hold the old state may send to it. After close the
// kernel gives the descriptor number to whatever is opened next, so a late write must fail instead of landing
// there.
func TestPacketSockRefusesWritesAfterClose(t *testing.T) {
	s, r := pipeSock(t)
	if err := s.write([]byte("frame")); err != nil {
		t.Fatalf("write before close: %v", err)
	}
	buf := make([]byte, 16)
	if n, err := unix.Read(r, buf); err != nil || string(buf[:n]) != "frame" {
		t.Fatalf("frame did not arrive: %q, %v", buf[:n], err)
	}
	fd := s.fd
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	// the next descriptor that is opened gets the number that was just freed
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	if p[0] != fd && p[1] != fd {
		t.Logf("descriptor %d was not reused (got %d and %d); the write below must fail all the same", fd, p[0], p[1])
	}
	if err := s.write([]byte("late")); !errors.Is(err, errSockClosed) {
		t.Fatalf("write after close: %v, want errSockClosed", err)
	}
	if n, err := unix.Read(p[0], buf); err != unix.EAGAIN {
		t.Errorf("the late write reached the descriptor that reused the number: %q, %v", buf[:max(n, 0)], err)
	}
	if err := s.close(); err != nil {
		t.Errorf("second close: %v (it must not close the reused descriptor)", err)
	}
	if _, err := unix.Write(p[1], []byte("x")); err != nil {
		t.Errorf("the reused descriptor was closed by the second close: %v", err)
	}
}

// Writers and a close at the same time: every write either goes out or fails with errSockClosed (run with -race).
func TestPacketSockCloseWhileWriting(t *testing.T) {
	s, r := pipeSock(t)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	drained := make(chan struct{})
	go func() { // keep the pipe empty so the writers do not run into EAGAIN
		defer close(drained)
		buf := make([]byte, 4096)
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = unix.Read(r, buf)
			}
		}
	}()
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2000 {
				if err := s.write([]byte("frame")); err != nil && !errors.Is(err, errSockClosed) && !errors.Is(err, unix.EAGAIN) {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	if err := s.close(); err != nil {
		t.Error(err)
	}
	wg.Wait()
	close(stop)
	<-drained
	if err := s.write([]byte("late")); !errors.Is(err, errSockClosed) {
		t.Errorf("write after close: %v", err)
	}
}
