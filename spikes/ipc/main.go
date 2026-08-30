//go:build linux

// Command ipc-spike validates the Linux Unix Socket boundary required by M0-B4.
package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

const maxFrame = 1 << 20

type request struct {
	Version   int    `json:"version"`
	Operation string `json:"operation"`
}

type caseResult struct {
	Name        string `json:"name"`
	Passed      bool   `json:"passed"`
	Reason      string `json:"reason"`
	PeerUID     uint32 `json:"peer_uid"`
	ExpectedUID int    `json:"expected_uid"`
	FrameLength uint32 `json:"frame_length"`
	Operation   string `json:"operation,omitempty"`
}

func peerUID(connection *net.UnixConn) (uint32, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var credential *syscall.Ucred
	var socketError error
	if err := raw.Control(func(fileDescriptor uintptr) {
		credential, socketError = syscall.GetsockoptUcred(
			int(fileDescriptor), syscall.SOL_SOCKET, syscall.SO_PEERCRED,
		)
	}); err != nil {
		return 0, err
	}
	if socketError != nil {
		return 0, socketError
	}
	return credential.Uid, nil
}

func acceptOne(listener *net.UnixListener, name string) caseResult {
	connection, err := listener.AcceptUnix()
	if err != nil {
		return caseResult{Name: name, Reason: err.Error()}
	}
	defer connection.Close()

	uid, err := peerUID(connection)
	if err != nil {
		return caseResult{Name: name, Reason: err.Error()}
	}
	result := caseResult{Name: name, PeerUID: uid, ExpectedUID: os.Getuid()}
	if int(uid) != os.Getuid() {
		result.Reason = "peer_uid_mismatch"
		return result
	}

	var length uint32
	if err := binary.Read(connection, binary.BigEndian, &length); err != nil {
		result.Reason = err.Error()
		return result
	}
	result.FrameLength = length
	if length > maxFrame {
		result.Passed = name == "oversized_frame"
		result.Reason = "frame_too_large"
		return result
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(connection, payload); err != nil {
		result.Reason = err.Error()
		return result
	}
	var decoded request
	if err := json.Unmarshal(payload, &decoded); err != nil {
		result.Reason = "invalid_json"
		return result
	}
	result.Operation = decoded.Operation
	if decoded.Version != 1 {
		result.Reason = "unsupported_version"
		return result
	}
	if decoded.Operation != "ProbeCapabilities" {
		result.Passed = name == "disallowed_operation"
		result.Reason = "operation_rejected"
		return result
	}

	result.Passed = name == "allowed_operation"
	result.Reason = "operation_allowed"
	return result
}

func send(socketPath string, declaredLength uint32, payload []byte) error {
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := binary.Write(connection, binary.BigEndian, declaredLength); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err = connection.Write(payload)
	}
	return err
}

func runCase(listener *net.UnixListener, socketPath, name string, declaredLength uint32, payload []byte) caseResult {
	resultChannel := make(chan caseResult, 1)
	go func() { resultChannel <- acceptOne(listener, name) }()
	if err := send(socketPath, declaredLength, payload); err != nil {
		return caseResult{Name: name, Reason: err.Error()}
	}
	return <-resultChannel
}

func encodedRequest(operation string) []byte {
	payload, err := json.Marshal(request{Version: 1, Operation: operation})
	if err != nil {
		panic(err)
	}
	return payload
}

func main() {
	directory, err := os.MkdirTemp("", "guard-m0-ipc-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o750); err != nil {
		panic(err)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		panic(err)
	}
	directoryOwner, ok := directoryInfo.Sys().(*syscall.Stat_t)
	if !ok {
		panic("directory ownership is unavailable")
	}
	if directoryInfo.Mode().Perm() != 0o750 || int(directoryOwner.Uid) != os.Getuid() || int(directoryOwner.Gid) != os.Getgid() {
		panic("directory mode or ownership read-back mismatch")
	}

	socketPath := filepath.Join(directory, "enforcer.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		panic(err)
	}
	defer listener.Close()
	if err := os.Chmod(socketPath, 0o660); err != nil {
		panic(err)
	}
	socketInfo, err := os.Stat(socketPath)
	if err != nil {
		panic(err)
	}
	socketOwner, ok := socketInfo.Sys().(*syscall.Stat_t)
	if !ok {
		panic("socket ownership is unavailable")
	}
	if socketInfo.Mode().Perm() != 0o660 || int(socketOwner.Uid) != os.Getuid() || int(socketOwner.Gid) != os.Getgid() {
		panic("socket mode or ownership read-back mismatch")
	}

	allowed := encodedRequest("ProbeCapabilities")
	disallowed := encodedRequest("ExecuteShell")
	results := []caseResult{
		runCase(listener, socketPath, "allowed_operation", uint32(len(allowed)), allowed),
		runCase(listener, socketPath, "disallowed_operation", uint32(len(disallowed)), disallowed),
		runCase(listener, socketPath, "oversized_frame", maxFrame+1, nil),
	}
	for _, result := range results {
		if !result.Passed {
			fmt.Fprintf(os.Stderr, "%s failed: %s\n", result.Name, result.Reason)
			os.Exit(1)
		}
	}

	output := map[string]any{
		"status":              "PASS",
		"socket_mode":         fmt.Sprintf("%04o", socketInfo.Mode().Perm()),
		"socket_owner_uid":    socketOwner.Uid,
		"socket_owner_gid":    socketOwner.Gid,
		"directory_mode":      fmt.Sprintf("%04o", directoryInfo.Mode().Perm()),
		"directory_owner_uid": directoryOwner.Uid,
		"directory_owner_gid": directoryOwner.Gid,
		"results":             results,
		"not_verified": []string{
			"cross-UID rejection with the production guard user",
			"systemd unit hardening",
			"request object-name validation",
			"Enforcer restart and cancellation",
		},
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		panic(err)
	}
}
