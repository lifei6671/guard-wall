package ipc_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/ipc"
)

const mutationResponseFrameMaxBytes = 1 << 20

func TestMutationResponseFrameGoldenVectors(t *testing.T) {
	cases := readMutationResponseCases(t)
	if got, want := len(cases.Valid), 12; got != want {
		t.Fatalf("valid mutation response cases = %d, want %d", got, want)
	}

	for _, test := range cases.Valid {
		test := test
		t.Run(strings.TrimSuffix(test.Path, ".json"), func(t *testing.T) {
			canonical := bytes.TrimSpace(readMutationResponseFixture(t, test))
			var wantWire mutationResponseWire
			if err := json.Unmarshal(canonical, &wantWire); err != nil {
				t.Fatalf("decode valid fixture expectation: %v", err)
			}
			response, err := ipc.DecodeMutationResponse(canonical)
			if err != nil {
				t.Fatalf("DecodeMutationResponse(): %v", err)
			}

			var output bytes.Buffer
			if err := ipc.WriteMutationResponseFrame(&output, response); err != nil {
				t.Fatalf("WriteMutationResponseFrame(): %v", err)
			}
			wantFrame := mutationResponseFrameBytes(canonical)
			if !bytes.Equal(output.Bytes(), wantFrame) {
				t.Fatalf("response frame = %x, want %x", output.Bytes(), wantFrame)
			}
			if got := binary.BigEndian.Uint32(output.Bytes()[:4]); got != uint32(len(canonical)) {
				t.Fatalf("response frame length = %d, want %d", got, len(canonical))
			}

			decoded, err := ipc.DecodeMutationResponseFrame(bytes.NewReader(output.Bytes()))
			if err != nil {
				t.Fatalf("DecodeMutationResponseFrame(): %v", err)
			}
			assertMutationResponseGetters(t, decoded, wantWire)
			reencoded, err := ipc.EncodeMutationResponse(decoded)
			if err != nil {
				t.Fatalf("EncodeMutationResponse(decoded): %v", err)
			}
			if !bytes.Equal(reencoded, canonical) {
				t.Fatalf("decoded canonical response = %s, want %s", reencoded, canonical)
			}
		})
	}
}

func TestMutationResponseFrameInvalidPayloadClassifications(t *testing.T) {
	cases := readMutationResponseCases(t)
	if got, want := len(cases.Invalid), 28; got != want {
		t.Fatalf("invalid mutation response cases = %d, want %d", got, want)
	}

	for _, test := range cases.Invalid {
		test := test
		t.Run(strings.TrimSuffix(test.Path, ".json"), func(t *testing.T) {
			raw := readMutationResponseFixture(t, test)
			response, err := ipc.DecodeMutationResponseFrame(bytes.NewReader(mutationResponseFrameBytes(raw)))
			if response != nil {
				t.Fatalf("DecodeMutationResponseFrame() response = %#v, want nil", response)
			}
			assertMutationResponseErrorCode(
				t, err, ipc.MutationResponseErrorCode(test.Classification),
			)
		})
	}
}

func TestMutationResponseFrameTruncation(t *testing.T) {
	t.Run("length", func(t *testing.T) {
		for size := 0; size < 4; size++ {
			t.Run(string(rune('0'+size))+" bytes", func(t *testing.T) {
				response, err := ipc.DecodeMutationResponseFrame(bytes.NewReader(make([]byte, size)))
				if response != nil {
					t.Fatalf("DecodeMutationResponseFrame() response = %#v, want nil", response)
				}
				assertFrameErrorCode(t, err, ipc.FrameErrorCodeTruncatedLength)
			})
		}
	})

	t.Run("payload", func(t *testing.T) {
		payload := bytes.TrimSpace(readMutationResponseGoldenFile(t, "valid/remove-confirmed.json"))
		truncated := mutationResponseFrameBytes(payload)
		truncated = truncated[:len(truncated)-1]
		response, err := ipc.DecodeMutationResponseFrame(bytes.NewReader(truncated))
		if response != nil {
			t.Fatalf("DecodeMutationResponseFrame() response = %#v, want nil", response)
		}
		assertFrameErrorCode(t, err, ipc.FrameErrorCodeTruncatedPayload)
	})
}

func TestMutationResponseFrameReaderErrorsAreSanitized(t *testing.T) {
	const marker = "reader-secret-a6e07d2b"
	for _, test := range []struct {
		name   string
		reader io.Reader
		code   ipc.FrameErrorCode
	}{
		{
			name:   "length",
			reader: &mutationResponseErrorReader{err: errors.New(marker)},
			code:   ipc.FrameErrorCodeTruncatedLength,
		},
		{
			name: "payload",
			reader: io.MultiReader(
				bytes.NewReader(mutationResponseFrameHeader(1)),
				&mutationResponseErrorReader{err: errors.New(marker)},
			),
			code: ipc.FrameErrorCodeTruncatedPayload,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := ipc.DecodeMutationResponseFrame(test.reader)
			if response != nil {
				t.Fatalf("DecodeMutationResponseFrame() response = %#v, want nil", response)
			}
			assertFrameErrorCode(t, err, test.code)
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("frame reader error echoed stream error: %q", err)
			}
		})
	}
}

