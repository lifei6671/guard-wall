package ipc_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestMutationRequestFrameGoldenVectorsAndRoundTrip(t *testing.T) {
	for _, path := range []string{
		"valid/apply-infrastructure.json",
		"valid/apply-policy.json",
		"valid/apply-target.json",
		"valid/remove-managed-infrastructure.json",
	} {
		path := path
		t.Run(strings.TrimSuffix(path, ".json"), func(t *testing.T) {
			canonical := compactMutationRequestGolden(t, path)
			decoded, err := ipc.DecodeRequest(canonical)
			if err != nil {
				t.Fatalf("DecodeRequest(golden): %v", err)
			}
			request, ok := decoded.(ipc.MutationRequest)
			if !ok {
				t.Fatalf("golden request type = %T, want ipc.MutationRequest", decoded)
			}

			writer := &mutationRequestFrameLifecycleWriter{}
			if err := ipc.WriteMutationRequestFrame(writer, request); err != nil {
				t.Fatalf("WriteMutationRequestFrame(): %v", err)
			}
			wantFrame := mutationRequestFrameBytes(canonical)
			if !bytes.Equal(writer.Bytes(), wantFrame) {
				t.Fatalf("request frame = %x, want exact golden frame %x", writer.Bytes(), wantFrame)
			}
			if got := binary.BigEndian.Uint32(writer.Bytes()[:4]); got != uint32(len(canonical)) {
				t.Fatalf("request frame length = %d, want %d", got, len(canonical))
			}
			assertMutationRequestFrameLifecycleUntouched(t, writer)

			roundTrip, err := ipc.DecodeFrame(bytes.NewReader(writer.Bytes()))
			if err != nil {
				t.Fatalf("DecodeFrame(written frame): %v", err)
			}
			roundTripMutation, ok := roundTrip.(ipc.MutationRequest)
			if !ok {
				t.Fatalf("round-trip request type = %T, want ipc.MutationRequest", roundTrip)
			}
			assertMutationRequestFields(t, path, roundTripMutation)
			reencoded, err := ipc.EncodeMutationRequest(roundTripMutation)
			if err != nil {
				t.Fatalf("EncodeMutationRequest(round trip): %v", err)
			}
			if !bytes.Equal(reencoded, canonical) {
				t.Fatalf("round-trip payload = %s, want exact golden %s", reencoded, canonical)
			}

			for attempt := 0; attempt < 32; attempt++ {
				var output bytes.Buffer
				if err := ipc.WriteMutationRequestFrame(&output, request); err != nil {
					t.Fatalf("deterministic frame %d: %v", attempt, err)
				}
				if !bytes.Equal(output.Bytes(), wantFrame) {
					t.Fatalf("frame %d = %x, want deterministic %x", attempt, output.Bytes(), wantFrame)
				}
			}
		})
	}
}

func TestMutationRequestFrameEncodeFailuresPrecedeWriterAndWriteNothing(t *testing.T) {
	var mutation ipc.MutationRequest
	var apply ipc.ApplyManagedPlanRequest
	var remove ipc.RemoveManagedInfrastructureRequest
	concreteApply, err := ipc.NewApplyInfrastructureRequest(strings.Repeat("a", 64), 1)
	if err != nil {
		t.Fatalf("NewApplyInfrastructureRequest(): %v", err)
	}
	concreteApplyNil := reflect.Zero(reflect.TypeOf(concreteApply)).Interface().(ipc.MutationRequest)
	concreteRemove := ipc.NewRemoveManagedInfrastructureRequest()
	concreteRemoveNil := reflect.Zero(reflect.TypeOf(concreteRemove)).Interface().(ipc.MutationRequest)

	for _, test := range []struct {
		name    string
		request ipc.MutationRequest
	}{
		{name: "untyped mutation nil", request: mutation},
		{name: "nil apply interface", request: apply},
		{name: "nil remove interface", request: remove},
		{name: "typed concrete apply nil", request: concreteApplyNil},
		{name: "typed concrete remove nil", request: concreteRemoveNil},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &mutationRequestFrameLifecycleWriter{}
			writeErr := ipc.WriteMutationRequestFrame(writer, test.request)
			assertMutationRequestValidationError(t, writeErr, ipc.ErrorCodeSchemaRejected)
			if writer.Len() != 0 || writer.writeCalls != 0 {
				t.Fatalf("invalid request wrote %d bytes in %d calls, want zero writes", writer.Len(), writer.writeCalls)
			}
			assertMutationRequestFrameLifecycleUntouched(t, writer)
		})
	}

	t.Run("validation precedes nil writer", func(t *testing.T) {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("WriteMutationRequestFrame(nil, nil) panicked: %v", recovered)
			}
		}()
		var writer io.Writer
		var request ipc.MutationRequest
		err := ipc.WriteMutationRequestFrame(writer, request)
		assertMutationRequestValidationError(t, err, ipc.ErrorCodeSchemaRejected)
	})
}

