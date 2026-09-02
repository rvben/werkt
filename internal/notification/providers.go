package notification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const userAgent = "Werkt-Notifier/1.0"

type SecretResolver interface {
	Resolve(context.Context, []string) (map[string]string, error)
}

type Sender struct {
	client  *http.Client
	secrets SecretResolver
}

type DeliveryError struct {
	Message   string
	Retryable bool
}

func (e *DeliveryError) Error() string { return e.Message }

func NewSender(secrets SecretResolver) *Sender {
	return &Sender{client: &http.Client{Timeout: 15 * time.Second}, secrets: secrets}
}

func NewSenderWithClient(secrets SecretResolver, client *http.Client) *Sender {
	return &Sender{client: client, secrets: secrets}
}

func (s *Sender) Send(ctx context.Context, destination Destination, message Message, deliveryID string) (int, error) {
	if s.secrets == nil {
		return 0, &DeliveryError{Message: "notification secret resolver is unavailable", Retryable: false}
	}
	secretNames := destination.secretNames()
	secrets, err := s.secrets.Resolve(ctx, secretNames)
	if err != nil {
		return 0, &DeliveryError{Message: "resolve notification credential: " + safeSecretError(err), Retryable: false}
	}
	var request *http.Request
	switch destination.Provider {
	case "ntfy":
		request, err = ntfyRequest(ctx, destination, message, deliveryID, secrets)
	case "telegram":
		request, err = telegramRequest(ctx, destination, message, secrets)
	case "pushbullet":
		request, err = pushbulletRequest(ctx, destination, message, secrets)
	case "pushover":
		request, err = pushoverRequest(ctx, destination, message, secrets)
	case "webhook":
		request, err = webhookRequest(ctx, destination, message, deliveryID, secrets)
	default:
		err = fmt.Errorf("unsupported notification provider %q", destination.Provider)
	}
	if err != nil {
		return 0, &DeliveryError{Message: err.Error(), Retryable: false}
	}
	request.Header.Set("User-Agent", userAgent)
	response, requestErr := s.client.Do(request)
	if requestErr != nil {
		// net/url errors may contain Telegram's token-bearing request path. Never
		// retain or log the underlying text.
		return 0, &DeliveryError{Message: destination.Provider + " notification request failed", Retryable: true}
	}
	defer response.Body.Close() //nolint:errcheck
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if destination.Provider == "pushover" {
			var result struct {
				Status int `json:"status"`
			}
			if json.Unmarshal(responseBody, &result) != nil || result.Status != 1 {
				return response.StatusCode, &DeliveryError{Message: "pushover notification was not accepted", Retryable: false}
			}
		}
		return response.StatusCode, nil
	}
	retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500
	return response.StatusCode, &DeliveryError{
		Message:   fmt.Sprintf("%s notification returned HTTP %d", destination.Provider, response.StatusCode),
		Retryable: retryable,
	}
}

func (d Destination) secretNames() []string {
	values := []string{d.TokenSecret, d.BasicSecret, d.BotTokenSecret, d.AccessTokenSecret, d.AppTokenSecret, d.UserKeySecret, d.SigningSecret, d.AuthorizationSecret}
	result := make([]string, 0, len(values))
	seen := make(map[string]bool)
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func ntfyRequest(ctx context.Context, destination Destination, message Message, deliveryID string, secrets map[string]string) (*http.Request, error) {
	endpoint := strings.TrimRight(destination.Server, "/") + "/" + url.PathEscape(destination.Topic)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(message.Body))
	if err != nil {
		return nil, errors.New("create ntfy request")
	}
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")
	request.Header.Set("Title", message.Title)
	request.Header.Set("Priority", message.Priority)
	request.Header.Set("Tags", strings.Join(message.Tags, ","))
	request.Header.Set("X-Sequence-ID", deliveryID)
	if message.URL != "" {
		request.Header.Set("Click", message.URL)
	}
	if destination.TokenSecret != "" {
		value, err := requiredSecret(secrets, destination.TokenSecret)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+value)
	} else if destination.BasicSecret != "" {
		value, err := requiredSecret(secrets, destination.BasicSecret)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(value)))
	}
	return request, nil
}

