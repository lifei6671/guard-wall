package ipc

import (
	"encoding/binary"
	"io"
)

// DecodeSnapshotManagedResponseFrame reads and decodes one uint32-be
// length-prefixed Snapshot response. The caller must discard the stream after
// any returned error.
func DecodeSnapshotManagedResponseFrame(reader io.Reader) (SnapshotManagedResponse, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, &FrameError{code: FrameErrorCodeTruncatedLength}
	}

	payloadLength := binary.BigEndian.Uint32(header[:])
	if payloadLength > maxFrameBytes {
		return nil, &FrameError{code: FrameErrorCodeFrameTooLarge}
	}
	if payloadLength > maxSnapshotManagedResponseBytes {
		return nil, snapshotManagedResponseError(SnapshotManagedResponseErrorCodeResponseTooLarge)
	}

	payload := make([]byte, int(payloadLength))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, &FrameError{code: FrameErrorCodeTruncatedPayload}
	}
	return DecodeSnapshotManagedResponse(payload)
}

// WriteSnapshotManagedResponseFrame encodes and writes one uint32-be
// length-prefixed Snapshot response. Encoding completes before the first
// write. The function never closes writer.
func WriteSnapshotManagedResponseFrame(writer io.Writer, response SnapshotManagedResponse) error {
	payload, err := EncodeSnapshotManagedResponse(response)
	if err != nil {
		return err
	}
	return writeFramePayload(writer, payload)
}