func TestMutationRequestFrameValidRequestRejectsNilWriter(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("WriteMutationRequestFrame(nil writer) panicked: %v", recovered)
		}
	}()
	var writer io.Writer
	err := ipc.WriteMutationRequestFrame(writer, ipc.NewRemoveManagedInfrastructureRequest())
	assertMutationRequestFrameWriteError(t, err, "")
}

func TestMutationRequestFrameCompletesHeaderAndPayloadShortWrites(t *testing.T) {
	request := ipc.NewRemoveManagedInfrastructureRequest()
	payload, err := ipc.EncodeMutationRequest(request)
	if err != nil {
		t.Fatalf("EncodeMutationRequest(): %v", err)
	}
	writer := &shortMutationRequestFrameWriter{limit: 1}
	if err := ipc.WriteMutationRequestFrame(writer, request); err != nil {
		t.Fatalf("WriteMutationRequestFrame(): %v", err)
	}
	wantFrame := mutationRequestFrameBytes(payload)
	if !bytes.Equal(writer.Bytes(), wantFrame) {
		t.Fatalf("short-write frame = %x, want %x", writer.Bytes(), wantFrame)
	}
	if writer.writeCalls <= len(wantFrame)/2 {
		t.Fatalf("short writer calls = %d, want repeated header and payload writes", writer.writeCalls)
	}
	assertMutationRequestFrameLifecycleUntouched(t, &writer.mutationRequestFrameLifecycleWriter)
}

func TestMutationRequestFrameRejectsZeroProgress(t *testing.T) {
	writer := &zeroProgressMutationRequestFrameWriter{}
	err := ipc.WriteMutationRequestFrame(writer, ipc.NewRemoveManagedInfrastructureRequest())
	assertMutationRequestFrameWriteError(t, err, "")
	if writer.writeCalls != 1 {
		t.Fatalf("zero-progress writer calls = %d, want 1", writer.writeCalls)
	}
	assertMutationRequestFrameLifecycleUntouched(t, &writer.mutationRequestFrameLifecycleWriter)
}

func TestMutationRequestFrameRejectsInvalidWriteCountsWithoutPanic(t *testing.T) {
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
					t.Fatalf("WriteMutationRequestFrame() panicked: %v", recovered)
				}
			}()
			writer := &invalidCountMutationRequestFrameWriter{count: test.count}
			err := ipc.WriteMutationRequestFrame(writer, ipc.NewRemoveManagedInfrastructureRequest())
			assertMutationRequestFrameWriteError(t, err, "")
			if writer.writeCalls != 1 {
				t.Fatalf("invalid-count writer calls = %d, want 1", writer.writeCalls)
			}
			assertMutationRequestFrameLifecycleUntouched(t, &writer.mutationRequestFrameLifecycleWriter)
		})
	}
}

func TestMutationRequestFrameWriteFailuresAreTerminalSanitizedAndSingleFrame(t *testing.T) {
	const marker = "request-frame-writer-secret-2d46f5a1"
	request, err := ipc.NewApplyPolicyRequest(
		strings.Repeat("b", 64),
		1,
		nil,
		[]netip.Prefix{
			netip.MustParsePrefix("127.0.0.0/8"),
			netip.MustParsePrefix("::1/128"),
		},
	)
	if err != nil {
		t.Fatalf("NewApplyPolicyRequest(): %v", err)
	}
	payload, err := ipc.EncodeMutationRequest(request)
	if err != nil {
		t.Fatalf("EncodeMutationRequest(): %v", err)
	}
	wantFrame := mutationRequestFrameBytes(payload)

	for _, test := range []struct {
		name  string
		allow int
	}{
		{name: "before header", allow: 0},
		{name: "partial header with error", allow: 2},
		{name: "before payload", allow: 4},
		{name: "partial payload with error", allow: 4 + 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &terminalFailureMutationRequestFrameWriter{
				allow:   test.allow,
				failure: errors.New(marker),
			}
			writeErr := ipc.WriteMutationRequestFrame(writer, request)
			assertMutationRequestFrameWriteError(t, writeErr, marker)
			if !bytes.Equal(writer.Bytes(), wantFrame[:test.allow]) {
				t.Fatalf("bytes before terminal failure = %x, want exact frame prefix %x", writer.Bytes(), wantFrame[:test.allow])
			}
			if writer.callsAfterFailure != 0 {
				t.Fatalf("writes after terminal failure = %d, want 0", writer.callsAfterFailure)
			}
			assertMutationRequestFrameLifecycleUntouched(t, &writer.mutationRequestFrameLifecycleWriter)
		})
	}
}

