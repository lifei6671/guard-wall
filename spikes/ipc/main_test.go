//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestDecodeFrameFailClosed(t *testing.T) {
	tests := []struct {
		name          string
		frame         []byte
		wantReason    string
		wantLength    uint32
		wantOperation string
	}{
		{
			name:          "probe capabilities",
			frame:         framed(encodedRequest("ProbeCapabilities")),
			wantReason:    "operation_allowed",
			wantLength:    uint32(len(encodedRequest("ProbeCapabilities"))),
			wantOperation: "ProbeCapabilities",
		},
		{
			name:          "snapshot managed",
			frame:         framed(encodedRequest("SnapshotManaged")),
			wantReason:    "operation_allowed",
			wantLength:    uint32(len(encodedRequest("SnapshotManaged"))),
			wantOperation: "SnapshotManaged",
		},
		{
			name:          "apply managed plan",
			frame:         framed(encodedRequest("ApplyManagedPlan")),
			wantReason:    "operation_allowed",
			wantLength:    uint32(len(encodedRequest("ApplyManagedPlan"))),
			wantOperation: "ApplyManagedPlan",
		},
		{
			name:          "remove managed infrastructure",
			frame:         framed(encodedRequest("RemoveManagedInfrastructure")),
			wantReason:    "operation_allowed",
			wantLength:    uint32(len(encodedRequest("RemoveManagedInfrastructure"))),
			wantOperation: "RemoveManagedInfrastructure",
		},
		{
			name:          "unknown operation",
			frame:         framed(encodedRequest("ExecuteShell")),
			wantReason:    "operation_rejected",
			wantLength:    uint32(len(encodedRequest("ExecuteShell"))),
			wantOperation: "ExecuteShell",
		},
		{
			name:       "truncated length prefix",
			frame:      []byte{0, 0, 1},
			wantReason: "truncated_length",
		},
		{
			name:       "truncated payload",
			frame:      frameWithLength(16, []byte(`{"version":1}`)),
			wantReason: "truncated_payload",
			wantLength: 16,
		},
		{
			name:       "invalid json",
			frame:      framed([]byte(`{"version":`)),
			wantReason: "invalid_json",
			wantLength: uint32(len(`{"version":`)),
		},
		{
			name:          "unknown version",
			frame:         framed([]byte(`{"version":2,"operation":"ProbeCapabilities"}`)),
			wantReason:    "unsupported_version",
			wantLength:    uint32(len(`{"version":2,"operation":"ProbeCapabilities"}`)),
			wantOperation: "ProbeCapabilities",
		},
		{
			name:       "unknown field",
			frame:      framed([]byte(`{"version":1,"operation":"ProbeCapabilities","command":"id"}`)),
			wantReason: "invalid_json",
			wantLength: uint32(len(`{"version":1,"operation":"ProbeCapabilities","command":"id"}`)),
		},
		{
			name:       "multiple json values",
			frame:      framed([]byte(`{"version":1,"operation":"ProbeCapabilities"}{}`)),
			wantReason: "invalid_json",
			wantLength: uint32(len(`{"version":1,"operation":"ProbeCapabilities"}{}`)),
		},
		{
			name:       "maximum declared length rejected before allocation",
			frame:      frameWithLength(^uint32(0), nil),
			wantReason: "frame_too_large",
			wantLength: ^uint32(0),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			length, decoded, reason := decodeFrame(bytes.NewReader(test.frame))
			if reason != test.wantReason {
				t.Fatalf("reason = %q, want %q", reason, test.wantReason)
			}
			if length != test.wantLength {
				t.Fatalf("length = %d, want %d", length, test.wantLength)
			}
			if decoded.Operation != test.wantOperation {
				t.Fatalf("operation = %q, want %q", decoded.Operation, test.wantOperation)
			}
		})
	}
}

func FuzzDecodeFrameFailClosed(f *testing.F) {
	f.Add(framed(encodedRequest("ProbeCapabilities")))
	f.Add(frameWithLength(maxFrame+1, nil))
	f.Add([]byte{0, 0, 0})
	f.Add(framed([]byte(`{"version":1,"operation":"ProbeCapabilities","unknown":true}`)))

	f.Fuzz(func(t *testing.T, frame []byte) {
		_, decoded, reason := decodeFrame(bytes.NewReader(frame))
		if reason != "operation_allowed" {
			return
		}
		if decoded.Version != 1 {
			t.Fatalf("accepted protocol version %d", decoded.Version)
		}
		if _, ok := allowedOperations[decoded.Operation]; !ok {
			t.Fatalf("accepted operation %q outside the allowlist", decoded.Operation)
		}
	})
}

func framed(payload []byte) []byte {
	return frameWithLength(uint32(len(payload)), payload)
}

func frameWithLength(length uint32, payload []byte) []byte {
	var frame bytes.Buffer
	if err := binary.Write(&frame, binary.BigEndian, length); err != nil {
		panic(err)
	}
	frame.Write(payload)
	return frame.Bytes()
}
