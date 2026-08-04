package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ChatExplainer is deliberately not an Agent. It is an optional, read-only
// explanation adapter: the model receives only a mined path and score, and
// its text can never change candidate validation or status.
type ChatExplainer struct {
	Model model.BaseChatModel
}

func NewChatExplainer(chat model.BaseChatModel) *ChatExplainer {
	return &ChatExplainer{Model: chat}
}

func (e *ChatExplainer) Explain(ctx context.Context, candidate CausalPatternCandidate) (string, error) {
	if e == nil || e.Model == nil {
		return "", errors.New("causal explanation model unavailable")
	}
	payload, _ := json.Marshal(map[string]any{
		"causal_path": candidate.CausalPath,
		"frequency":   candidate.Frequency,
		"coverage":    candidate.Coverage,
		"confidence":  candidate.Confidence,
	})
	reader, err := e.Model.Stream(ctx, []*schema.Message{
		schema.SystemMessage("Explain a mined causal path in one concise sentence. Do not invent evidence, causes, or actions. Return explanation text only."),
		schema.UserMessage(string(payload)),
	}, model.WithMaxTokens(256), model.WithTemperature(0))
	if err != nil {
		return "", err
	}
	defer reader.Close()
	var builder strings.Builder
	for {
		message, recvErr := reader.Recv()
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			return "", recvErr
		}
		if message != nil {
			builder.WriteString(message.Content)
		}
		if builder.Len() > 4096 {
			break
		}
	}
	explanation := strings.TrimSpace(builder.String())
	if explanation == "" {
		return "", fmt.Errorf("explanation model returned empty output")
	}
	if runes := []rune(explanation); len(runes) > 4096 {
		explanation = string(runes[:4096])
	}
	return explanation, nil
}
