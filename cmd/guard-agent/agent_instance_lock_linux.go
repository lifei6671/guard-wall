//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

var errGuardAgentAlreadyRunning = errors.New("guard-agent is already running")

// acquireAgentInstanceLock 在打开数据库前锁住其目录；同目录数据库共用一个运行所有者。
// 目录句柄由Agent持有至数据库关闭，关闭句柄即释放锁。
func acquireAgentInstanceLock(databasePath string) (io.Closer, error) {
	if !filepath.IsAbs(databasePath) {
		return nil, fmt.Errorf("acquire agent instance lock: database path must be absolute")
	}
	directoryPath := filepath.Dir(databasePath)
	fd, err := syscall.Open(directoryPath, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open agent instance directory: %w", err)
	}
	directory := os.NewFile(uintptr(fd), directoryPath)
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := directory.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			err = errors.Join(errGuardAgentAlreadyRunning, err)
		}
		return nil, errors.Join(fmt.Errorf("acquire agent instance lock: %w", err), closeErr)
	}
	return directory, nil
}
