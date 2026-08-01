package model

import (
	"encoding/json"
	"testing"
)

func TestServiceResponseJSON(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{
			name:  "health",
			value: HealthResponse{InstanceID: "pdu-session-1", Status: ServiceUp},
			want:  `{"instanceId":"pdu-session-1","status":"UP"}`,
		},
		{
			name: "metrics",
			value: MetricsResponse{
				InstanceID: "pdu-session-1", Weight: 3, ActiveRequests: 17,
			},
			want: `{"instanceId":"pdu-session-1","weight":3,"activeRequests":17}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatalf("encode response: %v", err)
			}
			assertJSONEqual(t, encoded, []byte(test.want))
		})
	}
}
