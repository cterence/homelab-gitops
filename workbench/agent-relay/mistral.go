package main

import (
	"context"
	"fmt"

	mistral "github.com/cterence/mistral-client-go/mistral"
)

// startConversation starts a stored Mistral agent conversation and returns its id.
func startConversation(ctx context.Context, apiKey, agentID, conversationName, prompt string) (string, error) {
	cfg := mistral.NewConfiguration()
	cfg.AddDefaultHeader("Authorization", "Bearer "+apiKey)
	client := mistral.NewAPIClient(cfg)

	entries := []mistral.EntriesInner{{
		MessageInputEntry: &mistral.MessageInputEntry{
			Role:    "user",
			Content: mistral.Content{String: mistral.PtrString(prompt)},
		},
	}}

	req := mistral.ConversationRequest{
		Inputs:  mistral.ConversationInputs{ArrayOfEntriesInner: &entries},
		Store:   mistral.PtrBool(true),
		AgentId: mistral.PtrString(agentID),
	}
	if conversationName != "" {
		req.Name = mistral.PtrString(conversationName)
	}

	resp, _, err := client.BetaConversationsAPI.AgentsApiV1ConversationsStart(ctx).
		ConversationRequest(req).Execute()
	if err != nil {
		return "", fmt.Errorf("starting conversation with agent %s: %w", agentID, err)
	}

	return resp.ConversationId, nil
}
