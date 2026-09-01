//go:build linux

package ipc

import (
	"net"
	"syscall"
)

// PeerErrorCode classifies an accepted Unix connection identity failure.
type PeerErrorCode string

const (
	PeerErrorCodeCredentialUnavailable PeerErrorCode = "peer_credential_unavailable"
	PeerErrorCodeUIDMismatch           PeerErrorCode = "peer_uid_mismatch"
)

// PeerError reports only a stable peer identity classification. It never
// includes credentials or errors supplied by the operating system.
type PeerError struct {
	code PeerErrorCode
}

func (e *PeerError) Error() string {
	return "ipc peer rejected: " + string(e.code)
}

// Code returns the peer identity failure classification.
func (e *PeerError) Code() PeerErrorCode {
	return e.code
}

// DecodeUnixFrame authenticates an accepted Unix connection before reading
// and decoding one request frame. The expected UID must be resolved once at
// Enforcer startup. The caller owns the connection and must discard it after
// any returned error.
func DecodeUnixFrame(connection *net.UnixConn, expectedUID uint32) (Request, error) {
	rawConnection, err := connection.SyscallConn()
	if err != nil {
		return nil, &PeerError{code: PeerErrorCodeCredentialUnavailable}
	}

	var credential *syscall.Ucred
	var credentialErr error
	if err := rawConnection.Control(func(fileDescriptor uintptr) {
		credential, credentialErr = syscall.GetsockoptUcred(
			int(fileDescriptor), syscall.SOL_SOCKET, syscall.SO_PEERCRED,
		)
	}); err != nil || credentialErr != nil || credential == nil {
		return nil, &PeerError{code: PeerErrorCodeCredentialUnavailable}
	}
	if credential.Uid != expectedUID {
		return nil, &PeerError{code: PeerErrorCodeUIDMismatch}
	}

	return DecodeFrame(connection)
}
