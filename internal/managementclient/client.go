package managementclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rvben/werkt/internal/domain"
)

type Client struct {
	baseURL    *url.URL
	token      string
	httpClient *http.Client
}

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Werkt API returned %d: %s", e.Status, e.Message)
}

func New(baseURL, token string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Werkt API URL %q", baseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("Werkt API URL scheme must be http or https")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: parsed, token: token, httpClient: httpClient}, nil
}

func (c *Client) CreateDeployment(ctx context.Context, archive io.Reader, digest, idempotencyKey, actor string) (domain.Deployment, bool, error) {
	request, err := c.request(ctx, http.MethodPost, "/api/v1/deployments", archive)
	if err != nil {
		return domain.Deployment{}, false, err
	}
	request.Header.Set("Content-Type", "application/gzip")
	request.Header.Set("X-Werkt-Content-SHA256", digest)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("X-Werkt-Actor", actor)
	var response struct {
		Deployment domain.Deployment `json:"deployment"`
		Created    bool              `json:"created"`
	}
	if err := c.do(request, &response); err != nil {
		return domain.Deployment{}, false, err
	}
	return response.Deployment, response.Created, nil
}

func (c *Client) GetDeployment(ctx context.Context, deploymentID string) (domain.Deployment, error) {
	request, err := c.request(ctx, http.MethodGet, "/api/v1/deployments/"+url.PathEscape(deploymentID), nil)
	if err != nil {
		return domain.Deployment{}, err
	}
	var value domain.Deployment
	if err := c.do(request, &value); err != nil {
		return domain.Deployment{}, err
	}
	return value, nil
}

func (c *Client) CancelDeployment(ctx context.Context, deploymentID, actor string) (domain.Deployment, error) {
	request, err := c.request(ctx, http.MethodPost, "/api/v1/deployments/"+url.PathEscape(deploymentID)+"/cancel", nil)
	if err != nil {
		return domain.Deployment{}, err
	}
	request.Header.Set("X-Werkt-Actor", actor)
	var value domain.Deployment
	if err := c.do(request, &value); err != nil {
		return domain.Deployment{}, err
	}
	return value, nil
}

func (c *Client) RetryDeployment(ctx context.Context, deploymentID, idempotencyKey, actor string) (domain.Deployment, bool, error) {
	request, err := c.request(ctx, http.MethodPost, "/api/v1/deployments/"+url.PathEscape(deploymentID)+"/retry", nil)
	if err != nil {
		return domain.Deployment{}, false, err
	}
	request.Header.Set("Idempotency-Key", idempotencyKey)
	request.Header.Set("X-Werkt-Actor", actor)
	var response struct {
		Deployment domain.Deployment `json:"deployment"`
		Created    bool              `json:"created"`
	}
	if err := c.do(request, &response); err != nil {
		return domain.Deployment{}, false, err
	}
	return response.Deployment, response.Created, nil
}

func (c *Client) RollbackAutomation(ctx context.Context, automationID, revisionID, actor string) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]string{"revisionId": revisionID})
	if err != nil {
		return nil, err
	}
	request, err := c.request(ctx, http.MethodPost, "/api/v1/automations/"+url.PathEscape(automationID)+"/rollback", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Werkt-Actor", actor)
	var response json.RawMessage
	if err := c.do(request, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func (c *Client) WaitDeployment(ctx context.Context, deployment domain.Deployment, pollInterval time.Duration) (domain.Deployment, error) {
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}
	for deployment.Status != domain.DeploymentSucceeded && deployment.Status != domain.DeploymentFailed && deployment.Status != domain.DeploymentCancelled {
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return domain.Deployment{}, ctx.Err()
		case <-timer.C:
		}
		var err error
		deployment, err = c.GetDeployment(ctx, deployment.ID)
		if err != nil {
			return domain.Deployment{}, err
		}
	}
	return deployment, nil
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	reference, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	target := c.baseURL.ResolveReference(reference)
	// CreateDeployment accepts an io.Reader rather than transferring ownership
	// of an io.ReadCloser. Hide Close from net/http so the caller remains
	// responsible for its archive and can reliably detect local close errors.
	var requestBody io.Reader
	if body != nil {
		requestBody = readerOnly{Reader: body}
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), requestBody)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	return request, nil
}

type readerOnly struct {
	io.Reader
}

func (c *Client) do(request *http.Request, destination any) error {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 2<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(limited).Decode(&problem); err != nil || problem.Error == "" {
			problem.Error = http.StatusText(response.StatusCode)
		}
		return &APIError{Status: response.StatusCode, Message: problem.Error}
	}
	if err := json.NewDecoder(limited).Decode(destination); err != nil {
		return fmt.Errorf("decode Werkt API response: %w", err)
	}
	return nil
}
