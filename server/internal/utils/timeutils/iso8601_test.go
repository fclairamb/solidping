package timeutils

import (
	"testing"
	"time"
)

// Test case labels reused across multiple test tables.
const (
	caseNameZeroDuration  = "zero duration"
	caseName1Second       = "1 second"
	caseName30Seconds     = "30 seconds"
	caseName1Minute       = "1 minute"
	caseName1Hour         = "1 hour"
	caseName1H30M         = "1 hour 30 minutes"
	caseName1H30M45S      = "1 hour 30 minutes 45 seconds"
	caseName25Hours       = "25 hours"
	caseNameInvalidFormat = "invalid"
)

// ISO 8601 duration string constants reused across multiple test tables.
const (
	iso1S        = "PT1S"
	iso30S       = "PT30S"
	iso1M        = "PT1M"
	iso1H        = "PT1H"
	iso1H30M     = "PT1H30M"
	intvZero     = "00:00:00"
	intv30S      = "00:00:30"
	intv1M       = "00:01:00"
	intv1H       = "01:00:00"
	intv1H30M    = "01:30:00"
	intv1H30M45S = "01:30:45"
	intv25H      = "25:00:00"
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
			name:     caseName1Second,
			input:    iso1S,
			expected: time.Second,
			wantErr:  false,
		},
		{
			name:     caseName30Seconds,
			input:    iso30S,
			expected: 30 * time.Second,
			wantErr:  false,
		},
		{
			name:     caseName1Minute,
			input:    iso1M,
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
			name:     caseName1Hour,
			input:    iso1H,
			expected: time.Hour,
			wantErr:  false,
		},
		{
			name:     caseName1H30M,
			input:    iso1H30M,
			expected: time.Hour + 30*time.Minute,
			wantErr:  false,
		},
		{
			name:     caseName1H30M45S,
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
			name:     caseNameZeroDuration,
			input:    0,
			expected: "PT0S",
		},
		{
			name:     caseName1Second,
			input:    time.Second,
			expected: iso1S,
		},
		{
			name:     caseName30Seconds,
			input:    30 * time.Second,
			expected: iso30S,
		},
		{
			name:     caseName1Minute,
			input:    time.Minute,
			expected: iso1M,
		},
		{
			name:     "90 seconds (1m30s)",
			input:    90 * time.Second,
			expected: "PT1M30S",
		},
		{
			name:     caseName1Hour,
			input:    time.Hour,
			expected: iso1H,
		},
		{
			name:     caseName1H30M,
			input:    time.Hour + 30*time.Minute,
			expected: iso1H30M,
		},
		{
			name:     caseName1H30M45S,
			input:    time.Hour + 30*time.Minute + 45*time.Second,
			expected: "PT1H30M45S",
		},
		{
			name:     caseName25Hours,
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
			name:     caseNameZeroDuration,
			input:    0,
			expected: intvZero,
		},
		{
			name:     caseName1Second,
			input:    time.Second,
			expected: "00:00:01",
		},
		{
			name:     caseName30Seconds,
			input:    30 * time.Second,
			expected: intv30S,
		},
		{
			name:     caseName1Minute,
			input:    time.Minute,
			expected: intv1M,
		},
		{
			name:     "90 seconds (1m30s)",
			input:    90 * time.Second,
			expected: "00:01:30",
		},
		{
			name:     caseName1Hour,
			input:    time.Hour,
			expected: intv1H,
		},
		{
			name:     caseName1H30M,
			input:    time.Hour + 30*time.Minute,
			expected: intv1H30M,
		},
		{
			name:     caseName1H30M45S,
			input:    time.Hour + 30*time.Minute + 45*time.Second,
			expected: intv1H30M45S,
		},
		{
			name:     caseName25Hours,
			input:    25 * time.Hour,
			expected: intv25H,
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
			name:     caseNameZeroDuration,
			input:    intvZero,
			expected: 0,
			wantErr:  false,
		},
		{
			name:     caseName1Second,
			input:    "00:00:01",
			expected: time.Second,
			wantErr:  false,
		},
		{
			name:     caseName30Seconds,
			input:    intv30S,
			expected: 30 * time.Second,
			wantErr:  false,
		},
		{
			name:     caseName1Minute,
			input:    intv1M,
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
			name:     caseName1Hour,
			input:    intv1H,
			expected: time.Hour,
			wantErr:  false,
		},
		{
			name:     caseName1H30M,
			input:    intv1H30M,
			expected: time.Hour + 30*time.Minute,
			wantErr:  false,
		},
		{
			name:     caseName1H30M45S,
			input:    intv1H30M45S,
			expected: time.Hour + 30*time.Minute + 45*time.Second,
			wantErr:  false,
		},
		{
			name:     caseName25Hours,
			input:    intv25H,
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
			name:     caseNameZeroDuration,
			duration: Duration(0),
			expected: intvZero,
		},
		{
			name:     caseName1Minute,
			duration: Duration(time.Minute),
			expected: intv1M,
		},
		{
			name:     caseName1Hour,
			duration: Duration(time.Hour),
			expected: intv1H,
		},
		{
			name:     caseName1H30M45S,
			duration: Duration(time.Hour + 30*time.Minute + 45*time.Second),
			expected: intv1H30M45S,
		},
		{
			name:     caseName25Hours,
			duration: Duration(25 * time.Hour),
			expected: intv25H,
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
			input:    iso1S,
			expected: Duration(time.Second),
			wantErr:  false,
		},
		{
			name:     "ISO8601 format - 1 minute",
			input:    iso1M,
			expected: Duration(time.Minute),
			wantErr:  false,
		},
		{
			name:     "ISO8601 format - 1 hour 30 minutes",
			input:    iso1H30M,
			expected: Duration(time.Hour + 30*time.Minute),
			wantErr:  false,
		},
		{
			name:     "PostgreSQL interval format - 1 minute",
			input:    intv1M,
			expected: Duration(time.Minute),
			wantErr:  false,
		},
		{
			name:     "PostgreSQL interval format - 1 hour",
			input:    intv1H,
			expected: Duration(time.Hour),
			wantErr:  false,
		},
		{
			name:     "PostgreSQL interval format - 1 hour 30 minutes 45 seconds",
			input:    intv1H30M45S,
			expected: Duration(time.Hour + 30*time.Minute + 45*time.Second),
			wantErr:  false,
		},
		{
			name:     "byte slice input - ISO8601",
			input:    []byte(iso1M),
			expected: Duration(time.Minute),
			wantErr:  false,
		},
		{
			name:     "byte slice input - PostgreSQL interval",
			input:    []byte(intv1M),
			expected: Duration(time.Minute),
			wantErr:  false,
		},
		{
			name:     "invalid format",
			input:    caseNameInvalidFormat,
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
			name:     caseName1Minute,
			duration: Duration(time.Minute),
		},
		{
			name:     caseName1Hour,
			duration: Duration(time.Hour),
		},
		{
			name:     caseName1H30M,
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
			storedValue: iso1M,
			expectedDur: Duration(time.Minute),
		},
		{
			name:        "old format - 30 seconds",
			storedValue: iso30S,
			expectedDur: Duration(30 * time.Second),
		},
		{
			name:        "old format - 1 hour 30 minutes",
			storedValue: iso1H30M,
			expectedDur: Duration(time.Hour + 30*time.Minute),
		},
		{
			name:        "new format - 1 minute",
			storedValue: intv1M,
			expectedDur: Duration(time.Minute),
		},
		{
			name:        "new format - 30 seconds",
			storedValue: intv30S,
			expectedDur: Duration(30 * time.Second),
		},
		{
			name:        "new format - 1 hour 30 minutes",
			storedValue: intv1H30M,
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
			expected: HumanReadableSubMinute,
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
			name:     caseName1Hour,
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
			expected: HumanReadableSubMinute,
		},
		{
			name:     "PostgreSQL interval - 00:01:00",
			input:    intv1M,
			expected: "1m",
		},
		{
			name:     "PostgreSQL interval - 00:05:00",
			input:    "00:05:00",
			expected: "5m",
		},
		{
			name:     "PostgreSQL interval - 01:00:00",
			input:    intv1H,
			expected: "1h",
		},
		{
			name:     "PostgreSQL interval - 24:00:00",
			input:    "24:00:00",
			expected: "1d",
		},
		{
			name:     "ISO 8601 - PT1M",
			input:    iso1M,
			expected: "1m",
		},
		{
			name:     "ISO 8601 - PT5M",
			input:    "PT5M",
			expected: "5m",
		},
		{
			name:     "ISO 8601 - PT1H",
			input:    iso1H,
			expected: "1h",
		},
		{
			name:     "Invalid format returns as-is",
			input:    caseNameInvalidFormat,
			expected: caseNameInvalidFormat,
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
