package ipc_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/ipc"
)

const frameMaxBytes = 1 << 20

func TestFrameErrorCodeConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "truncated length", got: string(ipc.FrameErrorCodeTruncatedLength), want: "truncated_length"},
		{name: "frame too large", got: string(ipc.FrameErrorCodeFrameTooLarge), want: "frame_too_large"},
		{name: "truncated payload", got: string(ipc.FrameErrorCodeTruncatedPayload), want: "truncated_payload"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("constant = %q, want %q", test.got, test.want)
			}
		})
	}
}

func TestDecodeFrameValidGoldenOperations(t *testing.T) {
	for _, name := range []string{
		"valid/probe-capabilities.json",
		"valid/snapshot-managed.json",
		"valid/apply-infrastructure.json",
		"valid/apply-policy.json",
		"valid/apply-target.json",
		"valid/remove-managed-infrastructure.json",
	} {
		name := name
		t.Run(strings.TrimSuffix(strings.TrimPrefix(name, "valid/"), ".json"), func(t *testing.T) {
			request, err := ipc.DecodeFrame(bytes.NewReader(frame(readGoldenFile(t, name))))
			if err != nil {
				t.Fatalf("DecodeFrame() error = %v", err)
			}
			assertValidGoldenRequest(t, name, request)
		})
	}
}

func TestDecodeFrameTruncatedLength(t *testing.T) {
	for size := 0; size < 4; size++ {
		t.Run(string(rune('0'+size))+" bytes", func(t *testing.T) {
			request, err := ipc.DecodeFrame(bytes.NewReader(make([]byte, size)))
			if request != nil {
				t.Fatalf("DecodeFrame() request = %#v, want nil", request)
			}
			assertFrameErrorCode(t, err, ipc.FrameErrorCodeTruncatedLength)
		})
	}
}

func TestDecodeFrameTruncatedPayload(t *testing.T) {
	payload := readGoldenFile(t, "valid/probe-capabilities.json")
	truncated := frame(payload)
	truncated = truncated[:len(truncated)-1]
	request, err := ipc.DecodeFrame(bytes.NewReader(truncated))
	if request != nil {
		t.Fatalf("DecodeFrame() request = %#v, want nil", request)
	}
	assertFrameErrorCode(t, err, ipc.FrameErrorCodeTruncatedPayload)
}

func TestDecodeFrameLengthLimits(t *testing.T) {
	t.Run("64 KiB exact", func(t *testing.T) {
		base := readGoldenFile(t, "valid/probe-capabilities.json")
		payload := append(append([]byte(nil), base...), bytes.Repeat([]byte(" "), maxRequestBytes-len(base))...)
		request, err := ipc.DecodeFrame(bytes.NewReader(frame(payload)))
		if err != nil {
			t.Fatalf("exact request limit rejected: %v", err)
		}
		if request.Operation() != ipc.OperationProbeCapabilities {
			t.Fatalf("operation = %q, want %q", request.Operation(), ipc.OperationProbeCapabilities)
		}
	})

	t.Run("64 KiB one-over does not read payload", func(t *testing.T) {
		reader := newHeaderOnlyReader(maxRequestBytes + 1)
		request, err := ipc.DecodeFrame(reader)
		if request != nil {
			t.Fatalf("DecodeFrame() request = %#v, want nil", request)
		}
		assertErrorCode(t, err, ipc.ErrorCodeRequestTooLarge)
		if reader.payloadRead {
			t.Fatal("DecodeFrame() read payload after declared request length exceeded 64 KiB")
		}
	})

	t.Run("1 MiB exact reaches request cap first", func(t *testing.T) {
		reader := newHeaderOnlyReader(frameMaxBytes)
		request, err := ipc.DecodeFrame(reader)
		if request != nil {
			t.Fatalf("DecodeFrame() request = %#v, want nil", request)
		}
		assertErrorCode(t, err, ipc.ErrorCodeRequestTooLarge)
		if reader.payloadRead {
			t.Fatal("DecodeFrame() read payload after declared request length exceeded 64 KiB")
		}
	})

	t.Run("1 MiB one-over reaches frame cap first", func(t *testing.T) {
		reader := newHeaderOnlyReader(frameMaxBytes + 1)
		request, err := ipc.DecodeFrame(reader)
		if request != nil {
			t.Fatalf("DecodeFrame() request = %#v, want nil", request)
		}
		assertFrameErrorCode(t, err, ipc.FrameErrorCodeFrameTooLarge)
		if reader.payloadRead {
			t.Fatal("DecodeFrame() read payload after declared frame length exceeded 1 MiB")
		}
	})
}

func TestDecodeFrameDelegatesPayloadValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  []byte
		code ipc.ErrorCode
	}{
		{name: "invalid JSON", raw: []byte(`{"version":1`), code: ipc.ErrorCodeInvalidJSON},
		{name: "unknown operation", raw: []byte(`{"version":1,"operation":"ExecuteShell","payload":{}}`), code: ipc.ErrorCodeSchemaRejected},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := ipc.DecodeFrame(bytes.NewReader(frame(test.raw)))
			if request != nil {
				t.Fatalf("DecodeFrame() request = %#v, want nil", request)
			}
			assertErrorCode(t, err, test.code)
			var frameError *ipc.FrameError
			if errors.As(err, &frameError) {
				t.Fatalf("payload validation returned framing code %q", frameError.Code())
			}
		})
	}
}

func TestDecodeFrameReadsConsecutiveFrames(t *testing.T) {
	first := frame(readGoldenFile(t, "valid/probe-capabilities.json"))
	second := frame(readGoldenFile(t, "valid/snapshot-managed.json"))
	reader := bytes.NewReader(append(first, second...))

	request, err := ipc.DecodeFrame(reader)
	if err != nil {
		t.Fatalf("first DecodeFrame() error = %v", err)
	}
	if request.Operation() != ipc.OperationProbeCapabilities {
		t.Fatalf("first operation = %q", request.Operation())
	}

	request, err = ipc.DecodeFrame(reader)
	if err != nil {
		t.Fatalf("second DecodeFrame() error = %v", err)
	}
	if request.Operation() != ipc.OperationSnapshotManaged {
		t.Fatalf("second operation = %q", request.Operation())
	}
	if reader.Len() != 0 {
		t.Fatalf("reader has %d trailing bytes, want 0", reader.Len())
	}
}

func TestDecodeFrameErrorsDoNotEchoInput(t *testing.T) {
	const marker = "frame-secret-69e05f7c"

	t.Run("payload validation", func(t *testing.T) {
		raw := []byte(`{"version":1,"operation":"ProbeCapabilities","payload":{},"unknown":"` + marker + `"}`)
		_, err := ipc.DecodeFrame(bytes.NewReader(frame(raw)))
		assertErrorCode(t, err, ipc.ErrorCodeSchemaRejected)
		if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), string(raw)) {
			t.Fatalf("validation error echoed attacker-controlled input: %q", err)
		}
	})

	t.Run("stream failure", func(t *testing.T) {
		reader := &headerOnlyReader{header: frameHeader(1), payloadError: errors.New(marker)}
		_, err := ipc.DecodeFrame(reader)
		assertFrameErrorCode(t, err, ipc.FrameErrorCodeTruncatedPayload)
		if !reader.payloadRead {
			t.Fatal("test reader did not expose its payload error")
		}
		if strings.Contains(err.Error(), marker) {
			t.Fatalf("frame error echoed stream error: %q", err)
		}
	})
}

func FuzzDecodeFrameClosedUnion(f *testing.F) {
	for _, name := range []string{
		"valid/probe-capabilities.json",
		"valid/snapshot-managed.json",
		"valid/apply-target.json",
		"valid/remove-managed-infrastructure.json",
	} {
		f.Add(frame(readGoldenFile(f, name)))
	}
	f.Add([]byte{0, 0, 0})
	f.Add(frameHeader(frameMaxBytes + 1))
	f.Add(frame([]byte(`{"version":1`)))

	f.Fuzz(func(t *testing.T, raw []byte) {
		request, err := ipc.DecodeFrame(bytes.NewReader(raw))
		if err == nil {
			assertClosedRequestUnion(t, request)
			return
		}
		if request != nil {
			t.Fatalf("rejected frame returned request %#v", request)
		}
		var frameError *ipc.FrameError
		var validationError *ipc.ValidationError
		if !errors.As(err, &frameError) && !errors.As(err, &validationError) {
			t.Fatalf("DecodeFrame() error type = %T, want framing or validation error", err)
		}
	})
}

func assertFrameErrorCode(t *testing.T, err error, want ipc.FrameErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("DecodeFrame() error = nil, want code %q", want)
	}
	var frameError *ipc.FrameError
	if !errors.As(err, &frameError) {
		t.Fatalf("DecodeFrame() error type = %T, want *ipc.FrameError", err)
	}
	if got := frameError.Code(); got != want {
		t.Fatalf("frame error code = %q, want %q", got, want)
	}
}

func frame(payload []byte) []byte {
	result := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(result[:4], uint32(len(payload)))
	copy(result[4:], payload)
	return result
}

func frameHeader(length int) []byte {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(length))
	return header
}

type headerOnlyReader struct {
	header       []byte
	offset       int
	payloadRead  bool
	payloadError error
}

func newHeaderOnlyReader(length int) *headerOnlyReader {
	return &headerOnlyReader{
		header:       frameHeader(length),
		payloadError: errors.New("unexpected payload read"),
	}
}

func (r *headerOnlyReader) Read(destination []byte) (int, error) {
	if r.offset < len(r.header) {
		count := copy(destination, r.header[r.offset:])
		r.offset += count
		return count, nil
	}
	r.payloadRead = true
	if r.payloadError != nil {
		return 0, r.payloadError
	}
	return 0, io.EOF
}
