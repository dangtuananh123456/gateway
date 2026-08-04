package store

import (
	"context"
	"errors"
	"testing"
)

// A PDU process owns exactly one LocalStore. Constructing a fresh store models
// a process/container restart because no persistence dependency is configured.
func TestLocalStoreRestartStartsWithoutPreviousSessions(t *testing.T) {
	beforeRestart := NewLocal()
	created, err := beforeRestart.Create(context.Background(), testCreateParams())
	if err != nil {
		t.Fatalf("Create() before restart error = %v", err)
	}
	if _, err := beforeRestart.Read(context.Background(), created.ContextID); err != nil {
		t.Fatalf("Read() before restart error = %v", err)
	}

	afterRestart := NewLocal()
	if _, err := afterRestart.Read(context.Background(), created.ContextID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Read(%q) after restart error = %v, want ErrSessionNotFound", created.ContextID, err)
	}
}