func assertMutationRequestFrameWriteError(t *testing.T, err error, forbidden string) {
	t.Helper()
	if err == nil {
		t.Fatal("frame write error = nil")
	}
	var frameError *ipc.FrameError
	if !errors.As(err, &frameError) {
		t.Fatalf("frame write error type = %T, want *ipc.FrameError", err)
	}
	if got := frameError.Code(); got != ipc.FrameErrorCodeWriteFailed {
		t.Fatalf("frame write error code = %q, want %q", got, ipc.FrameErrorCodeWriteFailed)
	}
	if forbidden != "" && strings.Contains(err.Error(), forbidden) {
		t.Fatalf("frame write error echoed stream error: %q", err)
	}
}

func assertMutationRequestFrameLifecycleUntouched(t *testing.T, writer *mutationRequestFrameLifecycleWriter) {
	t.Helper()
	if writer.closeCalls != 0 || writer.flushCalls != 0 || writer.deadlineCalls != 0 {
		t.Fatalf(
			"writer lifecycle calls = close %d, flush %d, deadline %d; want all zero",
			writer.closeCalls,
			writer.flushCalls,
			writer.deadlineCalls,
		)
	}
}

func mutationRequestFrameBytes(payload []byte) []byte {
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	return frame
}

type mutationRequestFrameLifecycleWriter struct {
	bytes.Buffer
	writeCalls    int
	closeCalls    int
	flushCalls    int
	deadlineCalls int
}

func (w *mutationRequestFrameLifecycleWriter) Write(contents []byte) (int, error) {
	w.writeCalls++
	return w.Buffer.Write(contents)
}

func (w *mutationRequestFrameLifecycleWriter) Close() error {
	w.closeCalls++
	return nil
}

func (w *mutationRequestFrameLifecycleWriter) Flush() error {
	w.flushCalls++
	return nil
}

func (w *mutationRequestFrameLifecycleWriter) SetDeadline(time.Time) error {
	w.deadlineCalls++
	return nil
}

type shortMutationRequestFrameWriter struct {
	mutationRequestFrameLifecycleWriter
	limit int
}

func (w *shortMutationRequestFrameWriter) Write(contents []byte) (int, error) {
	if len(contents) > w.limit {
		contents = contents[:w.limit]
	}
	return w.mutationRequestFrameLifecycleWriter.Write(contents)
}

type zeroProgressMutationRequestFrameWriter struct {
	mutationRequestFrameLifecycleWriter
}

func (w *zeroProgressMutationRequestFrameWriter) Write([]byte) (int, error) {
	w.writeCalls++
	if w.writeCalls == 1 {
		return 0, nil
	}
	return 0, errors.New("write retried after zero progress")
}

type invalidCountMutationRequestFrameWriter struct {
	mutationRequestFrameLifecycleWriter
	count func(int) int
}

func (w *invalidCountMutationRequestFrameWriter) Write(contents []byte) (int, error) {
	w.writeCalls++
	return w.count(len(contents)), nil
}

type terminalFailureMutationRequestFrameWriter struct {
	mutationRequestFrameLifecycleWriter
	allow             int
	failure           error
	failed            bool
	callsAfterFailure int
}

func (w *terminalFailureMutationRequestFrameWriter) Write(contents []byte) (int, error) {
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
		written, _ := w.mutationRequestFrameLifecycleWriter.Write(contents[:remaining])
		w.failed = true
		return written, w.failure
	}
	return w.mutationRequestFrameLifecycleWriter.Write(contents)
}

var _ io.Writer = (*mutationRequestFrameLifecycleWriter)(nil)
