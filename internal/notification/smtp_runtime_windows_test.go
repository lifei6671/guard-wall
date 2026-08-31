//go:build windows

package notification

import (
	"context"
	"errors"
	"testing"

	"github.com/lifei6671/guard-wall/internal/config"
)

func TestSMTPRuntimeWindowsSecureCredentialFailureBlocksReady(t *testing.T) {
	runtime, err := NewSMTPRuntime(unexpectedSMTPWorker(t))
	if err != nil {
		t.Fatal(err)
	}

	err = runtime.Run(
		context.Background(),
		config.SMTP{CredentialFile: `C:\sensitive\smtp.credential`},
		998,
	)
	if !errors.Is(err, ErrSMTPNotReady) || !errors.Is(err, config.ErrCredentialFileUnsupported) {
		t.Fatalf("Run() error = %v", err)
	}
	assertReadyClosed(t, runtime.Ready(), false)
}
