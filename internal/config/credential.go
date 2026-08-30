package config

import (
	"errors"
	"io/fs"
)

// MaxCredentialFileSize is the fixed upper bound for an SMTP credential file.
// Credential contents are intentionally kept outside the Config value and schema.
const MaxCredentialFileSize int64 = 64 * 1024

var (
	// ErrCredentialFileUnsupported reports that secure credential-file loading is
	// unavailable on the current operating system.
	ErrCredentialFileUnsupported = errors.New("credential file loading is unsupported on this operating system")
	// ErrCredentialFileUnsafe is the common category for a file that violates
	// the root:guard, regular-file, or mode boundary.
	ErrCredentialFileUnsafe = errors.New("credential file does not satisfy the security policy")
	// ErrCredentialFileSymlink reports a symbolic-link credential path.
	ErrCredentialFileSymlink = errors.New("credential file must not be a symbolic link")
	// ErrCredentialFileNotRegular reports a non-regular credential path.
	ErrCredentialFileNotRegular = errors.New("credential file must be a regular file")
	// ErrCredentialFileOwner reports an owner or group mismatch.
	ErrCredentialFileOwner = errors.New("credential file owner or group is invalid")
	// ErrCredentialFileMode reports permissions wider than 0640.
	ErrCredentialFileMode = errors.New("credential file permissions are too broad")
	// ErrCredentialFileTooLarge reports a file beyond MaxCredentialFileSize.
	ErrCredentialFileTooLarge = errors.New("credential file exceeds the size limit")
	// ErrCredentialFileRead is a sanitized read failure. Underlying filesystem
	// errors and paths are deliberately not exposed.
	ErrCredentialFileRead = errors.New("credential file could not be read securely")
)

type credentialFileFacts struct {
	mode fs.FileMode
	uid  uint32
	gid  uint32
	size int64
}

func validateCredentialFilePolicy(facts credentialFileFacts, guardGID uint32) error {
	if facts.mode&fs.ModeSymlink != 0 {
		return errors.Join(ErrCredentialFileUnsafe, ErrCredentialFileSymlink)
	}
	if !facts.mode.IsRegular() {
		return errors.Join(ErrCredentialFileUnsafe, ErrCredentialFileNotRegular)
	}
	if facts.uid != 0 || facts.gid != guardGID {
		return errors.Join(ErrCredentialFileUnsafe, ErrCredentialFileOwner)
	}
	// 0640 is the widest accepted mode: no execute bits, no group write,
	// and no permissions for other users. Any subset is stricter and valid.
	if facts.mode.Perm()&^fs.FileMode(0o640) != 0 ||
		facts.mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
		return errors.Join(ErrCredentialFileUnsafe, ErrCredentialFileMode)
	}
	if facts.size < 0 {
		return ErrCredentialFileRead
	}
	if facts.size > MaxCredentialFileSize {
		return ErrCredentialFileTooLarge
	}
	return nil
}