func telegramRequest(ctx context.Context, destination Destination, message Message, secrets map[string]string) (*http.Request, error) {
	token, err := requiredSecret(secrets, destination.BotTokenSecret)
	if err != nil {
		return nil, err
	}
	server := destination.Server
	if server == "" {
		server = "https://api.telegram.org"
	}
	body := map[string]any{
		"chat_id":              destination.ChatID,
		"text":                 message.Title + "\n\n" + message.Body,
		"link_preview_options": map[string]bool{"is_disabled": true},
	}
	if message.URL != "" {
		body["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{{{"text": "Open in Werkt", "url": message.URL}}}}
	}
	encoded, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+"/bot"+url.PathEscape(token)+"/sendMessage", bytes.NewReader(encoded))
	if err != nil {
		return nil, errors.New("create telegram request")
	}
	request.Header.Set("Content-Type", "application/json")
	return request, nil
}

func pushbulletRequest(ctx context.Context, destination Destination, message Message, secrets map[string]string) (*http.Request, error) {
	token, err := requiredSecret(secrets, destination.AccessTokenSecret)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"type": "link", "title": message.Title, "body": message.Body, "url": message.URL}
	if destination.DeviceID != "" {
		body["device_iden"] = destination.DeviceID
	}
	if destination.ChannelTag != "" {
		body["channel_tag"] = destination.ChannelTag
	}
	encoded, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.pushbullet.com/v2/pushes", bytes.NewReader(encoded))
	if err != nil {
		return nil, errors.New("create pushbullet request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Access-Token", token)
	return request, nil
}

func pushoverRequest(ctx context.Context, destination Destination, message Message, secrets map[string]string) (*http.Request, error) {
	appToken, err := requiredSecret(secrets, destination.AppTokenSecret)
	if err != nil {
		return nil, err
	}
	userKey, err := requiredSecret(secrets, destination.UserKeySecret)
	if err != nil {
		return nil, err
	}
	values := url.Values{
		"token":     {appToken},
		"user":      {userKey},
		"title":     {message.Title},
		"message":   {message.Body},
		"priority":  {pushoverPriority(message.Priority)},
		"timestamp": {strconv.FormatInt(message.OccurredAt.UTC().Unix(), 10)},
	}
	if message.URL != "" {
		values.Set("url", message.URL)
		values.Set("url_title", "Open in Werkt")
	}
	if destination.Device != "" {
		values.Set("device", destination.Device)
	}
	if destination.Sound != "" {
		values.Set("sound", destination.Sound)
	}
	if message.ExpiresAt != nil {
		ttl := int(time.Until(*message.ExpiresAt).Seconds())
		if ttl <= 0 {
			return nil, errors.New("pushover notification has already expired")
		}
		values.Set("ttl", strconv.Itoa(ttl))
	}
	server := destination.Server
	if server == "" {
		server = "https://api.pushover.net"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+"/1/messages.json", strings.NewReader(values.Encode()))
	if err != nil {
		return nil, errors.New("create pushover request")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return request, nil
}

func pushoverPriority(priority string) string {
	if priority == "high" {
		return "1"
	}
	return "0"
}

func webhookRequest(ctx context.Context, destination Destination, message Message, deliveryID string, secrets map[string]string) (*http.Request, error) {
	encoded, _ := json.Marshal(message)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, destination.URL, bytes.NewReader(encoded))
	if err != nil {
		return nil, errors.New("create webhook request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", deliveryID)
	request.Header.Set("X-Werkt-Delivery", deliveryID)
	if destination.AuthorizationSecret != "" {
		token, err := requiredSecret(secrets, destination.AuthorizationSecret)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if destination.SigningSecret != "" {
		secret, err := requiredSecret(secrets, destination.SigningSecret)
		if err != nil {
			return nil, err
		}
		timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "." + deliveryID + "."))
		_, _ = mac.Write(encoded)
		request.Header.Set("X-Werkt-Timestamp", timestamp)
		request.Header.Set("X-Werkt-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	return request, nil
}

func requiredSecret(values map[string]string, name string) (string, error) {
	value := values[name]
	if value == "" {
		return "", fmt.Errorf("notification credential %q is empty", name)
	}
	return value, nil
}

func safeSecretError(err error) string {
	if err == nil {
		return "unknown error"
	}
	return "unavailable"
}
