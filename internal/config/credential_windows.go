//go:build windows

package config

import "context"

// ReadCredentialFile reports Unsupported on Windows because Phase 1 cannot
// prove the Linux root:guard ownership and mode contract there.
func ReadCredentialFile(context.Context, string, uint32) ([]byte, error) {
	return nil, ErrCredentialFileUnsupported
}
