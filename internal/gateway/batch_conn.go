package gateway

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const (
	writeBatchBytes        = 32 << 10
	writeBatchDelay        = 250 * time.Microsecond
	writeBatchCloseTimeout = 10 * time.Millisecond
)

// batchConn coalesces the small HEADERS/DATA writes made by HTTP/2 streams.
// Its buffer is bounded; a short timer flushes partial batches even at low load.
// Closing drains accepted writes with a short deadline and stops the timer.
type batchConn struct {
	net.Conn
	mu            sync.Mutex
	buffer        []byte
	timer         *time.Timer
	err           error
	writeDeadline time.Time
}

func newBatchConn(conn net.Conn) *batchConn {
	return &batchConn{Conn: conn, buffer: make([]byte, 0, writeBatchBytes)}
}

type batchListener struct{ net.Listener }

func (listener batchListener) Accept() (net.Conn, error) {
	conn, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return newBatchConn(conn), nil
}

func (conn *batchConn) Write(data []byte) (int, error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.err != nil {
		return 0, conn.err
	}
	if !conn.writeDeadline.IsZero() && !time.Now().Before(conn.writeDeadline) {
		return 0, os.ErrDeadlineExceeded
	}
	written := 0
	for len(data) > 0 {
		n := min(len(data), cap(conn.buffer)-len(conn.buffer))
		conn.buffer = append(conn.buffer, data[:n]...)
		data = data[n:]
		written += n
		if len(conn.buffer) == cap(conn.buffer) {
			if err := conn.flushLocked(); err != nil {
				return written, err
			}
		}
	}
	if len(conn.buffer) > 0 && conn.timer == nil {
		conn.timer = time.AfterFunc(writeBatchDelay, func() {
			conn.mu.Lock()
			defer conn.mu.Unlock()
			conn.timer = nil
			if conn.err == nil {
				_ = conn.flushLocked()
			}
		})
	}
	return written, nil
}

func (conn *batchConn) SetWriteDeadline(deadline time.Time) error {
	// Set the socket deadline first to unblock a flush that may hold mu.
	if err := conn.Conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	conn.mu.Lock()
	conn.writeDeadline = deadline
	conn.mu.Unlock()
	return nil
}

func (conn *batchConn) SetDeadline(deadline time.Time) error {
	if err := conn.Conn.SetDeadline(deadline); err != nil {
		return err
	}
	conn.mu.Lock()
	conn.writeDeadline = deadline
	conn.mu.Unlock()
	return nil
}

func (conn *batchConn) flushLocked() error {
	pending := conn.buffer
	for len(pending) > 0 {
		n, err := conn.Conn.Write(pending)
		pending = pending[n:]
		if n == 0 && err == nil {
			err = io.ErrNoProgress
		}
		if err != nil {
			conn.err = err
			// Wake the transport's reader so all pending streams see the error.
			_ = conn.Conn.Close()
			return err
		}
	}
	conn.buffer = conn.buffer[:0]
	return nil
}

func (conn *batchConn) Close() error {
	// Preserve accepted response bytes during graceful/idle shutdown. Setting
	// a deadline first also unblocks a timer that holds mu in a network write.
	if err := conn.Conn.SetWriteDeadline(time.Now().Add(writeBatchCloseTimeout)); err != nil {
		_ = conn.Conn.Close()
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.timer != nil {
		conn.timer.Stop()
	}
	var flushErr error
	if conn.err == nil {
		flushErr = conn.flushLocked()
	}
	err := conn.Conn.Close()
	conn.err = net.ErrClosed
	conn.buffer = nil
	if flushErr != nil {
		return flushErr
	}
	return err
}
