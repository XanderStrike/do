package main

// Minimal OpenAI-compatible chat completions client with tool calling.
// Works with OpenAI, OpenRouter, local servers (Ollama, LM Studio), etc.
// Configure via env: LLM_BASE_URL, LLM_API_KEY, LLM_MODEL.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type completionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
}

type completionResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

type LLMClient struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

func newLLMClient() *LLMClient {
	base := os.Getenv("LLM_BASE_URL")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		model = "gpt-4o"
	}
	return &LLMClient{
		BaseURL: base,
		APIKey:  os.Getenv("LLM_API_KEY"),
		Model:   model,
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// Complete sends the conversation and returns the assistant's message
// (which may contain content and/or tool_calls).
func (c *LLMClient) Complete(ctx context.Context, messages []Message) (Message, error) {
	body, err := json.Marshal(completionRequest{
		Model:    c.Model,
		Messages: messages,
		Tools:    tools(),
	})
	if err != nil {
		return Message{}, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Message{}, err
	}
	if resp.StatusCode != 200 {
		return Message{}, fmt.Errorf("LLM request failed (%d): %s", resp.StatusCode, string(raw))
	}

	var cr completionResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return Message{}, fmt.Errorf("failed to parse LLM response: %w\nbody: %s", err, string(raw))
	}
	if cr.Error != nil {
		return Message{}, fmt.Errorf("LLM error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 {
		return Message{}, fmt.Errorf("LLM returned no choices")
	}
	return cr.Choices[0].Message, nil
}
