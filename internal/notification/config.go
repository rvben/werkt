package notification

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
)

var destinationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var supportedEvents = map[string]bool{
	"approval.requested": true,
	"approval.expiring":  true,
	"approval.resolved":  true,
	"run.failed":         true,
}

type Config struct {
	Destinations []Destination `json:"destinations"`
	Routes       []Route       `json:"routes"`
}

type Destination struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`

	Server              string `json:"server,omitempty"`
	Topic               string `json:"topic,omitempty"`
	TokenSecret         string `json:"tokenSecret,omitempty"`
	BasicSecret         string `json:"basicSecret,omitempty"`
	BotTokenSecret      string `json:"botTokenSecret,omitempty"`
	ChatID              string `json:"chatId,omitempty"`
	AccessTokenSecret   string `json:"accessTokenSecret,omitempty"`
	DeviceID            string `json:"deviceId,omitempty"`
	ChannelTag          string `json:"channelTag,omitempty"`
	URL                 string `json:"url,omitempty"`
	SigningSecret       string `json:"signingSecret,omitempty"`
	AuthorizationSecret string `json:"authorizationSecret,omitempty"`
}

type Route struct {
	Events       []string `json:"events"`
	Automations  []string `json:"automations,omitempty"`
	Destinations []string `json:"destinations"`
}

func Load(path, inline string) (Config, error) {
	if strings.TrimSpace(path) != "" && strings.TrimSpace(inline) != "" {
		return Config{}, errors.New("configure only one of WERKT_NOTIFICATIONS_FILE and WERKT_NOTIFICATIONS_JSON")
	}
	var contents []byte
	var err error
	switch {
	case strings.TrimSpace(path) != "":
		contents, err = os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read notification configuration: %w", err)
		}
	case strings.TrimSpace(inline) != "":
		contents = []byte(inline)
	default:
		return Config{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	var value Config
	if err := decoder.Decode(&value); err != nil {
		return Config{}, fmt.Errorf("decode notification configuration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("decode notification configuration: trailing JSON value")
		}
		return Config{}, fmt.Errorf("decode notification configuration: %w", err)
	}
	if err := value.Validate(); err != nil {
		return Config{}, err
	}
	return value, nil
}

func (c Config) Validate() error {
	destinations := make(map[string]Destination, len(c.Destinations))
	for _, destination := range c.Destinations {
		if !destinationIDPattern.MatchString(destination.ID) {
			return fmt.Errorf("notification destination id %q is invalid", destination.ID)
		}
		if _, exists := destinations[destination.ID]; exists {
			return fmt.Errorf("notification destination id %q is duplicated", destination.ID)
		}
		if err := destination.validate(); err != nil {
			return fmt.Errorf("notification destination %q: %w", destination.ID, err)
		}
		destinations[destination.ID] = destination
	}
	for index, route := range c.Routes {
		if len(route.Events) == 0 || len(route.Destinations) == 0 {
			return fmt.Errorf("notification route %d requires events and destinations", index+1)
		}
		for _, event := range route.Events {
			if !supportedEvents[event] {
				return fmt.Errorf("notification route %d uses unsupported event %q", index+1, event)
			}
		}
		for _, id := range route.Destinations {
			if _, exists := destinations[id]; !exists {
				return fmt.Errorf("notification route %d references unknown destination %q", index+1, id)
			}
		}
	}
	return nil
}

func (d Destination) validate() error {
	switch d.Provider {
	case "ntfy":
		if field := d.unexpectedField("server", "topic", "tokenSecret", "basicSecret"); field != "" {
			return fmt.Errorf("field %s is not valid for ntfy", field)
		}
		if err := validateBaseURL(d.Server); err != nil {
			return fmt.Errorf("server: %w", err)
		}
		if strings.TrimSpace(d.Topic) == "" || strings.ContainsAny(d.Topic, "/?#") {
			return errors.New("topic must be one non-empty path segment")
		}
		if d.TokenSecret != "" && d.BasicSecret != "" {
			return errors.New("tokenSecret and basicSecret are mutually exclusive")
		}
	case "telegram":
		if field := d.unexpectedField("server", "botTokenSecret", "chatId"); field != "" {
			return fmt.Errorf("field %s is not valid for telegram", field)
		}
		if strings.TrimSpace(d.BotTokenSecret) == "" || strings.TrimSpace(d.ChatID) == "" {
			return errors.New("botTokenSecret and chatId are required")
		}
		if d.Server != "" {
			if err := validateBaseURL(d.Server); err != nil {
				return fmt.Errorf("server: %w", err)
			}
		}
	case "pushbullet":
		if field := d.unexpectedField("accessTokenSecret", "deviceId", "channelTag"); field != "" {
			return fmt.Errorf("field %s is not valid for pushbullet", field)
		}
		if strings.TrimSpace(d.AccessTokenSecret) == "" {
			return errors.New("accessTokenSecret is required")
		}
		if d.DeviceID != "" && d.ChannelTag != "" {
			return errors.New("deviceId and channelTag are mutually exclusive")
		}
	case "webhook":
		if field := d.unexpectedField("url", "signingSecret", "authorizationSecret"); field != "" {
			return fmt.Errorf("field %s is not valid for webhook", field)
		}
		if err := validateEndpointURL(d.URL); err != nil {
			return fmt.Errorf("url: %w", err)
		}
	default:
		return fmt.Errorf("provider %q is not supported", d.Provider)
	}
	return nil
}

func validateBaseURL(raw string) error {
	if err := validateEndpointURL(raw); err != nil {
		return err
	}
	parsed, _ := url.Parse(raw)
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must not include a query or fragment")
	}
	return nil
}

func validateEndpointURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errors.New("must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil {
		return errors.New("must not contain credentials")
	}
	if parsed.Scheme == "http" && !privateHTTPHost(parsed.Hostname()) {
		return errors.New("plain HTTP is allowed only for local or private-network destinations")
	}
	return nil
}

func privateHTTPHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".lan") || strings.HasSuffix(host, ".internal") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func (d Destination) unexpectedField(allowed ...string) string {
	allowedSet := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = true
	}
	configured := []struct{ name, value string }{
		{"server", d.Server}, {"topic", d.Topic}, {"tokenSecret", d.TokenSecret},
		{"basicSecret", d.BasicSecret}, {"botTokenSecret", d.BotTokenSecret},
		{"chatId", d.ChatID}, {"accessTokenSecret", d.AccessTokenSecret},
		{"deviceId", d.DeviceID}, {"channelTag", d.ChannelTag}, {"url", d.URL},
		{"signingSecret", d.SigningSecret}, {"authorizationSecret", d.AuthorizationSecret},
	}
	for _, field := range configured {
		if field.value != "" && !allowedSet[field.name] {
			return field.name
		}
	}
	return ""
}

func (c Config) DestinationsFor(eventType, automationID string) []Destination {
	byID := make(map[string]Destination, len(c.Destinations))
	for _, destination := range c.Destinations {
		byID[destination.ID] = destination
	}
	selected := make(map[string]bool)
	result := make([]Destination, 0)
	for _, route := range c.Routes {
		if !slices.Contains(route.Events, eventType) || !matchesAutomation(route.Automations, automationID) {
			continue
		}
		for _, id := range route.Destinations {
			if !selected[id] {
				selected[id] = true
				result = append(result, byID[id])
			}
		}
	}
	return result
}

func matchesAutomation(filters []string, automationID string) bool {
	if len(filters) == 0 {
		return true
	}
	return slices.Contains(filters, "*") || slices.Contains(filters, automationID)
}
