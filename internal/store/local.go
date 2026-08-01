// Package store owns the local PDU Session context store.
package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// ErrSessionNotFound indicates that a context ID is absent from this PDU node.
var ErrSessionNotFound = errors.New("session context not found")

type idGenerator func() (string, error)

// CreateParams contains the data needed to persist a new local context.
type CreateParams struct {
	Request   model.CreateSMContextRequest
	HandledBy string
	Status    constants.SMContextStatus
}

// Session is an immutable value copied into and out of a LocalStore.
type Session struct {
	ContextID string
	Request   model.CreateSMContextRequest
	HandledBy string
	Status    constants.SMContextStatus
	CreatedAt time.Time
}

// LocalStore keeps contexts in one process. Its state is intentionally neither
// shared with other PDU replicas nor persisted across process restarts.
type LocalStore struct {
	mu       sync.RWMutex
	sessions map[string]Session
	newID    idGenerator
}

// NewLocal creates an empty per-process session store.
func NewLocal() *LocalStore {
	return newLocal(newUUIDV4)
}

func newLocal(generator idGenerator) *LocalStore {
	return &LocalStore{
		sessions: make(map[string]Session),
		newID:    generator,
	}
}

// Create assigns a UUID v4 and stores a copy of the supplied session data.
func (store *LocalStore) Create(ctx context.Context, params CreateParams) (Session, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Session{}, err
		}

		contextID, err := store.newID()
		if err != nil {
			return Session{}, fmt.Errorf("generate session context ID: %w", err)
		}
		session := Session{
			ContextID: contextID,
			Request:   params.Request,
			HandledBy: params.HandledBy,
			Status:    params.Status,
			CreatedAt: time.Now().UTC(),
		}

		store.mu.Lock()
		if err := ctx.Err(); err != nil {
			store.mu.Unlock()
			return Session{}, err
		}
		if _, exists := store.sessions[contextID]; exists {
			store.mu.Unlock()
			continue
		}
		store.sessions[contextID] = session
		store.mu.Unlock()
		return session, nil
	}
}

// Read returns a copy of one context from this PDU node.
func (store *LocalStore) Read(ctx context.Context, contextID string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}

	store.mu.RLock()
	session, found := store.sessions[contextID]
	store.mu.RUnlock()
	if !found {
		return Session{}, fmt.Errorf("%w: %s", ErrSessionNotFound, contextID)
	}
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	return session, nil
}
