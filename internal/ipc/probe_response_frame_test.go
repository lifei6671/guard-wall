package ipc_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestProbeCapabilitiesResponseFrameRoundTrip(t *testing.T) {
	response, err := ipc.NewProbeCapabilitiesSuccessResponse(completeProbeCapabilities(t))
	if err != nil {
		t.Fatalf("NewProbeCapabilitiesSuccessResponse(): %v", err)
	}
	payload, err := ipc.EncodeProbeCapabilitiesResponse(response)
	if err != nil {
		t.Fatalf("EncodeProbeCapabilitiesResponse(): %v", err)
	}

	var output bytes.Buffer
	if err := ipc.WriteProbeCapabilitiesResponseFrame(&output, response); err != nil {
		t.Fatalf("WriteProbeCapabilitiesResponseFrame(): %v", err)
	}
	want := probeResponseTestFrame(payload)
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("frame = %x, want %x", output.Bytes(), want)
	}

	decoded, err := ipc.DecodeProbeCapabilitiesResponseFrame(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("DecodeProbeCapabilitiesResponseFrame(): %v", err)
	}
	success, ok := decoded.(ipc.ProbeCapabilitiesSuccessResponse)
	if !ok {
		t.Fatalf("decoded type = %T", decoded)
	}
	assertProbeCapabilitiesEqual(t, success.Capabilities(), response.Capabilities())
}

func TestProbeCapabilitiesFailureResponseFrameRoundTrip(t *testing.T) {
	response, err := ipc.NewProbeCapabilitiesFailureResponse(ipc.ProbeCapabilitiesFailureCodeNotReady)
	if err != nil {
		t.Fatalf("NewProbeCapabilitiesFailureResponse(): %v", err)
	}
	var output bytes.Buffer
	if err := ipc.WriteProbeCapabilitiesResponseFrame(&output, response); err != nil {
		t.Fatalf("WriteProbeCapabilitiesResponseFrame(): %v", err)
	}
	decoded, err := ipc.DecodeProbeCapabilitiesResponseFrame(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("DecodeProbeCapabilitiesResponseFrame(): %v", err)
	}
	failure, ok := decoded.(ipc.ProbeCapabilitiesFailureResponse)
	if !ok || failure.FailureCode() != ipc.ProbeCapabilitiesFailureCodeNotReady {
		t.Fatalf("decoded = %T/%v", decoded, decoded)
	}
}

func TestDecodeProbeCapabilitiesResponseFrameBoundaries(t *testing.T) {
	t.Run("truncated header", func(t *testing.T) {
		response, err := ipc.DecodeProbeCapabilitiesResponseFrame(bytes.NewReader([]byte{0, 0, 0}))
		if response != nil {
			t.Fatalf("response = %T, want nil", response)
		}
		assertProbeFrameErrorCode(t, err, ipc.FrameErrorCodeTruncatedLength)
	})

	t.Run("frame too large before payload read", func(t *testing.T) {
		reader := &probeHeaderOnlyReader{header: probeResponseHeader((1 << 20) + 1)}
		response, err := ipc.DecodeProbeCapabilitiesResponseFrame(reader)
		if response != nil {
			t.Fatalf("response = %T, want nil", response)
		}
		assertProbeFrameErrorCode(t, err, ipc.FrameErrorCodeFrameTooLarge)
		if reader.payloadRead {
			t.Fatal("decoder read payload after oversized frame header")
		}
	})

	t.Run("response too large before payload read", func(t *testing.T) {
		reader := &probeHeaderOnlyReader{header: probeResponseHeader(4097)}
		response, err := ipc.DecodeProbeCapabilitiesResponseFrame(reader)
		if response != nil {
			t.Fatalf("response = %T, want nil", response)
		}
		assertProbeCapabilitiesResponseErrorCode(t, err, ipc.ProbeCapabilitiesResponseErrorCodeResponseTooLarge)
		if reader.payloadRead {
			t.Fatal("decoder read payload after oversized response header")
		}
	})

	t.Run("truncated payload", func(t *testing.T) {
		frame := append(probeResponseHeader(4), []byte("abc")...)
		response, err := ipc.DecodeProbeCapabilitiesResponseFrame(bytes.NewReader(frame))
		if response != nil {
			t.Fatalf("response = %T, want nil", response)
		}
		assertProbeFrameErrorCode(t, err, ipc.FrameErrorCodeTruncatedPayload)
	})

	t.Run("invalid payload delegates codec", func(t *testing.T) {
		response, err := ipc.DecodeProbeCapabilitiesResponseFrame(
			bytes.NewReader(probeResponseTestFrame([]byte(`{}`))),
		)
		if response != nil {
			t.Fatalf("response = %T, want nil", response)
		}
		assertProbeCapabilitiesResponseErrorCode(t, err, ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected)
	})
}

