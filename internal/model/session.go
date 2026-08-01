// Package model defines the HTTP API contract shared by the Gateway and PDU
// Session services.
package model

// CreateSMContextRequest represents Nsmf_PDUSession_CreateSMContext input.
// Validation of required fields belongs to the HTTP boundary using this model.
type CreateSMContextRequest struct {
	SUPI         string `json:"supi"`
	GPSI         string `json:"gpsi,omitempty"`
	PDUSessionID int    `json:"pduSessionId"`
	DNN          string `json:"dnn"`
	SNSSAI       SNSSAI `json:"sNssai"`
	ServingNFID  string `json:"servingNfId,omitempty"`
	ANType       string `json:"anType,omitempty"`
}

// SNSSAI identifies the network slice requested for a PDU session.
type SNSSAI struct {
	SST int    `json:"sst"`
	SD  string `json:"sd"`
}

// SMContextStatus is the lifecycle state returned for an SM context.
type SMContextStatus string

const (
	// SMContextActive indicates that the context was created successfully.
	SMContextActive SMContextStatus = "ACTIVE"
)

// CreateSMContextResponse is returned after a PDU creates a local context.
type CreateSMContextResponse struct {
	SMContextRef string          `json:"smContextRef"`
	SUPI         string          `json:"supi"`
	PDUSessionID int             `json:"pduSessionId"`
	HandledBy    string          `json:"handledBy"`
	Status       SMContextStatus `json:"status"`
}
