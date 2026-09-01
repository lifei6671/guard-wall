package ipc

import "io"

// WriteMutationRequestFrame encodes and writes one uint32-be length-prefixed
// mutation request. Encoding completes before the first write. The function
// never closes writer; after any returned write error, the caller must discard
// the stream.
func WriteMutationRequestFrame(writer io.Writer, request MutationRequest) error {
	payload, err := EncodeMutationRequest(request)
	if err != nil {
		return err
	}
	return writeFramePayload(writer, payload)
}
