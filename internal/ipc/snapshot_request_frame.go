package ipc

import "io"

// WriteSnapshotManagedRequestFrame encodes and writes one uint32-be
// length-prefixed managed snapshot request. Encoding completes before the
// first write. The function never closes writer and never retries a frame.
func WriteSnapshotManagedRequestFrame(writer io.Writer, request SnapshotManagedRequest) error {
	payload, err := EncodeSnapshotManagedRequest(request)
	if err != nil {
		return err
	}
	return writeFramePayload(writer, payload)
}
