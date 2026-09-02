package ipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

const snapshotManagedRequestGolden = `{"version":1,"operation":"SnapshotManaged","payload":{}}`

func TestSnapshotManagedRequestConstructorAndDeterministicEncoding(t *testing.T) {
	request := NewSnapshotManagedRequest()
	if request == nil || request.Operation() != OperationSnapshotManaged {
		t.Fatalf("request = %#v, want typed SnapshotManaged request", request)
	}

	for iteration := 0; iteration < 32; iteration++ {
		raw, err := EncodeSnapshotManagedRequest(request)
		if err != nil {
			t.Fatalf("EncodeSnapshotManagedRequest() iteration %d: %v", iteration, err)
		}
		if string(raw) != snapshotManagedRequestGolden {
			t.Fatalf("encoded request = %q, want %q", raw, snapshotManagedRequestGolden)
		}
	}
}

func TestSnapshotManagedRequestCodecAndFrameRoundTrip(t *testing.T) {
	raw, err := EncodeSnapshotManagedRequest(NewSnapshotManagedRequest())
	if err != nil {
		t.Fatalf("EncodeSnapshotManagedRequest(): %v", err)
	}
	decoded, err := DecodeRequest(raw)
	if err != nil {
		t.Fatalf("DecodeRequest(): %v", err)
	}
	if _, ok := decoded.(SnapshotManagedRequest); !ok {
		t.Fatalf("decoded request = %T, want SnapshotManagedRequest", decoded)
	}

	writer := &bytes.Buffer{}
	if err := WriteSnapshotManagedRequestFrame(writer, NewSnapshotManagedRequest()); err != nil {
		t.Fatalf("WriteSnapshotManagedRequestFrame(): %v", err)
	}
	frame := writer.Bytes()
	if got := binary.BigEndian.Uint32(frame[:4]); got != uint32(len(snapshotManagedRequestGolden)) {
		t.Fatalf("declared payload = %d, want %d", got, len(snapshotManagedRequestGolden))
	}
	decoded, err = DecodeFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("DecodeFrame(): %v", err)
	}
	if _, ok := decoded.(SnapshotManagedRequest); !ok {
		t.Fatalf("decoded frame = %T, want SnapshotManagedRequest", decoded)
	}
}

func TestSnapshotManagedRequestRejectsNilBeforeWrite(t *testing.T) {
	var typedNil *snapshotManagedRequest
	for _, request := range []SnapshotManagedRequest{nil, typedNil} {
		writer := &bytes.Buffer{}
		err := WriteSnapshotManagedRequestFrame(writer, request)
		if writer.Len() != 0 {
			t.Fatalf("invalid request emitted %d bytes", writer.Len())
		}
		var validation *ValidationError
		if !errors.As(err, &validation) || validation.Code() != ErrorCodeSchemaRejected {
			t.Fatalf("error = %T %v, want schema-rejected ValidationError", err, err)
		}
	}
}
