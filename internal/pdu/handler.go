package pdu

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dangtuananh123456/gateway/internal/model"
	"github.com/dangtuananh123456/gateway/internal/store"
	"github.com/dangtuananh123456/gateway/pkg/constants"
)

// HandlerConfig contains the PDU identity and create-session behavior.
type HandlerConfig struct {
	InstanceID       string
	PublicGatewayURL string
	ProcessingDelay  time.Duration
}

type sessionCreator interface {
	Create(context.Context, store.CreateParams) (store.Session, error)
}

// Handler owns the PDU HTTP routes and node-local active request counter.
type Handler struct {
	instanceID       string
	contextRefPrefix string
	processingDelay  time.Duration
	sessions         sessionCreator
	activeRequests   atomic.Int64
}

// NewHandler builds a PDU handler with an injected node-local session store.
func NewHandler(cfg HandlerConfig, sessions sessionCreator) (*Handler, error) {
	if sessions == nil {
		return nil, errors.New("create PDU handler: session store must not be nil")
	}
	if strings.TrimSpace(cfg.InstanceID) == "" {
		return nil, errors.New("create PDU handler: instance ID must not be empty")
	}
	if strings.TrimSpace(cfg.PublicGatewayURL) == "" {
		return nil, errors.New("create PDU handler: public Gateway URL must not be empty")
	}
	if cfg.ProcessingDelay < 0 {
		return nil, errors.New("create PDU handler: processing delay must not be negative")
	}

	return &Handler{
		instanceID:       cfg.InstanceID,
		contextRefPrefix: strings.TrimSuffix(cfg.PublicGatewayURL, "/") + constants.CreateSMContextPath + "/",
		processingDelay:  cfg.ProcessingDelay,
		sessions:         sessions,
	}, nil
}

// ServeHTTP routes the small, fixed PDU API without allocating a router state
// per request.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case constants.HealthPath:
		if request.Method != http.MethodGet {
			handler.writeMethodNotAllowed(writer, http.MethodGet)
			return
		}
		handler.handleHealth(writer)
	case constants.CreateSMContextPath:
		if request.Method != http.MethodPost {
			handler.writeMethodNotAllowed(writer, http.MethodPost)
			return
		}
		handler.handleCreateSMContext(writer, request)
	default:
		writeJSON(
			writer,
			http.StatusNotFound,
			model.NewErrorResponse(constants.CauseNotFound, "resource not found"),
		)
	}
}

func (handler *Handler) handleHealth(writer http.ResponseWriter) {
	writeJSON(writer, http.StatusOK, model.HealthResponse{
		InstanceID: handler.instanceID,
		Status:     constants.ServiceUp,
	})
}

func (handler *Handler) handleCreateSMContext(
	writer http.ResponseWriter,
	request *http.Request,
) {
	payload, requestErr := decodeCreateSMContext(writer, request)
	if requestErr != nil {
		writeRequestError(writer, requestErr)
		return
	}

	handler.activeRequests.Add(1)
	defer handler.activeRequests.Add(-1)

	if err := waitForProcessing(request.Context(), handler.processingDelay); err != nil {
		return
	}
	session, err := handler.sessions.Create(request.Context(), store.CreateParams{
		Request:   payload,
		HandledBy: handler.instanceID,
		Status:    constants.SMContextActive,
	})
	if err != nil {
		if request.Context().Err() != nil {
			return
		}
		writeJSON(
			writer,
			http.StatusInternalServerError,
			model.NewErrorResponse(constants.CauseInternalError, "failed to create session context"),
		)
		return
	}

	contextReference := handler.contextRefPrefix + session.ContextID
	writer.Header().Set("Location", contextReference)
	writeJSON(writer, http.StatusCreated, model.CreateSMContextResponse{
		SMContextRef: contextReference,
		SUPI:         payload.SUPI,
		PDUSessionID: payload.PDUSessionID,
		HandledBy:    handler.instanceID,
		Status:       constants.SMContextActive,
	})
}

func (handler *Handler) writeMethodNotAllowed(writer http.ResponseWriter, allowedMethod string) {
	writer.Header().Set("Allow", allowedMethod)
	writeJSON(
		writer,
		http.StatusMethodNotAllowed,
		model.NewErrorResponse(constants.CauseMethodNotAllowed, "method not allowed"),
	)
}

func waitForProcessing(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay == 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
