package ipc

import (
	"encoding/binary"
	"io"
)

// FrameErrorCodeWriteFailed classifies an IPC frame write failure.
const FrameErrorCodeWriteFailed FrameErrorCode = "write_failed"

// DecodeMutationResponseFrame reads and decodes one uint32-be length-prefixed
// mutation response. The caller must discard the stream after any returned
// error.
func DecodeMutationResponseFrame(reader io.Reader) (MutationResponse, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, &FrameError{code: FrameErrorCodeTruncatedLength}
	}

	payloadLength := binary.BigEndian.Uint32(header[:])
	if payloadLength > maxFrameBytes {
		return nil, &FrameError{code: FrameErrorCodeFrameTooLarge}
	}
	if payloadLength > maxMutationResponseBytes {
		return nil, mutationResponseError(MutationResponseErrorCodeResponseTooLarge)
	}

	payload := make([]byte, int(payloadLength))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, &FrameError{code: FrameErrorCodeTruncatedPayload}
	}
	return DecodeMutationResponse(payload)
}

// WriteMutationResponseFrame encodes and writes one uint32-be length-prefixed
// mutation response. Encoding completes before the first write. The function
// never closes writer; after any returned write error, the caller must discard
// the stream.
func WriteMutationResponseFrame(writer io.Writer, response MutationResponse) error {
	payload, err := EncodeMutationResponse(response)
	if err != nil {
		return err
	}
	return writeFramePayload(writer, payload)
}

func writeFramePayload(writer io.Writer, payload []byte) error {
	if len(payload) > maxFrameBytes {
		return &FrameError{code: FrameErrorCodeFrameTooLarge}
	}
	if writer == nil {
		return &FrameError{code: FrameErrorCodeWriteFailed}
	}

	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if !writeAll(writer, header[:]) || !writeAll(writer, payload) {
		return &FrameError{code: FrameErrorCodeWriteFailed}
	}
	return nil
}

func writeAll(writer io.Writer, remaining []byte) bool {
	for len(remaining) > 0 {
		written, err := writer.Write(remaining)
		if written < 0 || written > len(remaining) {
			return false
		}
		remaining = remaining[written:]
		if err != nil || written == 0 {
			return false
		}
	}
	return true
}
