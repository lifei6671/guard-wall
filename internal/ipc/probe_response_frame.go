package ipc

import (
	"encoding/binary"
	"io"
)

// DecodeProbeCapabilitiesResponseFrame reads and decodes one uint32-be
// length-prefixed Probe response. The caller must discard the stream after any
// returned error.
func DecodeProbeCapabilitiesResponseFrame(reader io.Reader) (ProbeCapabilitiesResponse, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, &FrameError{code: FrameErrorCodeTruncatedLength}
	}

	payloadLength := binary.BigEndian.Uint32(header[:])
	if payloadLength > maxFrameBytes {
		return nil, &FrameError{code: FrameErrorCodeFrameTooLarge}
	}
	if payloadLength > maxProbeCapabilitiesResponseBytes {
		return nil, probeCapabilitiesResponseError(ProbeCapabilitiesResponseErrorCodeResponseTooLarge)
	}

	payload := make([]byte, int(payloadLength))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, &FrameError{code: FrameErrorCodeTruncatedPayload}
	}
	return DecodeProbeCapabilitiesResponse(payload)
}

// WriteProbeCapabilitiesResponseFrame encodes and writes one uint32-be
// length-prefixed Probe response. Encoding completes before the first write.
// The function never closes writer; after a write error the caller must discard
// the stream.
func WriteProbeCapabilitiesResponseFrame(
	writer io.Writer,
	response ProbeCapabilitiesResponse,
) error {
	payload, err := EncodeProbeCapabilitiesResponse(response)
	if err != nil {
		return err
	}
	return writeFramePayload(writer, payload)
}
