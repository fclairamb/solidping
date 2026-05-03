package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// Test errors for isNetworkError tests.
var (
	errGeneric          = errors.New("some error")
	errConnRefused      = errors.New("dial tcp: connection refused")
	errConnReset        = errors.New("connection reset by peer")
	errConnTimedOut     = errors.New("connection timed out")
	errNoHost           = errors.New("no such host")
	errNetUnreachable   = errors.New("network is unreachable")
	errIOTimeout        = errors.New("i/o timeout")
	errDialTCP          = errors.New("dial tcp 127.0.0.1:25: connect: connection refused")
	errInvalidRecipient = errors.New("invalid recipient format")
	errTemplateNotFound = errors.New("template not found")
)

func TestEmailJobDefinition_Type(t *testing.T) {
	t.Parallel()

	def := &EmailJobDefinition{}
	assert.Equal(t, jobdef.JobTypeEmail, def.Type())
}

func TestEmailJobDefinition_CreateJobRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  EmailJobConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config with raw content",
			config: EmailJobConfig{
				To:      []string{"test@example.com"},
				Subject: "Test Subject",
				HTML:    "<p>Hello</p>",
			},
			wantErr: false,
		},
		{
			name: "valid config with plain text only",
			config: EmailJobConfig{
				To:      []string{"test@example.com"},
				Subject: "Test Subject",
				Text:    "Hello World",
			},
			wantErr: false,
		},
		{
			name: "valid config with template",
			config: EmailJobConfig{
				To:           []string{"test@example.com"},
				Subject:      "Test Subject",
				Template:     "incident.html",
				TemplateData: map[string]any{"CheckName": "Test"},
			},
			wantErr: false,
		},
		{
			name: "valid config with multiple recipients",
			config: EmailJobConfig{
				To:      []string{"a@example.com", "b@example.com"},
				CC:      []string{"cc@example.com"},
				BCC:     []string{"bcc@example.com"},
				ReplyTo: "reply@example.com",
				Subject: "Test Subject",
				HTML:    "<p>Hello</p>",
			},
			wantErr: false,
		},
		{
			name: "missing recipients",
			config: EmailJobConfig{
				To:      []string{},
				Subject: "Test Subject",
				HTML:    "<p>Hello</p>",
			},
			wantErr: true,
			errMsg:  "requires at least one recipient",
		},
		{
			name: "missing subject",
			config: EmailJobConfig{
				To:   []string{"test@example.com"},
				HTML: "<p>Hello</p>",
			},
			wantErr: true,
			errMsg:  "requires a subject",
		},
		{
			name: "missing content and template",
			config: EmailJobConfig{
				To:      []string{"test@example.com"},
				Subject: "Test Subject",
			},
			wantErr: true,
			errMsg:  "requires either content",
		},
		{
			name: "both content and template",
			config: EmailJobConfig{
				To:       []string{"test@example.com"},
				Subject:  "Test Subject",
				HTML:     "<p>Hello</p>",
				Template: "incident.html",
			},
			wantErr: true,
			errMsg:  "cannot have both",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			def := &EmailJobDefinition{}

			configBytes, err := json.Marshal(tt.config)
			require.NoError(t, err)

			runner, err := def.CreateJobRun(configBytes)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
				assert.Nil(t, runner)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, runner)
			}
		})
	}
}

