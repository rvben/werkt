package notification

import (
	"net/url"
	"strings"
	"testing"
)

func TestConfigRoutesFanOutOnceAndRespectAutomationScope(t *testing.T) {
	config := Config{
		Destinations: []Destination{
			{ID: "phone", Provider: "ntfy", Server: "https://notify.example", Topic: "ops"},
			{ID: "chat", Provider: "telegram", BotTokenSecret: "notifications/telegram", ChatID: "123"},
			{ID: "push", Provider: "pushover", AppTokenSecret: "notifications/pushover-app", UserKeySecret: "notifications/pushover-user"},
		},
		Routes: []Route{
			{Events: []string{"notification.test"}, Destinations: []string{"push"}},
			{Events: []string{"automation.notification"}, Automations: []string{"sermon"}, Destinations: []string{"push"}},
			{Events: []string{"approval.requested"}, Destinations: []string{"phone"}},
			{Events: []string{"approval.requested"}, Automations: []string{"sermon"}, Destinations: []string{"phone", "chat"}},
		},
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	got := config.DestinationsFor("approval.requested", "sermon")
	if len(got) != 2 || got[0].ID != "phone" || got[1].ID != "chat" {
		t.Fatalf("destinations = %#v", got)
	}
	got = config.DestinationsFor("run.failed", "sermon")
	if len(got) != 0 {
		t.Fatalf("unexpected run failure destinations = %#v", got)
	}
	got = config.DestinationsFor("automation.notification", "sermon")
	if len(got) != 1 || got[0].ID != "push" {
		t.Fatalf("automation notification destinations = %#v", got)
	}
}

func TestConfigRejectsUnsafeOrAmbiguousDestinations(t *testing.T) {
	credentialURL := (&url.URL{Scheme: "https", Host: "example.test", Path: "/hook", User: url.UserPassword("fixture-user", "fixture-password")}).String()
	for name, destination := range map[string]Destination{
		"credentials in url":   {ID: "ops", Provider: "webhook", URL: credentialURL},
		"public cleartext":     {ID: "ops", Provider: "webhook", URL: "http://example.test/hook"},
		"both ntfy auth modes": {ID: "ops", Provider: "ntfy", Server: "https://notify.example", Topic: "ops", TokenSecret: "token", BasicSecret: "basic"},
		"provider field mixup": {ID: "ops", Provider: "ntfy", Server: "https://notify.example", Topic: "ops", BotTokenSecret: "token"},
		"pushover missing key": {ID: "ops", Provider: "pushover", AppTokenSecret: "app-token"},
		"pushover field mixup": {ID: "ops", Provider: "pushover", AppTokenSecret: "app-token", UserKeySecret: "user-key", Topic: "ops"},
		"unknown provider":     {ID: "ops", Provider: "smtp"},
	} {
		t.Run(name, func(t *testing.T) {
			err := (Config{Destinations: []Destination{destination}}).Validate()
			if err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	_, err := Load("", `{"destinations":[],"routes":[],"surprise":true}`)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v", err)
	}
}
