package gateway

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestBatchConnFlushesSmallWriteAndCopiesCallerBuffer(t *testing.T) {
	local, remote := net.Pipe()
	conn := newBatchConn(local)
	t.Cleanup(func() { conn.Close(); remote.Close() })
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	payload := []byte("headers and body")
	want := bytes.Clone(payload)
	if n, err := conn.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	clear(payload)
	got := make([]byte, len(want))
	if _, err := io.ReadFull(remote, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBatchConnCloseDrainsAcceptedWrites(t *testing.T) {
	local, remote := net.Pipe()
	conn := newBatchConn(local)
	t.Cleanup(func() { conn.Close(); remote.Close() })
	want := []byte("last response before shutdown")
	if _, err := conn.Write(want); err != nil {
		t.Fatal(err)
	}
	done := make(chan []byte, 1)
	go func() {
		body, _ := io.ReadAll(remote)
		done <- body
	}()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got := <-done; !bytes.Equal(got, want) {
		t.Fatalf("Close lost accepted data: %q", got)
	}
}

func TestBatchConnPreservesLargeWritesAndReusesBoundedBuffer(t *testing.T) {
	local, remote := net.Pipe()
	conn := newBatchConn(local)
	t.Cleanup(func() { conn.Close(); remote.Close() })
	_ = remote.SetReadDeadline(time.Now().Add(time.Second))
	want := bytes.Repeat([]byte("0123456789"), writeBatchBytes)
	done := make(chan error, 1)
	go func() {
		_, err := conn.Write(want)
		if err == nil {
			_, err = conn.Write([]byte("tail"))
		}
		done <- err
	}()
	got := make([]byte, len(want)+4)
	if _, err := io.ReadFull(remote, got); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:len(want)], want) || string(got[len(want):]) != "tail" {
		t.Fatal("buffered writes corrupted or reordered data")
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if cap(conn.buffer) != writeBatchBytes {
		t.Fatalf("buffer capacity = %d", cap(conn.buffer))
	}
}

func TestBatchConnCloseInterruptsBlockedFlush(t *testing.T) {
	local, remote := net.Pipe()
	started := make(chan struct{})
	conn := newBatchConn(&notifyingConn{Conn: local, started: started})
	t.Cleanup(func() { conn.Close(); remote.Close() })
	writeDone := make(chan error, 1)
	go func() {
		_, err := conn.Write(make([]byte, writeBatchBytes))
		writeDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("flush did not start")
	}
	done := make(chan error, 1)
	go func() { done <- conn.Close() }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind a pending network write")
	}
	if err := <-writeDone; err == nil {
		t.Fatal("blocked writer did not observe connection closure")
	}
	if _, err := conn.Write([]byte("after close")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Write after Close = %v", err)
	}
}

type notifyingConn struct {
	net.Conn
	started chan struct{}
}

func (conn *notifyingConn) Write(data []byte) (int, error) {
	close(conn.started)
	return conn.Conn.Write(data)
}

func TestBatchConnRejectsExpiredWriteDeadline(t *testing.T) {
	for _, wholeConnection := range []bool{false, true} {
		local, remote := net.Pipe()
		conn := newBatchConn(local)
		deadline := time.Now().Add(-time.Second)
		if wholeConnection {
			_ = conn.SetDeadline(deadline)
		} else {
			_ = conn.SetWriteDeadline(deadline)
		}
		if n, err := conn.Write([]byte("late")); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Errorf("expired deadline: Write = %d, %v", n, err)
		}
		conn.Close()
		remote.Close()
	}
}

func TestBatchConnReportsFlushFailureToReaderAndNextWriter(t *testing.T) {
	local, remote := net.Pipe()
	conn := newBatchConn(local)
	t.Cleanup(func() { conn.Close(); remote.Close() })
	_ = conn.SetWriteDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := conn.Write([]byte("unread")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var data [1]byte
		_, err := conn.Read(data[:])
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("reader did not observe failed connection")
		}
	case <-time.After(time.Second):
		t.Fatal("flush failure did not unblock the transport reader")
	}
	if _, err := conn.Write([]byte("next")); err == nil {
		t.Fatal("next writer did not observe the flush failure")
	}
}