func TestEmailJobDefinition_CreateJobRun_InvalidJSON(t *testing.T) {
	t.Parallel()

	def := &EmailJobDefinition{}

	_, err := def.CreateJobRun([]byte("invalid json"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing email config")
}

func TestIsNetworkError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: false,
		},
		{
			name:     "regular error",
			err:      errGeneric,
			expected: false,
		},
		{
			name:     "connection refused",
			err:      errConnRefused,
			expected: true,
		},
		{
			name:     "connection reset",
			err:      errConnReset,
			expected: true,
		},
		{
			name:     "connection timed out",
			err:      errConnTimedOut,
			expected: true,
		},
		{
			name:     "no such host",
			err:      errNoHost,
			expected: true,
		},
		{
			name:     "network unreachable",
			err:      errNetUnreachable,
			expected: true,
		},
		{
			name:     "i/o timeout",
			err:      errIOTimeout,
			expected: true,
		},
		{
			name:     "dial tcp error",
			err:      errDialTCP,
			expected: true,
		},
		{
			name:     "wrapped network error",
			err:      fmt.Errorf("sending email: %w", errConnRefused),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := isNetworkError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// mockSender is a test double for email.Sender.
type mockSender struct {
	sendFunc func(ctx context.Context, msg *email.Message) (*email.SendResult, error)
	calls    []*email.Message
}

func (m *mockSender) Send(ctx context.Context, msg *email.Message) (*email.SendResult, error) {
	m.calls = append(m.calls, msg)
	if m.sendFunc != nil {
		return m.sendFunc(ctx, msg)
	}

	return &email.SendResult{Sent: true, Message: "mock: email sent"}, nil
}

// mockFormatter is a test double for email.Formatter.
type mockFormatter struct {
	formatFunc func(templateName string, data any) (string, string, error)
}

func (m *mockFormatter) Format(templateName string, data any) (string, string, error) {
	if m.formatFunc != nil {
		return m.formatFunc(templateName, data)
	}

	return "", "<html>formatted</html>", nil
}

func TestEmailJobRun_Run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		config     EmailJobConfig
		sender     *mockSender
		formatter  *mockFormatter
		wantErr    bool
		errMsg     string
		retryable  bool
		checkCalls func(t *testing.T, sender *mockSender)
	}{
		{
			name: "successful send with raw content",
			config: EmailJobConfig{
				To:      []string{"test@example.com"},
				Subject: "Test Subject",
				HTML:    "<p>Hello</p>",
				Text:    "Hello",
			},
			sender:    &mockSender{},
			formatter: &mockFormatter{},
			wantErr:   false,
			checkCalls: func(t *testing.T, sender *mockSender) {
				t.Helper()
				require.Len(t, sender.calls, 1)
				msg := sender.calls[0]
				assert.Equal(t, []string{"test@example.com"}, msg.Recipients.To)
				assert.Equal(t, "Test Subject", msg.Subject)
				assert.Equal(t, "<p>Hello</p>", msg.HTML)
				assert.Equal(t, "Hello", msg.Text)
			},
		},
		{
			name: "successful send with template",
			config: EmailJobConfig{
				To:           []string{"test@example.com"},
				Subject:      "Test Subject",
				Template:     "incident.html",
				TemplateData: map[string]any{"CheckName": "Test"},
			},
			sender:    &mockSender{},
			formatter: &mockFormatter{},
			wantErr:   false,
			checkCalls: func(t *testing.T, sender *mockSender) {
				t.Helper()
				require.Len(t, sender.calls, 1)
				msg := sender.calls[0]
				assert.Equal(t, "<html>formatted</html>", msg.HTML)
				assert.Equal(t, "plain text", msg.Text)
			},
		},
		{
			name: "network error is retryable",
			config: EmailJobConfig{
				To:      []string{"test@example.com"},
				Subject: "Test Subject",
				HTML:    "<p>Hello</p>",
			},
			sender: &mockSender{
				sendFunc: func(_ context.Context, _ *email.Message) (*email.SendResult, error) {
					return nil, errConnRefused
				},
			},
			formatter: &mockFormatter{},
			wantErr:   true,
			retryable: true,
		},
		{
			name: "non-network error is not retryable",
			config: EmailJobConfig{
				To:      []string{"test@example.com"},
				Subject: "Test Subject",
				HTML:    "<p>Hello</p>",
			},
			sender: &mockSender{
				sendFunc: func(_ context.Context, _ *email.Message) (*email.SendResult, error) {
					return nil, errInvalidRecipient
				},
			},
			formatter: &mockFormatter{},
			wantErr:   true,
			retryable: false,
		},
		{
			name: "template format error",
			config: EmailJobConfig{
				To:       []string{"test@example.com"},
				Subject:  "Test Subject",
				Template: "nonexistent.html",
			},
			sender: &mockSender{},
			formatter: &mockFormatter{
				formatFunc: func(_ string, _ any) (string, string, error) {
					return "", "", errTemplateNotFound
				},
			},
			wantErr: true,
			errMsg:  "formatting template",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run := &EmailJobRun{config: tt.config}

			jctx := &jobdef.JobContext{
				Job: &models.Job{
					UID:    "test-job-uid",
					Output: models.JSONMap{},
				},
				Services: &services.Registry{
					EmailSender:    tt.sender,
					EmailFormatter: tt.formatter,
				},
				Logger: slog.Default(),
			}

			err := run.Run(context.Background(), jctx)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
				if tt.retryable {
					assert.True(t, jobdef.IsRetryable(err), "error should be retryable")
				} else {
					assert.False(t, jobdef.IsRetryable(err), "error should not be retryable")
				}
			} else {
				require.NoError(t, err)
			}

			if tt.checkCalls != nil {
				tt.checkCalls(t, tt.sender)
			}
		})
	}
}

func TestEmailJobRun_Run_NoSender(t *testing.T) {
	t.Parallel()

	run := &EmailJobRun{
		config: EmailJobConfig{
			To:      []string{"test@example.com"},
			Subject: "Test",
			HTML:    "<p>Hello</p>",
		},
	}

	jctx := &jobdef.JobContext{
		Services: &services.Registry{
			EmailSender: nil, // No sender configured
		},
		Logger: slog.Default(),
	}

	err := run.Run(context.Background(), jctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmailSenderMissing)
}

func TestEmailJobRun_Run_NoFormatter(t *testing.T) {
	t.Parallel()

	run := &EmailJobRun{
		config: EmailJobConfig{
			To:       []string{"test@example.com"},
			Subject:  "Test",
			Template: "incident.html",
		},
	}

	jctx := &jobdef.JobContext{
		Services: &services.Registry{
			EmailSender:    &mockSender{},
			EmailFormatter: nil, // No formatter configured
		},
		Logger: slog.Default(),
	}

	err := run.Run(context.Background(), jctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmailFormatterMissing)
}

// netError is a test implementation of net.Error.
type netError struct {
	msg     string
	timeout bool
}

func (e *netError) Error() string   { return e.msg }
func (e *netError) Timeout() bool   { return e.timeout }
func (e *netError) Temporary() bool { return false }

func TestIsNetworkError_NetError(t *testing.T) {
	t.Parallel()

	var _ net.Error = &netError{} // Verify it implements net.Error

	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "net.Error timeout",
			err:      &netError{msg: "timeout", timeout: true},
			expected: true,
		},
		{
			name:     "net.Error non-timeout",
			err:      &netError{msg: "other network error", timeout: false},
			expected: true,
		},
		{
			name:     "wrapped net.Error",
			err:      fmt.Errorf("wrapped: %w", &netError{msg: "timeout", timeout: true}),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := isNetworkError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
