//go:build windows

package config

import (
	"context"
	"errors"
	"testing"
)

func TestReadCredentialFileWindowsIsExplicitlyUnsupported(t *testing.T) {
	contents, err := ReadCredentialFile(context.Background(), `C:\sensitive\smtp-secret`, 998)
	if contents != nil || !errors.Is(err, ErrCredentialFileUnsupported) {
		t.Fatalf("ReadCredentialFile() contents=%v error=%v", contents, err)
	}
}
