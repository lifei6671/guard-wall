//go:build linux

package ipc_test

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/ipc"
)

func TestPeerErrorCodeConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "credential unavailable", got: string(ipc.PeerErrorCodeCredentialUnavailable), want: "peer_credential_unavailable"},
		{name: "UID mismatch", got: string(ipc.PeerErrorCodeUIDMismatch), want: "peer_uid_mismatch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("constant = %q, want %q", test.got, test.want)
			}
		})
	}
}

func TestDecodeUnixFrameAcceptsSameUID(t *testing.T) {
	server, client := unixConnectionPair(t)
	writeUnixFrame(t, client, frame(readGoldenFile(t, "valid/probe-capabilities.json")))

	request, err := ipc.DecodeUnixFrame(server, uint32(os.Getuid()))
	if err != nil {
		t.Fatalf("DecodeUnixFrame() error = %v", err)
	}
	if request.Operation() != ipc.OperationProbeCapabilities {
		t.Fatalf("operation = %q, want %q", request.Operation(), ipc.OperationProbeCapabilities)
	}
}

func TestDecodeUnixFrameRejectsUIDBeforeReadingFrame(t *testing.T) {
	server, client := unixConnectionPair(t)
	writtenFrame := frame(readGoldenFile(t, "valid/snapshot-managed.json"))
	writeUnixFrame(t, client, writtenFrame)

	actualUID := uint32(os.Getuid())
	request, err := ipc.DecodeUnixFrame(server, differentUID(actualUID))
	if request != nil {
		t.Fatalf("DecodeUnixFrame() request = %#v, want nil", request)
	}
	assertPeerErrorCode(t, err, ipc.PeerErrorCodeUIDMismatch)

	request, err = ipc.DecodeFrame(server)
	if err != nil {
		t.Fatalf("DecodeFrame() after UID rejection error = %v", err)
	}
	if request.Operation() != ipc.OperationSnapshotManaged {
		t.Fatalf("operation after UID rejection = %q, want %q", request.Operation(), ipc.OperationSnapshotManaged)
	}
}

func TestDecodeUnixFrameClosedConnectionCredentialUnavailable(t *testing.T) {
	server, _ := unixConnectionPair(t)
	localAddress := server.LocalAddr().String()
	if err := server.Close(); err != nil {
		t.Fatalf("close accepted connection: %v", err)
	}

	request, err := ipc.DecodeUnixFrame(server, uint32(os.Getuid()))
	if request != nil {
		t.Fatalf("DecodeUnixFrame() request = %#v, want nil", request)
	}
	assertPeerErrorCode(t, err, ipc.PeerErrorCodeCredentialUnavailable)
	if strings.Contains(err.Error(), "closed network connection") || strings.Contains(err.Error(), localAddress) {
		t.Fatalf("credential error disclosed OS or socket details: %q", err)
	}
}

func TestDecodeUnixFrameMismatchErrorDoesNotDiscloseUIDs(t *testing.T) {
	server, _ := unixConnectionPair(t)
	actualUID := uint32(os.Getuid())
	expectedUID := differentUID(actualUID)

	_, err := ipc.DecodeUnixFrame(server, expectedUID)
	assertPeerErrorCode(t, err, ipc.PeerErrorCodeUIDMismatch)
	for _, uid := range []uint32{actualUID, expectedUID} {
		if strings.Contains(err.Error(), strconv.FormatUint(uint64(uid), 10)) {
			t.Fatalf("peer error disclosed UID %d: %q", uid, err)
		}
	}
}

func assertPeerErrorCode(t *testing.T, err error, want ipc.PeerErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("DecodeUnixFrame() error = nil, want code %q", want)
	}
	var peerError *ipc.PeerError
	if !errors.As(err, &peerError) {
		t.Fatalf("DecodeUnixFrame() error type = %T, want *ipc.PeerError", err)
	}
	if got := peerError.Code(); got != want {
		t.Fatalf("peer error code = %q, want %q", got, want)
	}
}

func unixConnectionPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "enforcer.sock")
	address := &net.UnixAddr{Name: socketPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatalf("listen Unix socket: %v", err)
	}

	client, err := net.DialUnix("unix", nil, address)
	if err != nil {
		listener.Close()
		t.Fatalf("dial Unix socket: %v", err)
	}
	server, err := listener.AcceptUnix()
	if err != nil {
		client.Close()
		listener.Close()
		t.Fatalf("accept Unix socket: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	if err := client.SetDeadline(deadline); err != nil {
		t.Fatalf("set client deadline: %v", err)
	}
	if err := server.SetDeadline(deadline); err != nil {
		t.Fatalf("set server deadline: %v", err)
	}
	t.Cleanup(func() {
		server.Close()
		client.Close()
		listener.Close()
	})
	return server, client
}

func writeUnixFrame(t *testing.T, connection *net.UnixConn, contents []byte) {
	t.Helper()
	written, err := io.Copy(connection, bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("write frame: %v", err)
	}
	if written != int64(len(contents)) {
		t.Fatalf("wrote %d frame bytes, want %d", written, len(contents))
	}
}

func differentUID(uid uint32) uint32 {
	if uid == ^uint32(0) {
		return uid - 1
	}
	return uid + 1
}
