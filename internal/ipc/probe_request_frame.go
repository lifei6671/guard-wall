package ipc

import "io"

// WriteProbeCapabilitiesRequestFrame encodes and writes one uint32-be
// length-prefixed capability probe request. Encoding completes before the
// first write. The function never closes writer and never retries a frame.
func WriteProbeCapabilitiesRequestFrame(writer io.Writer, request ProbeCapabilitiesRequest) error {
	payload, err := EncodeProbeCapabilitiesRequest(request)
	if err != nil {
		return err
	}
	return writeFramePayload(writer, payload)
}