func TestMutationResponseFrameResourceLimits(t *testing.T) {
	base := bytes.TrimSpace(readMutationResponseGoldenFile(t, "valid/remove-confirmed.json"))
	exactResponseLimit := append(append([]byte(nil), base...), bytes.Repeat(
		[]byte(" "), 4*1024-len(base),
	)...)
	response, err := ipc.DecodeMutationResponseFrame(bytes.NewReader(
		mutationResponseFrameBytes(exactResponseLimit),
	))
	if err != nil || response == nil {
		t.Fatalf("exact response limit = (%T, %v), want success", response, err)
	}

	for _, test := range []struct {
		name       string
		length     int
		assertCode func(*testing.T, error)
	}{
		{
			name:   "response one-over",
			length: 4*1024 + 1,
			assertCode: func(t *testing.T, err error) {
				assertMutationResponseErrorCode(t, err, ipc.MutationResponseErrorCodeResponseTooLarge)
			},
		},
		{
			name:   "frame exact reaches response cap",
			length: mutationResponseFrameMaxBytes,
			assertCode: func(t *testing.T, err error) {
				assertMutationResponseErrorCode(t, err, ipc.MutationResponseErrorCodeResponseTooLarge)
			},
		},
		{
			name:   "frame one-over reaches frame cap",
			length: mutationResponseFrameMaxBytes + 1,
			assertCode: func(t *testing.T, err error) {
				assertFrameErrorCode(t, err, ipc.FrameErrorCodeFrameTooLarge)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := newMutationResponseHeaderOnlyReader(test.length)
			response, err := ipc.DecodeMutationResponseFrame(reader)
			if response != nil {
				t.Fatalf("DecodeMutationResponseFrame() response = %#v, want nil", response)
			}
			test.assertCode(t, err)
			if reader.payloadRead {
				t.Fatalf("DecodeMutationResponseFrame() read payload for declared length %d", test.length)
			}
		})
	}
}

func TestMutationResponseFrameReadsConsecutiveFrames(t *testing.T) {
	firstPayload := bytes.TrimSpace(readMutationResponseGoldenFile(t, "valid/apply-target-confirmed.json"))
	secondPayload := bytes.TrimSpace(readMutationResponseGoldenFile(t, "valid/remove-unknown.json"))
	reader := bytes.NewReader(append(
		mutationResponseFrameBytes(firstPayload), mutationResponseFrameBytes(secondPayload)...,
	))

	first, err := ipc.DecodeMutationResponseFrame(reader)
	if err != nil {
		t.Fatalf("first DecodeMutationResponseFrame(): %v", err)
	}
	if first.Operation() != ipc.OperationApplyManagedPlan || first.Status() != ipc.MutationStatusConfirmed {
		t.Fatalf("first response = %q/%q, want ApplyManagedPlan/confirmed", first.Operation(), first.Status())
	}

	second, err := ipc.DecodeMutationResponseFrame(reader)
	if err != nil {
		t.Fatalf("second DecodeMutationResponseFrame(): %v", err)
	}
	if second.Operation() != ipc.OperationRemoveManagedInfrastructure || second.Status() != ipc.MutationStatusUnknown {
		t.Fatalf("second response = %q/%q, want RemoveManagedInfrastructure/unknown", second.Operation(), second.Status())
	}
	if reader.Len() != 0 {
		t.Fatalf("reader has %d trailing bytes, want 0", reader.Len())
	}
}

