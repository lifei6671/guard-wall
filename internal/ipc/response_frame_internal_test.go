package ipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestWriteFramePayloadLimits(t *testing.T) {
	t.Run("zero length", func(t *testing.T) {
		var output bytes.Buffer
		if err := writeFramePayload(&output, nil); err != nil {
			t.Fatalf("writeFramePayload() error = %v", err)
		}
		if got := output.Bytes(); !bytes.Equal(got, []byte{0, 0, 0, 0}) {
			t.Fatalf("frame = %v, want zero-length header", got)
		}
	})

	t.Run("one MiB exact", func(t *testing.T) {
		payload := bytes.Repeat([]byte{0x5a}, maxFrameBytes)
		var output bytes.Buffer
		if err := writeFramePayload(&output, payload); err != nil {
			t.Fatalf("writeFramePayload() error = %v", err)
		}
		frame := output.Bytes()
		if got := binary.BigEndian.Uint32(frame[:4]); got != maxFrameBytes {
			t.Fatalf("declared length = %d, want %d", got, maxFrameBytes)
		}
		if !bytes.Equal(frame[4:], payload) {
			t.Fatal("payload changed while framing")
		}
	})

	t.Run("one MiB one-over writes nothing", func(t *testing.T) {
		payload := make([]byte, maxFrameBytes+1)
		var output bytes.Buffer
		err := writeFramePayload(&output, payload)
		assertInternalFrameErrorCode(t, err, FrameErrorCodeFrameTooLarge)
		if output.Len() != 0 {
			t.Fatalf("writer received %d bytes before oversize rejection", output.Len())
		}
	})
}

func assertInternalFrameErrorCode(t *testing.T, err error, want FrameErrorCode) {
	t.Helper()
	var frameError *FrameError
	if !errors.As(err, &frameError) {
		t.Fatalf("error type = %T, want *FrameError", err)
	}
	if got := frameError.Code(); got != want {
		t.Fatalf("frame error code = %q, want %q", got, want)
	}
}
