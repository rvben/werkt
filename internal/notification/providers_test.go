package notification

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type staticSecrets map[string]string

func (s staticSecrets) Resolve(_ context.Context, names []string) (map[string]string, error) {
	result := make(map[string]string, len(names))
	for _, name := range names {
		if value, exists := s[name]; exists {
			result[name] = value
		} else {
			return nil, errors.New("missing secret " + name)
		}
	}
	return result, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func response(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}
}

func responseBody(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func sampleMessage() Message {
	return Message{Title: "Approval needed", Body: "Review it", URL: "https://werkt.example/app/?view=approvals", Priority: "high", Tags: []string{"approval"}, OccurredAt: time.Now()}
}

func TestSenderNtfyUsesBasicVaultCredentialAndStableDedupeID(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		if request.URL.String() != "https://notify.example/ops" || string(body) != "Review it" {
			t.Fatalf("request = %s body=%q", request.URL, body)
		}
		wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("publisher:password"))
		if request.Header.Get("Authorization") != wantAuth || request.Header.Get("X-Sequence-ID") != "delivery_1" || request.Header.Get("Click") == "" {
			t.Fatalf("headers = %#v", request.Header)
		}
		if request.UserAgent() != userAgent {
			t.Fatalf("user agent = %q", request.UserAgent())
		}
		return response(http.StatusOK), nil
	})}
	sender := NewSenderWithClient(staticSecrets{"ntfy/basic": "publisher:password"}, client)
	status, err := sender.Send(context.Background(), Destination{ID: "phone", Provider: "ntfy", Server: "https://notify.example", Topic: "ops", BasicSecret: "ntfy/basic"}, sampleMessage(), "delivery_1")
	if err != nil || status != http.StatusOK {
		t.Fatalf("Send() status=%d err=%v", status, err)
	}
}

func TestSenderBuildsTelegramAndPushbulletContracts(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		body, _ := io.ReadAll(request.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		switch requests {
		case 1:
			if !strings.Contains(request.URL.Path, "/bot123:token/sendMessage") || payload["chat_id"] != "456" || payload["reply_markup"] == nil {
				t.Fatalf("telegram request url=%s payload=%#v", request.URL, payload)
			}
		case 2:
			if request.URL.Host != "api.pushbullet.com" || request.Header.Get("Access-Token") != "push-token" || payload["type"] != "link" {
				t.Fatalf("pushbullet request url=%s headers=%#v payload=%#v", request.URL, request.Header, payload)
			}
		}
		return response(http.StatusOK), nil
	})}
	sender := NewSenderWithClient(staticSecrets{"telegram/token": "123:token", "pushbullet/token": "push-token"}, client)
	if _, err := sender.Send(context.Background(), Destination{ID: "chat", Provider: "telegram", Server: "https://telegram.example", BotTokenSecret: "telegram/token", ChatID: "456"}, sampleMessage(), "delivery_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Send(context.Background(), Destination{ID: "push", Provider: "pushbullet", AccessTokenSecret: "pushbullet/token"}, sampleMessage(), "delivery_2"); err != nil {
		t.Fatal(err)
	}
}

func TestSenderBuildsPushoverContract(t *testing.T) {
	expiresAt := time.Now().Add(30 * time.Minute)
	message := sampleMessage()
	message.Priority = "default"
	message.ExpiresAt = &expiresAt
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://pushover-proxy.example/1/messages.json" {
			t.Fatalf("request URL = %s", request.URL)
		}
		if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || request.UserAgent() != userAgent {
			t.Fatalf("headers = %#v", request.Header)
		}
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if request.Form.Get("token") != "app-token" || request.Form.Get("user") != "user-key" {
			t.Fatalf("credentials were not encoded in the form")
		}
		if request.Form.Get("title") != message.Title || request.Form.Get("message") != message.Body || request.Form.Get("priority") != "0" {
			t.Fatalf("form = %#v", request.Form)
		}
		if request.Form.Get("url") != message.URL || request.Form.Get("url_title") != "Open in Werkt" || request.Form.Get("sound") != "pushover" {
			t.Fatalf("form = %#v", request.Form)
		}
		ttl, err := strconv.Atoi(request.Form.Get("ttl"))
		if err != nil || ttl < 1700 || ttl > 1800 {
			t.Fatalf("ttl = %q", request.Form.Get("ttl"))
		}
		return responseBody(http.StatusOK, `{"status":1,"request":"fixture"}`), nil
	})}
	sender := NewSenderWithClient(staticSecrets{"pushover/app": "app-token", "pushover/user": "user-key"}, client)
	status, err := sender.Send(context.Background(), Destination{
		ID: "phone", Provider: "pushover", Server: "https://pushover-proxy.example",
		AppTokenSecret: "pushover/app", UserKeySecret: "pushover/user", Sound: "pushover",
	}, message, "delivery_3")
	if err != nil || status != http.StatusOK {
		t.Fatalf("Send() status=%d err=%v", status, err)
	}
}

func TestSenderRejectsPushoverFailureInSuccessfulHTTPResponse(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return responseBody(http.StatusOK, `{"status":0,"errors":["invalid"]}`), nil
	})}
	sender := NewSenderWithClient(staticSecrets{"pushover/app": "app-token", "pushover/user": "user-key"}, client)
	_, err := sender.Send(context.Background(), Destination{
		ID: "phone", Provider: "pushover", AppTokenSecret: "pushover/app", UserKeySecret: "pushover/user",
	}, sampleMessage(), "delivery_4")
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) || deliveryErr.Retryable {
		t.Fatalf("Send() error = %#v", err)
	}
}

func TestSenderSanitizesTokenBearingTransportErrors(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed for " + request.URL.String())
	})}
	sender := NewSenderWithClient(staticSecrets{"telegram/token": "123:very-secret"}, client)
	_, err := sender.Send(context.Background(), Destination{ID: "chat", Provider: "telegram", BotTokenSecret: "telegram/token", ChatID: "456"}, sampleMessage(), "delivery_1")
	if err == nil || strings.Contains(err.Error(), "very-secret") || !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("Send() error = %v", err)
	}
}

func TestSenderClassifiesProviderFailures(t *testing.T) {
	for status, retryable := range map[int]bool{http.StatusBadRequest: false, http.StatusTooManyRequests: true, http.StatusBadGateway: true} {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(status), nil })}
		sender := NewSenderWithClient(staticSecrets{}, client)
		_, err := sender.Send(context.Background(), Destination{ID: "phone", Provider: "ntfy", Server: "https://notify.example", Topic: "ops"}, sampleMessage(), "delivery_1")
		var deliveryErr *DeliveryError
		if !errors.As(err, &deliveryErr) || deliveryErr.Retryable != retryable {
			t.Fatalf("status %d error = %#v", status, err)
		}
	}
}