func TestWriteProbeCapabilitiesResponseFrameShortWritesSameFrame(t *testing.T) {
	response, err := ipc.NewProbeCapabilitiesFailureResponse(ipc.ProbeCapabilitiesFailureCodeUnsupported)
	if err != nil {
		t.Fatalf("NewProbeCapabilitiesFailureResponse(): %v", err)
	}
	payload, err := ipc.EncodeProbeCapabilitiesResponse(response)
	if err != nil {
		t.Fatalf("EncodeProbeCapabilitiesResponse(): %v", err)
	}
	writer := &probeChunkWriter{maxChunk: 3}
	if err := ipc.WriteProbeCapabilitiesResponseFrame(writer, response); err != nil {
		t.Fatalf("WriteProbeCapabilitiesResponseFrame(): %v", err)
	}
	if !bytes.Equal(writer.output.Bytes(), probeResponseTestFrame(payload)) {
		t.Fatalf("short-write output = %x", writer.output.Bytes())
	}
	if writer.writes < 2 {
		t.Fatalf("writes = %d, want positive short writes", writer.writes)
	}
}

func TestWriteProbeCapabilitiesResponseFrameFailuresAreFailClosed(t *testing.T) {
	valid, err := ipc.NewProbeCapabilitiesFailureResponse(ipc.ProbeCapabilitiesFailureCodeNotReady)
	if err != nil {
		t.Fatalf("NewProbeCapabilitiesFailureResponse(): %v", err)
	}

	t.Run("nil writer", func(t *testing.T) {
		err := ipc.WriteProbeCapabilitiesResponseFrame(nil, valid)
		assertProbeFrameErrorCode(t, err, ipc.FrameErrorCodeWriteFailed)
	})

	t.Run("invalid response is rejected before first write", func(t *testing.T) {
		writer := &probeFaultWriter{mode: probeFaultError}
		err := ipc.WriteProbeCapabilitiesResponseFrame(writer, nil)
		assertProbeCapabilitiesResponseErrorCode(t, err, ipc.ProbeCapabilitiesResponseErrorCodeSchemaRejected)
		if writer.writes != 0 {
			t.Fatalf("writes = %d, want zero", writer.writes)
		}
	})

	for _, mode := range []probeFaultMode{probeFaultError, probeFaultZero, probeFaultOverlong} {
		t.Run(string(mode), func(t *testing.T) {
			writer := &probeFaultWriter{mode: mode}
			err := ipc.WriteProbeCapabilitiesResponseFrame(writer, valid)
			assertProbeFrameErrorCode(t, err, ipc.FrameErrorCodeWriteFailed)
			if writer.writes != 1 {
				t.Fatalf("writes = %d, want 1 with no second response", writer.writes)
			}
		})
	}
}

func assertProbeFrameErrorCode(t testing.TB, err error, want ipc.FrameErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %q", want)
	}
	var typed *ipc.FrameError
	if !errors.As(err, &typed) {
		t.Fatalf("error type = %T, want *ipc.FrameError", err)
	}
	if typed.Code() != want {
		t.Fatalf("frame error code = %q, want %q", typed.Code(), want)
	}
}

func probeResponseTestFrame(payload []byte) []byte {
	return append(probeResponseHeader(uint32(len(payload))), payload...)
}

func probeResponseHeader(length uint32) []byte {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, length)
	return header
}

type probeHeaderOnlyReader struct {
	header      []byte
	offset      int
	payloadRead bool
}

func (r *probeHeaderOnlyReader) Read(destination []byte) (int, error) {
	if r.offset < len(r.header) {
		copied := copy(destination, r.header[r.offset:])
		r.offset += copied
		return copied, nil
	}
	r.payloadRead = true
	return 0, errors.New("unexpected payload read")
}

type probeChunkWriter struct {
	maxChunk int
	writes   int
	output   bytes.Buffer
}

func (w *probeChunkWriter) Write(value []byte) (int, error) {
	w.writes++
	length := len(value)
	if length > w.maxChunk {
		length = w.maxChunk
	}
	return w.output.Write(value[:length])
}

type probeFaultMode string

const (
	probeFaultError    probeFaultMode = "error"
	probeFaultZero     probeFaultMode = "zero"
	probeFaultOverlong probeFaultMode = "overlong"
)

type probeFaultWriter struct {
	mode   probeFaultMode
	writes int
}

func (w *probeFaultWriter) Write(value []byte) (int, error) {
	w.writes++
	switch w.mode {
	case probeFaultError:
		return 0, io.ErrClosedPipe
	case probeFaultZero:
		return 0, nil
	case probeFaultOverlong:
		return len(value) + 1, nil
	default:
		return len(value), nil
	}
}
