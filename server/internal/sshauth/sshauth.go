// Package sshauth builds golang.org/x/crypto/ssh authentication methods
// shared by every SSH-speaking checker/integration in the repo (checkssh,
// checksftp, sshtunnel).
package sshauth

import "golang.org/x/crypto/ssh"

// PasswordMethods returns the auth methods to try for a plaintext password:
// both "password" and "keyboard-interactive".
//
// x/crypto/ssh never attempts an auth method the server doesn't advertise in
// its "authentications that can continue" list. Some servers (a common
// sshd ChallengeResponseAuthentication configuration, e.g. test.rebex.net as
// of 2026-08-25) advertise only "keyboard-interactive" and never
// "password" — a client offering only ssh.Password fails having attempted
// nothing. Every OpenSSH client handles this transparently by answering
// keyboard-interactive prompts with the password; this is that same
// behavior.
func PasswordMethods(password string) []ssh.AuthMethod {
	return []ssh.AuthMethod{
		ssh.Password(password),
		ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			// A zero-question challenge is a server banner/notice, not a
			// real prompt — answering it with a non-empty slice is a
			// protocol error, so it must get back an empty slice.
			if len(questions) == 0 {
				return []string{}, nil
			}

			answers := make([]string, len(questions))
			for i := range answers {
				answers[i] = password
			}

			return answers, nil
		}),
	}
}
