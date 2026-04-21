package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.temporal.io/sdk/activity"
)

// GenerateHighlightContentActivity uses Claude CLI to create individual Hugo markdown pages for highlights.
func GenerateHighlightContentActivity(ctx context.Context, input GenerateHighlightContentInput) (*ClaudeAnalysisResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Generating highlight content", "highlight_count", len(input.Highlights))

	opts := input.ClaudeOptions
	if opts.OAuthToken == "" {
		opts.OAuthToken = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	}
	if opts.Model == "" {
		opts.Model = ClaudeModel
	}
	if len(opts.AllowedTools) == 0 {
		opts.AllowedTools = []string{"Read", "Glob", "Grep", "Edit", "Write"}
	}

	// Write highlights JSON to workspace for Claude to read
	highlightsData, err := json.MarshalIndent(input.Highlights, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal highlights: %w", err)
	}
	pipelineDir := filepath.Join(input.WorkspacePath, ".pipeline")
	if err := os.MkdirAll(pipelineDir, 0755); err != nil {
		return nil, fmt.Errorf("create pipeline dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "highlights.json"), highlightsData, 0644); err != nil {
		return nil, fmt.Errorf("write highlights data: %w", err)
	}

	prompt := buildHighlightContentPrompt(input.SessionID)

	// Heartbeat ticker
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, "generating highlight pages...")
			case <-heartbeatDone:
				return
			}
		}
	}()
	defer close(heartbeatDone)

	result, err := RunClaude(ctx, input.WorkspacePath, opts, prompt,
		func(msg string, kv ...any) { logger.Info(msg, kv...) },
		func(detail string) { activity.RecordHeartbeat(ctx, detail) },
	)
	if err != nil {
		return nil, fmt.Errorf("claude highlight content: %w", err)
	}

	_ = os.RemoveAll(pipelineDir)
	return result, nil
}

func buildHighlightContentPrompt(sessionID string) string {
	return fmt.Sprintf(`You are generating highlight pages for a D&D campaign Hugo website.

%s
## Your Task
Read the highlights data at .pipeline/highlights.json and create individual Hugo markdown pages for each highlight.

## Instructions
1. Read existing highlight pages in content/highlights/*.md for style and front matter reference
2. Read .pipeline/highlights.json for the highlight data
3. For each highlight, create a markdown file at content/highlights/%s-<slug>.md
4. Match the existing front matter pattern exactly (title, date, session, moment_type, characters, description, weight)
5. Write engaging descriptions that capture the excitement of the moment
6. Reference characters by their character names
7. If a highlight has a "clip_path" field, embed the audio/video clip using the shortcode shown below

## Front Matter Template
---
title: "<Title>"
date: <session-date>
session: "Session: <date>"
moment_type: "<epic|funny|dramatic|tactical>"
characters: ["Character Name 1", "Character Name 2"]
description: "<Brief description>"
weight: <1-10, lower = more prominent>
tags: ["tag1", "tag2"]
---

<Longer narrative description of the moment>

## Embedding Clips
If the highlight has a clip_path, embed it after the narrative using this Hugo shortcode:

{{<clip src="<session-id>/<clip-filename>" caption="<title>">}}

The src path should be relative — just the session directory and filename, e.g.:
{{<clip src="2026-04-17/clip-01-epic-crit.wav" caption="Aelara's Critical Strike">}}

Only include the shortcode if clip_path is present in the highlight data.
`, buildPartySection(), sessionID)
}
