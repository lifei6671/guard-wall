package config

import (
	"errors"
	"io/fs"
	"testing"
)

func TestValidateCredentialFilePolicy(t *testing.T) {
	const guardGID uint32 = 998
	valid := credentialFileFacts{mode: 0o640, uid: 0, gid: guardGID, size: MaxCredentialFileSize}
	tests := []struct {
		name  string
		facts credentialFileFacts
		want  error
	}{
		{name: "root guard 0640", facts: valid},
		{name: "stricter 0600", facts: credentialFileFacts{mode: 0o600, uid: 0, gid: guardGID, size: 1}},
		{name: "stricter read only", facts: credentialFileFacts{mode: 0o440, uid: 0, gid: guardGID, size: 1}},
		{name: "symlink", facts: credentialFileFacts{mode: fs.ModeSymlink | 0o640, uid: 0, gid: guardGID}, want: ErrCredentialFileSymlink},
		{name: "directory", facts: credentialFileFacts{mode: fs.ModeDir | 0o640, uid: 0, gid: guardGID}, want: ErrCredentialFileNotRegular},
		{name: "non root owner", facts: credentialFileFacts{mode: 0o640, uid: 1000, gid: guardGID}, want: ErrCredentialFileOwner},
		{name: "wrong group", facts: credentialFileFacts{mode: 0o640, uid: 0, gid: guardGID + 1}, want: ErrCredentialFileOwner},
		{name: "owner execute", facts: credentialFileFacts{mode: 0o740, uid: 0, gid: guardGID}, want: ErrCredentialFileMode},
		{name: "group write", facts: credentialFileFacts{mode: 0o660, uid: 0, gid: guardGID}, want: ErrCredentialFileMode},
		{name: "group execute", facts: credentialFileFacts{mode: 0o650, uid: 0, gid: guardGID}, want: ErrCredentialFileMode},
		{name: "other read", facts: credentialFileFacts{mode: 0o644, uid: 0, gid: guardGID}, want: ErrCredentialFileMode},
		{name: "setuid bit", facts: credentialFileFacts{mode: fs.ModeSetuid | 0o640, uid: 0, gid: guardGID}, want: ErrCredentialFileMode},
		{name: "size above limit", facts: credentialFileFacts{mode: 0o640, uid: 0, gid: guardGID, size: MaxCredentialFileSize + 1}, want: ErrCredentialFileTooLarge},
		{name: "negative size", facts: credentialFileFacts{mode: 0o640, uid: 0, gid: guardGID, size: -1}, want: ErrCredentialFileRead},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateCredentialFilePolicy(test.facts, guardGID)
			if test.want == nil && err != nil {
				t.Fatalf("validateCredentialFilePolicy() error = %v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("validateCredentialFilePolicy() error = %v, want %v", err, test.want)
			}
			if test.want != nil && (errors.Is(test.want, ErrCredentialFileSymlink) ||
				errors.Is(test.want, ErrCredentialFileNotRegular) ||
				errors.Is(test.want, ErrCredentialFileOwner) ||
				errors.Is(test.want, ErrCredentialFileMode)) && !errors.Is(err, ErrCredentialFileUnsafe) {
				t.Fatalf("policy error %v is not categorized as unsafe", err)
			}
		})
	}
}