func TestMutationResponseFrameWriterRejectsNilWithoutWritingOrClosing(t *testing.T) {
	var untyped ipc.MutationResponse
	var typedApply ipc.ApplyManagedPlanResponse
	var typedRemove ipc.RemoveManagedInfrastructureResponse
	apply, err := ipc.NewApplyManagedPlanConfirmedResponse(ipc.DomainTarget)
	if err != nil {
		t.Fatalf("NewApplyManagedPlanConfirmedResponse(): %v", err)
	}
	concreteApplyNil := reflect.Zero(reflect.TypeOf(apply)).Interface().(ipc.MutationResponse)
	remove := ipc.NewRemoveManagedInfrastructureConfirmedResponse()
	concreteRemoveNil := reflect.Zero(reflect.TypeOf(remove)).Interface().(ipc.MutationResponse)

	for _, test := range []struct {
		name     string
		response ipc.MutationResponse
	}{
		{name: "untyped nil", response: untyped},
		{name: "nil apply interface", response: typedApply},
		{name: "nil remove interface", response: typedRemove},
		{name: "typed concrete apply nil", response: concreteApplyNil},
		{name: "typed concrete remove nil", response: concreteRemoveNil},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &closeTrackingBuffer{}
			writeErr := ipc.WriteMutationResponseFrame(writer, test.response)
			assertMutationResponseErrorCode(
				t, writeErr, ipc.MutationResponseErrorCodeSchemaRejected,
			)
			if writer.Len() != 0 || writer.writeCalls != 0 {
				t.Fatalf("nil response wrote %d bytes in %d calls, want zero writes", writer.Len(), writer.writeCalls)
			}
			if writer.closeCalls != 0 {
				t.Fatalf("WriteMutationResponseFrame() Close calls = %d, want 0", writer.closeCalls)
			}
		})
	}
}

func TestMutationResponseFrameWriterCompletesPositiveShortWritesWithoutClosing(t *testing.T) {
	response := ipc.NewRemoveManagedInfrastructureUnknownResponse()
	payload, err := ipc.EncodeMutationResponse(response)
	if err != nil {
		t.Fatalf("EncodeMutationResponse(): %v", err)
	}
	writer := &shortMutationResponseWriter{limit: 3}
	if err := ipc.WriteMutationResponseFrame(writer, response); err != nil {
		t.Fatalf("WriteMutationResponseFrame(): %v", err)
	}
	want := mutationResponseFrameBytes(payload)
	if !bytes.Equal(writer.Bytes(), want) {
		t.Fatalf("short-write frame = %x, want %x", writer.Bytes(), want)
	}
	if writer.writeCalls <= 1 {
		t.Fatalf("short writer calls = %d, want multiple calls", writer.writeCalls)
	}
	if writer.closeCalls != 0 {
		t.Fatalf("WriteMutationResponseFrame() Close calls = %d, want 0", writer.closeCalls)
	}
}

func TestMutationResponseFrameWriterRejectsZeroProgress(t *testing.T) {
	writer := &zeroProgressMutationResponseWriter{}
	err := ipc.WriteMutationResponseFrame(writer, ipc.NewRemoveManagedInfrastructureConfirmedResponse())
	assertMutationResponseFrameWriteError(t, err, "")
	if writer.writeCalls != 1 {
		t.Fatalf("zero-progress writer calls = %d, want 1", writer.writeCalls)
	}
	if writer.closeCalls != 0 {
		t.Fatalf("WriteMutationResponseFrame() Close calls = %d, want 0", writer.closeCalls)
	}
}

func TestMutationResponseFrameWriterRejectsNilWriterWithoutPanic(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("WriteMutationResponseFrame(nil writer) panicked: %v", recovered)
		}
	}()
	var writer io.Writer
	err := ipc.WriteMutationResponseFrame(writer, ipc.NewRemoveManagedInfrastructureConfirmedResponse())
	assertMutationResponseFrameWriteError(t, err, "")
}

func TestMutationResponseFrameWriterRejectsInvalidWriteCountsWithoutPanic(t *testing.T) {
	for _, test := range []struct {
		name  string
		count func(int) int
	}{
		{name: "negative", count: func(int) int { return -1 }},
		{name: "overlong", count: func(length int) int { return length + 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("WriteMutationResponseFrame() panicked: %v", recovered)
				}
			}()
			writer := &invalidCountMutationResponseWriter{count: test.count}
			err := ipc.WriteMutationResponseFrame(writer, ipc.NewRemoveManagedInfrastructureConfirmedResponse())
			assertMutationResponseFrameWriteError(t, err, "")
			if writer.writeCalls != 1 {
				t.Fatalf("invalid-count writer calls = %d, want 1", writer.writeCalls)
			}
		})
	}
}

func TestMutationResponseFrameWriterFailuresAreTerminalSanitizedAndNotClosed(t *testing.T) {
	const marker = "writer-secret-4a8e13f5"
	response, err := ipc.NewApplyManagedPlanRejectedResponse(
		ipc.DomainPolicy, ipc.MutationErrorCodeBackendRejected,
	)
	if err != nil {
		t.Fatalf("NewApplyManagedPlanRejectedResponse(): %v", err)
	}
	payload, err := ipc.EncodeMutationResponse(response)
	if err != nil {
		t.Fatalf("EncodeMutationResponse(): %v", err)
	}
	wantFrame := mutationResponseFrameBytes(payload)

	for _, test := range []struct {
		name  string
		allow int
	}{
		{name: "header", allow: 0},
		{name: "payload", allow: 4 + 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &terminalFailureMutationResponseWriter{
				allow:   test.allow,
				failure: errors.New(marker),
			}
			writeErr := ipc.WriteMutationResponseFrame(writer, response)
			assertMutationResponseFrameWriteError(t, writeErr, marker)
			if !bytes.Equal(writer.Bytes(), wantFrame[:test.allow]) {
				t.Fatalf("bytes before %s failure = %x, want exact frame prefix %x", test.name, writer.Bytes(), wantFrame[:test.allow])
			}
			if writer.callsAfterFailure != 0 {
				t.Fatalf("writes after terminal failure = %d, want 0", writer.callsAfterFailure)
			}
			if writer.closeCalls != 0 {
				t.Fatalf("WriteMutationResponseFrame() Close calls = %d, want 0", writer.closeCalls)
			}
		})
	}
}

