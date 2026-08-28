package sshauth_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/fclairamb/solidping/server/internal/sshauth"
)

const testPassword = "s3cret"

// TestPasswordMethodsReturnsBoth asserts the helper offers exactly the two
// auth methods needed to work against either a plain-password server or a
// keyboard-interactive-only one.
func TestPasswordMethodsReturnsBoth(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	methods := sshauth.PasswordMethods(testPassword)
	r.Len(methods, 2)
}

// TestKeyboardInteractiveOnlyServerAuthenticates is the spec's primary case:
// a server whose ServerConfig sets ONLY KeyboardInteractiveCallback (no
// PasswordCallback) — the shape sshd's ChallengeResponseAuthentication
// presents, and what test.rebex.net switched to on 2026-08-25. A client
// offering only ssh.Password never gets to attempt anything against such a
// server; sshauth.PasswordMethods must authenticate successfully.
func TestKeyboardInteractiveOnlyServerAuthenticates(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var gotQuestions []string

	srv := startServer(t, &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(
			_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge,
		) (*ssh.Permissions, error) {
			answers, err := challenge("", "", []string{"Password: "}, []bool{false})
			if err != nil {
				return nil, err
			}

			gotQuestions = answers
			if len(answers) != 1 || answers[0] != testPassword {
				return nil, errAuthFailed
			}

			return &ssh.Permissions{}, nil
		},
	})

	dial(t, srv, testPassword, true)
	r.Equal([]string{testPassword}, gotQuestions)
}

// TestPasswordOnlyServerAuthenticates is the positive control: a server
// advertising only "password" must keep working exactly as before — adding
// keyboard-interactive to the client's method list must not break or bypass
// the existing password path.
func TestPasswordOnlyServerAuthenticates(t *testing.T) {
	t.Parallel()

	srv := startServer(t, &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			if string(password) != testPassword {
				return nil, errAuthFailed
			}

			return &ssh.Permissions{}, nil
		},
	})

	dial(t, srv, testPassword, true)
}

// TestKeyboardInteractiveOnlyServerRejectsWrongPassword proves the wrong
// password does not silently succeed against the keyboard-interactive path.
func TestKeyboardInteractiveOnlyServerRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	srv := startServer(t, &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(
			_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge,
		) (*ssh.Permissions, error) {
			answers, err := challenge("", "", []string{"Password: "}, []bool{false})
			if err != nil {
				return nil, err
			}

			if len(answers) != 1 || answers[0] != testPassword {
				return nil, errAuthFailed
			}

			return &ssh.Permissions{}, nil
		},
	})

	dial(t, srv, "wrong-password", false)
}

// TestKeyboardInteractiveZeroQuestionChallenge pins the answer-count
// contract for a zero-question challenge (a server-side banner/notice with
// nothing to answer): x/crypto/ssh treats a mismatched answer count as a
// protocol error, so the callback must return an empty slice, never one
// sized for a phantom question.
func TestKeyboardInteractiveZeroQuestionChallenge(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var challengeErr error

	srv := startServer(t, &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(
			_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge,
		) (*ssh.Permissions, error) {
			// First, a zero-question "banner" round — must not error and
			// must not desync the exchange.
			_, challengeErr = challenge("", "", nil, nil)
			if challengeErr != nil {
				return nil, challengeErr
			}

			// Then the real prompt, so this test still proves overall auth
			// success rather than only that the first round didn't crash.
			answers, err := challenge("", "", []string{"Password: "}, []bool{false})
			if err != nil {
				return nil, err
			}

			if len(answers) != 1 || answers[0] != testPassword {
				return nil, errAuthFailed
			}

			return &ssh.Permissions{}, nil
		},
	})

	dial(t, srv, testPassword, true)
	r.NoError(challengeErr)
}
