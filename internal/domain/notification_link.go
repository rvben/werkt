package domain

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

const (
	MaxNotificationURLBytes  = 2048
	MaxNotificationLinkLabel = 40
)

// ValidateNotificationLink checks the link an automation attaches to its own
// notification. Tapping the message opens it, so only an absolute HTTPS URL
// without embedded credentials qualifies: a notification is not a channel for
// secrets, and a plain-HTTP link on a phone leaves the operator's network.
func ValidateNotificationLink(raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > MaxNotificationURLBytes {
		return errors.New("notification.url must contain at most 2048 bytes")
	}
	if strings.IndexFunc(raw, unicode.IsSpace) >= 0 || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return errors.New("notification.url must not contain whitespace or control characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" {
		return errors.New("notification.url must be an absolute HTTPS URL")
	}
	if parsed.User != nil {
		return errors.New("notification.url must not contain credentials")
	}
	return nil
}

// ValidateNotificationLinkLabel checks the label shown on a notification's
// link button. A label without a link would name nothing.
func ValidateNotificationLinkLabel(label, link string) error {
	if label == "" {
		return nil
	}
	if link == "" {
		return errors.New("notification.urlTitle requires notification.url")
	}
	if len([]rune(label)) > MaxNotificationLinkLabel || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return errors.New("notification.urlTitle must contain at most 40 characters and no control characters")
	}
	return nil
}
