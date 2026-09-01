package ipc

import (
	"encoding/binary"
	"io"
)

const maxFrameBytes = 1 << 20

// FrameErrorCode classifies an IPC frame boundary failure.
type FrameErrorCode string

const (
	FrameErrorCodeTruncatedLength  FrameErrorCode = "truncated_length"
	FrameErrorCodeFrameTooLarge    FrameErrorCode = "frame_too_large"
	FrameErrorCodeTruncatedPayload FrameErrorCode = "truncated_payload"
)

// FrameError reports only a stable framing classification. It never includes
// bytes or errors supplied by the stream.
type FrameError struct {
	code FrameErrorCode
}

func (e *FrameError) Error() string {
	return "ipc frame rejected: " + string(e.code)
}

// Code returns the framing failure classification.
func (e *FrameError) Code() FrameErrorCode {
	return e.code
}

// DecodeFrame reads and decodes one uint32-be length-prefixed IPC request.
// The caller must discard the stream after any returned error.
func DecodeFrame(reader io.Reader) (Request, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, &FrameError{code: FrameErrorCodeTruncatedLength}
	}

	payloadLength := binary.BigEndian.Uint32(header[:])
	if payloadLength > maxFrameBytes {
		return nil, &FrameError{code: FrameErrorCodeFrameTooLarge}
	}
	if payloadLength > maxRequestBytes {
		return nil, validationError(ErrorCodeRequestTooLarge)
	}

	payload := make([]byte, int(payloadLength))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, &FrameError{code: FrameErrorCodeTruncatedPayload}
	}
	return DecodeRequest(payload)
}