func assertMutationResponseFrameWriteError(t *testing.T, err error, forbidden string) {
	t.Helper()
	if err == nil {
		t.Fatal("frame write error = nil")
	}
	var frameError *ipc.FrameError
	if !errors.As(err, &frameError) {
		t.Fatalf("frame write error type = %T, want *ipc.FrameError", err)
	}
	if got, want := string(frameError.Code()), "write_failed"; got != want {
		t.Fatalf("frame write error code = %q, want %q", got, want)
	}
	if forbidden != "" && strings.Contains(err.Error(), forbidden) {
		t.Fatalf("frame write error echoed stream error: %q", err)
	}
}

func mutationResponseFrameBytes(payload []byte) []byte {
	framed := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(framed[:4], uint32(len(payload)))
	copy(framed[4:], payload)
	return framed
}

func mutationResponseFrameHeader(length int) []byte {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(length))
	return header
}

type mutationResponseHeaderOnlyReader struct {
	header      [4]byte
	offset      int
	payloadRead bool
}

type mutationResponseErrorReader struct {
	err error
}

func (r *mutationResponseErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func newMutationResponseHeaderOnlyReader(length int) *mutationResponseHeaderOnlyReader {
	reader := &mutationResponseHeaderOnlyReader{}
	binary.BigEndian.PutUint32(reader.header[:], uint32(length))
	return reader
}

func (r *mutationResponseHeaderOnlyReader) Read(contents []byte) (int, error) {
	if r.offset < len(r.header) {
		count := copy(contents, r.header[r.offset:])
		r.offset += count
		return count, nil
	}
	r.payloadRead = true
	return 0, errors.New("unexpected payload read")
}

type closeTrackingBuffer struct {
	bytes.Buffer
	writeCalls int
	closeCalls int
}

func (w *closeTrackingBuffer) Write(contents []byte) (int, error) {
	w.writeCalls++
	return w.Buffer.Write(contents)
}

func (w *closeTrackingBuffer) Close() error {
	w.closeCalls++
	return nil
}

type shortMutationResponseWriter struct {
	closeTrackingBuffer
	limit int
}

func (w *shortMutationResponseWriter) Write(contents []byte) (int, error) {
	if len(contents) > w.limit {
		contents = contents[:w.limit]
	}
	return w.closeTrackingBuffer.Write(contents)
}

type zeroProgressMutationResponseWriter struct {
	writeCalls int
	closeCalls int
}

type invalidCountMutationResponseWriter struct {
	count      func(int) int
	writeCalls int
}

func (w *invalidCountMutationResponseWriter) Write(contents []byte) (int, error) {
	w.writeCalls++
	return w.count(len(contents)), nil
}

func (w *zeroProgressMutationResponseWriter) Write([]byte) (int, error) {
	w.writeCalls++
	if w.writeCalls == 1 {
		return 0, nil
	}
	return 0, errors.New("write retried after zero progress")
}

func (w *zeroProgressMutationResponseWriter) Close() error {
	w.closeCalls++
	return nil
}

type terminalFailureMutationResponseWriter struct {
	closeTrackingBuffer
	allow             int
	failure           error
	failed            bool
	callsAfterFailure int
}

func (w *terminalFailureMutationResponseWriter) Write(contents []byte) (int, error) {
	if w.failed {
		w.callsAfterFailure++
		return 0, errors.New("write attempted after terminal failure")
	}
	remaining := w.allow - w.Len()
	if remaining <= 0 {
		w.failed = true
		w.writeCalls++
		return 0, w.failure
	}
	if len(contents) > remaining {
		written, _ := w.closeTrackingBuffer.Write(contents[:remaining])
		w.failed = true
		return written, w.failure
	}
	return w.closeTrackingBuffer.Write(contents)
}

var (
	_ io.Reader = (*mutationResponseHeaderOnlyReader)(nil)
	_ io.Writer = (*closeTrackingBuffer)(nil)
)
