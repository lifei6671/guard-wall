//go:build linux

package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// ReadCredentialFile securely reads the YAML-referenced SMTP credential file.
// guardGID is the numeric GID assigned to the guard group on the target host.
func ReadCredentialFile(ctx context.Context, path string, guardGID uint32) ([]byte, error) {
	if ctx == nil || path == "" {
		return nil, ErrCredentialFileRead
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCredentialFileRead, err)
	}

	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, ErrCredentialFileRead
	}
	pathFacts, err := linuxCredentialFileFacts(pathInfo)
	if err != nil {
		return nil, err
	}
	if err := validateCredentialFilePolicy(pathFacts, guardGID); err != nil {
		return nil, err
	}

	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, errors.Join(ErrCredentialFileUnsafe, ErrCredentialFileSymlink)
		}
		return nil, ErrCredentialFileRead
	}
	file := os.NewFile(uintptr(fd), "credential")
	if file == nil {
		_ = syscall.Close(fd)
		return nil, ErrCredentialFileRead
	}

	contents, readErr := readValidatedCredentialFile(ctx, file, guardGID)
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		clear(contents)
		return nil, ErrCredentialFileRead
	}
	return contents, nil
}

func readValidatedCredentialFile(ctx context.Context, file *os.File, guardGID uint32) ([]byte, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, ErrCredentialFileRead
	}
	facts, err := linuxCredentialFileFacts(info)
	if err != nil {
		return nil, err
	}
	if err := validateCredentialFilePolicy(facts, guardGID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCredentialFileRead, err)
	}

	contents, err := io.ReadAll(io.LimitReader(file, MaxCredentialFileSize+1))
	if err != nil {
		clear(contents)
		return nil, ErrCredentialFileRead
	}
	if int64(len(contents)) > MaxCredentialFileSize {
		clear(contents)
		return nil, ErrCredentialFileTooLarge
	}
	if err := ctx.Err(); err != nil {
		clear(contents)
		return nil, fmt.Errorf("%w: %w", ErrCredentialFileRead, err)
	}
	return contents, nil
}

func linuxCredentialFileFacts(info os.FileInfo) (credentialFileFacts, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return credentialFileFacts{}, ErrCredentialFileRead
	}
	return credentialFileFacts{
		mode: info.Mode(),
		uid:  stat.Uid,
		gid:  stat.Gid,
		size: info.Size(),
	}, nil
}
