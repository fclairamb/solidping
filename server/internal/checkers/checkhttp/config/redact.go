package config

import "net/url"

// RedactURL returns a URL safe to write to a log line: the userinfo password
// is replaced with "xxxxx" by the stdlib's (*url.URL).Redacted().
//
// Chat-command handlers ("/solidping checks add <url>") log the string the
// user typed, and a user is free to type https://user:secret@host/. Pass every
// such value through here.
//
// Unparsable input is returned unchanged rather than dropped: the point of
// those log lines is to say which value failed, and a value url.Parse rejects
// cannot contain a parsed userinfo section anyway.
func RedactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	return parsed.Redacted()
}
