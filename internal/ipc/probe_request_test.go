package ipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

const probeCapabilitiesRequestGolden = `{"version":1,"operation":"ProbeCapabilities","payload":{}}`

func TestProbeCapabilitiesRequestConstructorAndDeterministicEncoding(t *testing.T) {
	request := NewProbeCapabilitiesRequest()
	if request == nil || request.Operation() != OperationProbeCapabilities {
		t.Fatalf("request = %#v, want typed ProbeCapabilities request", request)
	}

	for iteration := 0; iteration < 32; iteration++ {
		raw, err := EncodeProbeCapabilitiesRequest(request)
		if err != nil {
			t.Fatalf("EncodeProbeCapabilitiesRequest() iteration %d: %v", iteration, err)
		}
		if string(raw) != probeCapabilitiesRequestGolden {
			t.Fatalf("encoded request = %q, want %q", raw, probeCapabilitiesRequestGolden)
		}
	}
}

func TestProbeCapabilitiesRequestCodecRoundTrip(t *testing.T) {
	raw, err := EncodeProbeCapabilitiesRequest(NewProbeCapabilitiesRequest())
	if err != nil {
		t.Fatalf("EncodeProbeCapabilitiesRequest(): %v", err)
	}
	decoded, err := DecodeRequest(raw)
	if err != nil {
		t.Fatalf("DecodeRequest(): %v", err)
	}
	if _, ok := decoded.(ProbeCapabilitiesRequest); !ok {
		t.Fatalf("decoded request = %T, want ProbeCapabilitiesRequest", decoded)
	}
}

func TestProbeCapabilitiesRequestRejectsNilWithoutBytes(t *testing.T) {
	var typedNil *probeCapabilitiesRequest
	tests := []struct {
		name    string
		request ProbeCapabilitiesRequest
	}{
		{name: "nil interface", request: nil},
		{name: "typed nil", request: typedNil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := EncodeProbeCapabilitiesRequest(test.request)
			if len(raw) != 0 {
				t.Fatalf("encoded bytes = %q, want none", raw)
			}
			assertProbeRequestValidationCode(t, err, ErrorCodeSchemaRejected)

			writer := &bytes.Buffer{}
			err = WriteProbeCapabilitiesRequestFrame(writer, test.request)
			if writer.Len() != 0 {
				t.Fatalf("frame writer emitted %d bytes before validation", writer.Len())
			}
			assertProbeRequestValidationCode(t, err, ErrorCodeSchemaRejected)
		})
	}
}

func TestProbeCapabilitiesRequestFrameRoundTrip(t *testing.T) {
	writer := &bytes.Buffer{}
	if err := WriteProbeCapabilitiesRequestFrame(writer, NewProbeCapabilitiesRequest()); err != nil {
		t.Fatalf("WriteProbeCapabilitiesRequestFrame(): %v", err)
	}
	frame := writer.Bytes()
	if len(frame) < 4 {
		t.Fatalf("frame length = %d, want header and payload", len(frame))
	}
	if got := binary.BigEndian.Uint32(frame[:4]); got != uint32(len(probeCapabilitiesRequestGolden)) {
		t.Fatalf("declared payload = %d, want %d", got, len(probeCapabilitiesRequestGolden))
	}
	decoded, err := DecodeFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("DecodeFrame(): %v", err)
	}
	if _, ok := decoded.(ProbeCapabilitiesRequest); !ok {
		t.Fatalf("decoded request = %T, want ProbeCapabilitiesRequest", decoded)
	}
}

func TestProbeCapabilitiesRequestErrorsDoNotEchoAttackerText(t *testing.T) {
	secret := "probe-request-secret"
	_, err := DecodeRequest([]byte(`{"version":1,"operation":"ProbeCapabilities","payload":{"command":"` + secret + `"}}`))
	if err == nil {
		t.Fatal("DecodeRequest() error = nil")
	}
	if bytes.Contains([]byte(err.Error()), []byte(secret)) {
		t.Fatalf("error leaked request contents: %q", err)
	}
}

func assertProbeRequestValidationCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("error = %T %v, want *ValidationError", err, err)
	}
	if validation.Code() != want {
		t.Fatalf("validation code = %q, want %q", validation.Code(), want)
	}
}
