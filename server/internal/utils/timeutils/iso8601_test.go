package timeutils

import (
	"testing"
	"time"
)

func TestParseISO8601Duration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{
			name:     "1 second",
			input:    "PT1S",
			expected: time.Second,
			wantErr:  false,
		},
		{
			name:     "30 seconds",
			input:    "PT30S",
			expected: 30 * time.Second,
			wantErr:  false,
		},
		{
			name:     "1 minute",
			input:    "PT1M",
			expected: time.Minute,
			wantErr:  false,
		},
		{
			name:     "90 minutes",
			input:    "PT90M",
			expected: 90 * time.Minute,
			wantErr:  false,
		},
		{
			name:     "1 hour",
			input:    "PT1H",
			expected: time.Hour,
			wantErr:  false,
		},
		{
			name:     "1 hour 30 minutes",
			input:    "PT1H30M",
			expected: time.Hour + 30*time.Minute,
			wantErr:  false,
		},
		{
			name:     "1 hour 30 minutes 45 seconds",
			input:    "PT1H30M45S",
			expected: time.Hour + 30*time.Minute + 45*time.Second,
			wantErr:  false,
		},
		{
			name:     "invalid format - no PT prefix",
			input:    "1M",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "invalid format - empty",
			input:    "",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "invalid format - too short",
			input:    "PT",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "invalid number",
			input:    "PTxM",
			expected: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := ParseISO8601Duration(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseISO8601Duration() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("ParseISO8601Duration() unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("ParseISO8601Duration() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestFormatISO8601Duration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    time.Duration
		expected string
	}{
		{
			name:     "zero duration",
			input:    0,
			expected: "PT0S",
		},
		{
			name:     "1 second",
			input:    time.Second,
			expected: "PT1S",
		},
		{
			name:     "30 seconds",
			input:    30 * time.Second,
			expected: "PT30S",
		},
		{
			name:     "1 minute",
			input:    time.Minute,
			expected: "PT1M",
		},
		{
			name:     "90 seconds (1m30s)",
			input:    90 * time.Second,
			expected: "PT1M30S",
		},
		{
			name:     "1 hour",
			input:    time.Hour,
			expected: "PT1H",
		},
		{
			name:     "1 hour 30 minutes",
			input:    time.Hour + 30*time.Minute,
			expected: "PT1H30M",
		},
		{
			name:     "1 hour 30 minutes 45 seconds",
			input:    time.Hour + 30*time.Minute + 45*time.Second,
			expected: "PT1H30M45S",
		},
		{
			name:     "25 hours",
			input:    25 * time.Hour,
			expected: "PT25H",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := FormatISO8601Duration(tt.input)
			if result != tt.expected {
				t.Errorf("FormatISO8601Duration() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestFormatDurationAsInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    time.Duration
		expected string
	}{
		{
			name:     "zero duration",
			input:    0,
			expected: "00:00:00",
		},
		{
			name:     "1 second",
			input:    time.Second,
			expected: "00:00:01",
		},
		{
			name:     "30 seconds",
			input:    30 * time.Second,
			expected: "00:00:30",
		},
		{
			name:     "1 minute",
			input:    time.Minute,
			expected: "00:01:00",
		},
		{
			name:     "90 seconds (1m30s)",
			input:    90 * time.Second,
			expected: "00:01:30",
		},
		{
			name:     "1 hour",
			input:    time.Hour,
			expected: "01:00:00",
		},
		{
			name:     "1 hour 30 minutes",
			input:    time.Hour + 30*time.Minute,
			expected: "01:30:00",
		},
		{
			name:     "1 hour 30 minutes 45 seconds",
			input:    time.Hour + 30*time.Minute + 45*time.Second,
			expected: "01:30:45",
		},
		{
			name:     "25 hours",
			input:    25 * time.Hour,
			expected: "25:00:00",
		},
		{
			name:     "99 hours 59 minutes 59 seconds",
			input:    99*time.Hour + 59*time.Minute + 59*time.Second,
			expected: "99:59:59",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := formatDurationAsInterval(tt.input)
			if result != tt.expected {
				t.Errorf("formatDurationAsInterval() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestParsePostgresInterval(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{
			name:     "zero duration",
			input:    "00:00:00",
			expected: 0,
			wantErr:  false,
		},
		{
			name:     "1 second",
			input:    "00:00:01",
			expected: time.Second,
			wantErr:  false,
		},
		{
			name:     "30 seconds",
			input:    "00:00:30",
			expected: 30 * time.Second,
			wantErr:  false,
		},
		{
			name:     "1 minute",
			input:    "00:01:00",
			expected: time.Minute,
			wantErr:  false,
		},
		{
			name:     "1 minute 30 seconds",
			input:    "00:01:30",
			expected: time.Minute + 30*time.Second,
			wantErr:  false,
		},
		{
			name:     "1 hour",
			input:    "01:00:00",
			expected: time.Hour,
			wantErr:  false,
		},
		{
			name:     "1 hour 30 minutes",
			input:    "01:30:00",
			expected: time.Hour + 30*time.Minute,
			wantErr:  false,
		},
		{
			name:     "1 hour 30 minutes 45 seconds",
			input:    "01:30:45",
			expected: time.Hour + 30*time.Minute + 45*time.Second,
			wantErr:  false,
		},
		{
			name:     "25 hours",
			input:    "25:00:00",
			expected: 25 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "168 hours (7 days)",
			input:    "168:00:00",
			expected: 168 * time.Hour,
			wantErr:  false,
		},
		{
			name:     "with milliseconds",
			input:    "01:30:45.123",
			expected: time.Hour + 30*time.Minute + 45*time.Second,
			wantErr:  false,
		},
		{
			name:     "invalid format - no colons",
			input:    "010000",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "invalid format - single digit",
			input:    "1:0:0",
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "invalid format - empty",
			input:    "",
			expected: 0,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := parsePostgresInterval(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parsePostgresInterval() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("parsePostgresInterval() unexpected error: %v", err)
				return
			}
			if result != tt.expected {
				t.Errorf("parsePostgresInterval() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestDuration_Value(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration Duration
		expected string
	}{
		{
			name:     "zero duration",
			duration: Duration(0),
			expected: "00:00:00",
		},
		{
			name:     "1 minute",
			duration: Duration(time.Minute),
			expected: "00:01:00",
		},
		{
			name:     "1 hour",
			duration: Duration(time.Hour),
			expected: "01:00:00",
		},
		{
			name:     "1 hour 30 minutes 45 seconds",
			duration: Duration(time.Hour + 30*time.Minute + 45*time.Second),
			expected: "01:30:45",
		},
		{
			name:     "25 hours",
			duration: Duration(25 * time.Hour),
			expected: "25:00:00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			value, err := tt.duration.Value()
			if err != nil {
				t.Errorf("Duration.Value() unexpected error: %v", err)
				return
			}
			result, ok := value.(string)
			if !ok {
				t.Errorf("Duration.Value() returned non-string type: %T", value)
				return
			}
			if result != tt.expected {
				t.Errorf("Duration.Value() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestDuration_Scan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    any
		expected Duration
		wantErr  bool
	}{
		{
			name:     "nil value defaults to 1 minute",
			input:    nil,
			expected: Duration(time.Minute),
			wantErr:  false,
		},
		{
			name:     "ISO8601 format - 1 second",
			input:    "PT1S",
			expected: Duration(time.Second),
			wantErr:  false,
		},
		{
			name:     "ISO8601 format - 1 minute",
			input:    "PT1M",
			expected: Duration(time.Minute),
			wantErr:  false,
		},
		{
			name:     "ISO8601 format - 1 hour 30 minutes",
			input:    "PT1H30M",
			expected: Duration(time.Hour + 30*time.Minute),
			wantErr:  false,
		},
		{
			name:     "PostgreSQL interval format - 1 minute",
			input:    "00:01:00",
			expected: Duration(time.Minute),
			wantErr:  false,
		},
		{
			name:     "PostgreSQL interval format - 1 hour",
			input:    "01:00:00",
			expected: Duration(time.Hour),
			wantErr:  false,
		},
		{
			name:     "PostgreSQL interval format - 1 hour 30 minutes 45 seconds",
			input:    "01:30:45",
			expected: Duration(time.Hour + 30*time.Minute + 45*time.Second),
			wantErr:  false,
		},
		{
			name:     "byte slice input - ISO8601",
			input:    []byte("PT1M"),
			expected: Duration(time.Minute),
			wantErr:  false,
		},
		{
			name:     "byte slice input - PostgreSQL interval",
			input:    []byte("00:01:00"),
			expected: Duration(time.Minute),
			wantErr:  false,
		},
		{
			name:     "invalid format",
			input:    "invalid",
			expected: Duration(0),
			wantErr:  true,
		},
		{
			name:     "unsupported type",
			input:    123,
			expected: Duration(0),
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var d Duration
			err := d.Scan(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Duration.Scan() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("Duration.Scan() unexpected error: %v", err)
				return
			}
			if d != tt.expected {
				t.Errorf("Duration.Scan() = %v, want %v", d, tt.expected)
			}
		})
	}
}

func TestDuration_RoundTrip(t *testing.T) {
	t.Parallel()

	// Test that we can convert a duration to a database value and back
	tests := []struct {
		name     string
		duration Duration
	}{
		{
			name:     "1 minute",
			duration: Duration(time.Minute),
		},
		{
			name:     "1 hour",
			duration: Duration(time.Hour),
		},
		{
			name:     "1 hour 30 minutes",
			duration: Duration(time.Hour + 30*time.Minute),
		},
		{
			name:     "90 seconds",
			duration: Duration(90 * time.Second),
		},
		{
			name:     "7 days (168 hours)",
			duration: Duration(168 * time.Hour),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Convert to database value
			value, err := tt.duration.Value()
			if err != nil {
				t.Fatalf("Duration.Value() error: %v", err)
			}

			// Convert back from database value
			var scanned Duration
			err = scanned.Scan(value)
			if err != nil {
				t.Fatalf("Duration.Scan() error: %v", err)
			}

			// Should match original
			if scanned != tt.duration {
				t.Errorf("Round trip failed: original=%v, scanned=%v", tt.duration, scanned)
			}
		})
	}
}

func TestDuration_BackwardCompatibility(t *testing.T) {
	t.Parallel()

	// Test that we can still read old ISO8601 format from database
	tests := []struct {
		name        string
		storedValue string
		expectedDur Duration
	}{
		{
			name:        "old format - 1 minute",
			storedValue: "PT1M",
			expectedDur: Duration(time.Minute),
		},
		{
			name:        "old format - 30 seconds",
			storedValue: "PT30S",
			expectedDur: Duration(30 * time.Second),
		},
		{
			name:        "old format - 1 hour 30 minutes",
			storedValue: "PT1H30M",
			expectedDur: Duration(time.Hour + 30*time.Minute),
		},
		{
			name:        "new format - 1 minute",
			storedValue: "00:01:00",
			expectedDur: Duration(time.Minute),
		},
		{
			name:        "new format - 30 seconds",
			storedValue: "00:00:30",
			expectedDur: Duration(30 * time.Second),
		},
		{
			name:        "new format - 1 hour 30 minutes",
			storedValue: "01:30:00",
			expectedDur: Duration(time.Hour + 30*time.Minute),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var d Duration
			err := d.Scan(tt.storedValue)
			if err != nil {
				t.Errorf("Duration.Scan() error: %v", err)
				return
			}
			if d != tt.expectedDur {
				t.Errorf("Duration.Scan() = %v, want %v", d, tt.expectedDur)
			}
		})
	}
}

func TestFormatHumanReadable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    time.Duration
		expected string
	}{
		{
			name:     "less than 1 minute",
			input:    30 * time.Second,
			expected: "< 1m",
		},
		{
			name:     "exactly 1 minute",
			input:    time.Minute,
			expected: "1m",
		},
		{
			name:     "5 minutes",
			input:    5 * time.Minute,
			expected: "5m",
		},
		{
			name:     "1 hour",
			input:    time.Hour,
			expected: "1h",
		},
		{
			name:     "1 hour 30 minutes (shows hours only)",
			input:    time.Hour + 30*time.Minute,
			expected: "1h",
		},
		{
			name:     "2 hours",
			input:    2 * time.Hour,
			expected: "2h",
		},
		{
			name:     "24 hours (1 day)",
			input:    24 * time.Hour,
			expected: "1d",
		},
		{
			name:     "48 hours (2 days)",
			input:    48 * time.Hour,
			expected: "2d",
		},
		{
			name:     "25 hours (1 day 1 hour, shows days only)",
			input:    25 * time.Hour,
			expected: "1d",
		},
		{
			name:     "negative duration",
			input:    -5 * time.Minute,
			expected: "5m",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := FormatHumanReadable(tt.input)
			if result != tt.expected {
				t.Errorf("FormatHumanReadable(%v) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFormatPeriod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Go duration - 1m",
			input:    "1m",
			expected: "1m",
		},
		{
			name:     "Go duration - 5m",
			input:    "5m",
			expected: "5m",
		},
		{
			name:     "Go duration - 1h",
			input:    "1h",
			expected: "1h",
		},
		{
			name:     "Go duration - 30s",
			input:    "30s",
			expected: "< 1m",
		},
		{
			name:     "PostgreSQL interval - 00:01:00",
			input:    "00:01:00",
			expected: "1m",
		},
		{
			name:     "PostgreSQL interval - 00:05:00",
			input:    "00:05:00",
			expected: "5m",
		},
		{
			name:     "PostgreSQL interval - 01:00:00",
			input:    "01:00:00",
			expected: "1h",
		},
		{
			name:     "PostgreSQL interval - 24:00:00",
			input:    "24:00:00",
			expected: "1d",
		},
		{
			name:     "ISO 8601 - PT1M",
			input:    "PT1M",
			expected: "1m",
		},
		{
			name:     "ISO 8601 - PT5M",
			input:    "PT5M",
			expected: "5m",
		},
		{
			name:     "ISO 8601 - PT1H",
			input:    "PT1H",
			expected: "1h",
		},
		{
			name:     "Invalid format returns as-is",
			input:    "invalid",
			expected: "invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := FormatPeriod(tt.input)
			if result != tt.expected {
				t.Errorf("FormatPeriod(%v) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}
