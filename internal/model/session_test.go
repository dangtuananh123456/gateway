package model

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dangtuananh123456/gateway/pkg/constants"
)

func TestCreateSMContextRequestJSON(t *testing.T) {
	input := []byte(`{
  "supi": "imsi-452040000000001",
  "gpsi": "msisdn-84900000001",
  "pduSessionId": 1,
  "dnn": "v-internet",
  "sNssai": {"sst": 1, "sd": "000001"},
  "servingNfId": "2ab2b5a9-68e8-4ee6-b939-024c109b520c",
  "anType": "3GPP_ACCESS"
}`)
	want := CreateSMContextRequest{
		SUPI:         "imsi-452040000000001",
		GPSI:         "msisdn-84900000001",
		PDUSessionID: 1,
		DNN:          "v-internet",
		SNSSAI:       SNSSAI{SST: 1, SD: "000001"},
		ServingNFID:  "2ab2b5a9-68e8-4ee6-b939-024c109b520c",
		ANType:       "3GPP_ACCESS",
	}

	var got CreateSMContextRequest
	if err := json.Unmarshal(input, &got); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decoded request = %+v, want %+v", got, want)
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	assertJSONEqual(t, encoded, input)
}

func TestCreateSMContextResponseJSON(t *testing.T) {
	response := CreateSMContextResponse{
		SMContextRef: "http://gw/nsmf-pdusession/v1/sm-contexts/ctx-0001",
		SUPI:         "imsi-452040000000001",
		PDUSessionID: 1,
		HandledBy:    "pdu-session-2",
		Status:       constants.SMContextActive,
	}
	wantJSON := []byte(`{
  "smContextRef": "http://gw/nsmf-pdusession/v1/sm-contexts/ctx-0001",
  "supi": "imsi-452040000000001",
  "pduSessionId": 1,
  "handledBy": "pdu-session-2",
  "status": "ACTIVE"
}`)

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	assertJSONEqual(t, encoded, wantJSON)

	var decoded CreateSMContextResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !reflect.DeepEqual(decoded, response) {
		t.Errorf("decoded response = %+v, want %+v", decoded, response)
	}
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode actual JSON: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode expected JSON: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("JSON = %s, want %s", got, want)
	}
}
