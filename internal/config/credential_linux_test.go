//go:build linux

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadCredentialFileUsesLinuxStatAndNoFollow(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "credential")
	const secret = "smtp-password=must-not-appear-in-errors"
	if err := os.WriteFile(target, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	guardGID := uint32(os.Getegid())

	if os.Geteuid() == 0 {
		if err := os.Chown(target, 0, int(guardGID)); err != nil {
			t.Fatal(err)
		}
		contents, err := ReadCredentialFile(context.Background(), target, guardGID)
		if err != nil {
			t.Fatalf("ReadCredentialFile() error = %v", err)
		}
		if string(contents) != secret {
			t.Fatal("credential contents changed during secure read")
		}
		clear(contents)
	} else {
		_, err := ReadCredentialFile(context.Background(), target, guardGID)
		if !errors.Is(err, ErrCredentialFileOwner) {
			t.Fatalf("non-root-owned file error = %v", err)
		}
		if strings.Contains(err.Error(), target) || strings.Contains(err.Error(), secret) {
			t.Fatal("credential error leaked path or contents")
		}
	}

	link := filepath.Join(directory, "credential-link")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "linux" {
			t.Fatal(err)
		}
	}
	if _, err := ReadCredentialFile(context.Background(), link, guardGID); !errors.Is(err, ErrCredentialFileSymlink) {
		t.Fatalf("symlink error = %v", err)
	}
	if _, err := ReadCredentialFile(context.Background(), directory, guardGID); !errors.Is(err, ErrCredentialFileNotRegular) {
		t.Fatalf("directory error = %v", err)
	}
}
