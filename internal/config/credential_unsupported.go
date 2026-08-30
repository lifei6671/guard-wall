//go:build !linux && !windows

package config

import "context"

// ReadCredentialFile reports Unsupported outside the verified Linux target.
func ReadCredentialFile(context.Context, string, uint32) ([]byte, error) {
	return nil, ErrCredentialFileUnsupported
}
