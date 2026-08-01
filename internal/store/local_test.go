package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestLocalStoreCreateAndRead(t *testing.T) {
	local := NewLocal()
	params := testCreateParams()

	created, err := local.Create(context.Background(), params)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !validUUIDV4(created.ContextID) {
		t.Errorf("context ID = %q, want UUID v4", created.ContextID)
	}
	if created.CreatedAt.IsZero() {
		t.Error("CreatedAt must be populated")
	}

	read, err := local.Read(context.Background(), created.ContextID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if read != created {
		t.Errorf("Read() = %+v, want %+v", read, created)
	}
}

func TestLocalStoresAreIsolated(t *testing.T) {
	first := NewLocal()
	second := NewLocal()

	created, err := first.Create(context.Background(), testCreateParams())
	if err != nil {
		t.Fatalf("first.Create() error = %v", err)
	}
	if _, err := second.Read(context.Background(), created.ContextID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("second.Read() error = %v, want ErrSessionNotFound", err)
	}
}

func TestLocalStoreReadMissing(t *testing.T) {
	_, err := NewLocal().Read(context.Background(), "missing")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Read() error = %v, want ErrSessionNotFound", err)
	}
}

func TestLocalStoreHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	local := NewLocal()

	if _, err := local.Create(ctx, testCreateParams()); !errors.Is(err, context.Canceled) {
		t.Errorf("Create() error = %v, want context.Canceled", err)
	}
	if _, err := local.Read(ctx, "missing"); !errors.Is(err, context.Canceled) {
		t.Errorf("Read() error = %v, want context.Canceled", err)
	}
}

func TestLocalStoreReturnsIDGeneratorError(t *testing.T) {
	wantErr := errors.New("random source failed")
	local := newLocal(func() (string, error) { return "", wantErr })

	_, err := local.Create(context.Background(), testCreateParams())
	if !errors.Is(err, wantErr) {
		t.Fatalf("Create() error = %v, want wrapped generator error", err)
	}
}

func TestLocalStoreRetriesIDCollision(t *testing.T) {
	IDs := []string{
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
	}
	var index int
	local := newLocal(func() (string, error) {
		ID := IDs[index]
		index++
		return ID, nil
	})

	first, err := local.Create(context.Background(), testCreateParams())
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	second, err := local.Create(context.Background(), testCreateParams())
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	if first.ContextID == second.ContextID || index != len(IDs) {
		t.Errorf("IDs = %q and %q, generator calls = %d", first.ContextID, second.ContextID, index)
	}
}

func TestLocalStoreConcurrentCreateAndRead(t *testing.T) {
	local := NewLocal()
	const sessionCount = 2000
	IDs := make(chan string, sessionCount)
	errorsFound := make(chan error, sessionCount)

	var workers sync.WaitGroup
	workers.Add(sessionCount)
	for range sessionCount {
		go func() {
			defer workers.Done()
			created, err := local.Create(context.Background(), testCreateParams())
			if err != nil {
				errorsFound <- fmt.Errorf("create: %w", err)
				return
			}
			read, err := local.Read(context.Background(), created.ContextID)
			if err != nil {
				errorsFound <- fmt.Errorf("read: %w", err)
				return
			}
			if read != created {
				errorsFound <- fmt.Errorf("read context %s differs from created value", created.ContextID)
				return
			}
			IDs <- created.ContextID
		}()
	}
	workers.Wait()
	close(IDs)
	close(errorsFound)

	for err := range errorsFound {
		t.Error(err)
	}
	unique := make(map[string]struct{}, sessionCount)
	for ID := range IDs {
		if !validUUIDV4(ID) {
			t.Errorf("context ID = %q, want UUID v4", ID)
		}
		if _, duplicate := unique[ID]; duplicate {
			t.Errorf("duplicate context ID = %q", ID)
		}
		unique[ID] = struct{}{}
	}
	if len(unique) != sessionCount {
		t.Errorf("created unique sessions = %d, want %d", len(unique), sessionCount)
	}
}

func testCreateParams() CreateParams {
	return CreateParams{
		Request: model.CreateSMContextRequest{
			SUPI:         "imsi-452040000000001",
			PDUSessionID: 1,
			DNN:          "v-internet",
			SNSSAI:       model.SNSSAI{SST: 1, SD: "000001"},
		},
		HandledBy: "pdu-session-1",
		Status:    constants.SMContextActive,
	}
}

func validUUIDV4(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	if value[14] != '4' || !containsByte("89ab", value[19]) {
		return false
	}
	for index, character := range []byte(value) {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !containsByte("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func containsByte(allowed string, value byte) bool {
	for index := range len(allowed) {
		if allowed[index] == value {
			return true
		}
	}
	return false
}
