package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	ProviderOpenAI       = "openai"
	DefaultOpenAIBaseURL = "https://api.openai.com/v1"
	DefaultOpenAIModel   = "gpt-5.2"
)

type OpenAIClient struct {
	*Client
}

// NewOpenAIClient creates OpenAI client (backward compatible)
func NewOpenAIClient() AIClient {
	return NewOpenAIClientWithOptions()
}

// NewOpenAIClientWithOptions creates OpenAI client (supports options pattern)
func NewOpenAIClientWithOptions(opts ...ClientOption) AIClient {
	// 1. Create OpenAI preset options
	openaiOpts := []ClientOption{
		WithProvider(ProviderOpenAI),
		WithModel(DefaultOpenAIModel),
		WithBaseURL(DefaultOpenAIBaseURL),
	}

	// 2. Merge user options (user options have higher priority)
	allOpts := append(openaiOpts, opts...)

	// 3. Create base client
	baseClient := NewClient(allOpts...).(*Client)

	// 4. Create OpenAI client
	openaiClient := &OpenAIClient{
		Client: baseClient,
	}

	// 5. Set hooks to point to OpenAIClient (implement dynamic dispatch)
	baseClient.hooks = openaiClient

	return openaiClient
}

func (c *OpenAIClient) SetAPIKey(apiKey string, customURL string, customModel string) {
	c.APIKey = apiKey

	if len(apiKey) > 8 {
		c.logger.Infof("🔧 [MCP] OpenAI API Key: %s...%s", apiKey[:4], apiKey[len(apiKey)-4:])
	}
	if customURL != "" {
		c.BaseURL = customURL
		c.logger.Infof("🔧 [MCP] OpenAI using custom BaseURL: %s", customURL)
	} else {
		c.logger.Infof("🔧 [MCP] OpenAI using default BaseURL: %s", c.BaseURL)
	}
	if customModel != "" {
		c.Model = customModel
		c.logger.Infof("🔧 [MCP] OpenAI using custom Model: %s", customModel)
	} else {
		c.logger.Infof("🔧 [MCP] OpenAI using default Model: %s", c.Model)
	}
}

// OpenAI uses standard Bearer auth
func (c *OpenAIClient) setAuthHeader(reqHeaders http.Header) {
	c.Client.setAuthHeader(reqHeaders)
}

// buildUrl uses the Responses API required by current OpenAI/Codex models.
func (c *OpenAIClient) buildUrl() string {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if strings.HasSuffix(baseURL, "/responses") {
		return baseURL
	}
	return baseURL + "/responses"
}

func (c *OpenAIClient) buildRequestBodyFromRequest(req *Request) map[string]any {
	input := make([]map[string]any, 0, len(req.Messages))
	for _, message := range req.Messages {
		role := message.Role
		if role == "system" {
			role = "developer"
		}
		input = append(input, map[string]any{
			"role":    role,
			"content": message.Content,
		})
	}
	body := map[string]any{
		"model":             req.Model,
		"input":             input,
		"max_output_tokens": c.MaxTokens,
	}
	if req.MaxTokens != nil {
		body["max_output_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.Tools) > 0 {
		body["tools"] = req.Tools
	}
	return body
}

func (c *OpenAIClient) buildMCPRequestBody(systemPrompt, userPrompt string) map[string]any {
	input := make([]map[string]any, 0, 2)
	if systemPrompt != "" {
		input = append(input, map[string]any{
			"role":    "developer",
			"content": systemPrompt,
		})
	}
	input = append(input, map[string]any{
		"role":    "user",
		"content": userPrompt,
	})
	return map[string]any{
		"model":             c.Model,
		"input":             input,
		"max_output_tokens": c.MaxTokens,
	}
}

func (c *OpenAIClient) parseMCPResponse(body []byte) (string, error) {
	var response struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("failed to parse OpenAI response: %w", err)
	}
	if response.Error != nil {
		return "", fmt.Errorf("OpenAI API error: %s", response.Error.Message)
	}
	for _, item := range response.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" || content.Type == "text" {
				return content.Text, nil
			}
		}
	}
	return "", fmt.Errorf("OpenAI returned empty response")
}
