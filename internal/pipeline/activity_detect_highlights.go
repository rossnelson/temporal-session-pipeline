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

// DetectHighlightsActivity uses Claude CLI to analyze a transcript and identify highlight moments.
func DetectHighlightsActivity(ctx context.Context, input DetectHighlightsInput) (*DetectHighlightsResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Detecting highlights", "session_id", input.SessionID)

	opts := input.ClaudeOptions
	if opts.OAuthToken == "" {
		opts.OAuthToken = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	}
	if opts.Model == "" {
		opts.Model = ClaudeModel
	}
	if len(opts.AllowedTools) == 0 {
		opts.AllowedTools = []string{"Read", "Glob", "Grep", "Write"}
	}

	// Create a temp workspace for Claude to work in.
	workDir, err := os.MkdirTemp("", "highlights-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	// Copy transcript to workspace
	transcriptData, err := os.ReadFile(input.TranscriptPath)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(workDir, ".pipeline"), 0755); err != nil {
		return nil, fmt.Errorf("create pipeline dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, ".pipeline", "transcript.json"), transcriptData, 0644); err != nil {
		return nil, fmt.Errorf("write transcript: %w", err)
	}

	// Copy session notes if provided
	var sessionNotes string
	if input.NotesPath != "" {
		if notesData, err := os.ReadFile(input.NotesPath); err == nil {
			sessionNotes = string(notesData)
			_ = os.WriteFile(filepath.Join(workDir, ".pipeline", "session-notes.txt"), notesData, 0644)
		}
	}

	prompt := buildHighlightPrompt(input.SessionID, sessionNotes)

	// Heartbeat ticker
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, "detecting highlights...")
			case <-heartbeatDone:
				return
			}
		}
	}()
	defer close(heartbeatDone)

	_, err = RunClaude(ctx, workDir, opts, prompt,
		func(msg string, kv ...any) { logger.Info(msg, kv...) },
		func(detail string) { activity.RecordHeartbeat(ctx, detail) },
	)
	if err != nil {
		return nil, fmt.Errorf("claude highlights: %w", err)
	}

	// Read the highlights JSON that Claude wrote
	highlightsPath := filepath.Join(workDir, ".pipeline", "highlights.json")
	highlightsData, err := os.ReadFile(highlightsPath)
	if err != nil {
		return nil, fmt.Errorf("read highlights.json: %w (Claude may not have written it)", err)
	}

	var highlights []Highlight
	if err := json.Unmarshal(highlightsData, &highlights); err != nil {
		return nil, fmt.Errorf("parse highlights: %w", err)
	}

	logger.Info("Highlights detected", "count", len(highlights))
	return &DetectHighlightsResult{Highlights: highlights}, nil
}

func buildHighlightPrompt(sessionID, sessionNotes string) string {
	notesSection := ""
	if sessionNotes != "" {
		notesSection = "\n## Session Notes (from the DM/players)\n" + sessionNotes +
			"\n\nUse these notes to filter out non-game audio (talking to babies/children/pets, side conversations) and understand context. Do NOT create highlights from non-game moments.\n"
	}

	return fmt.Sprintf(`You are analyzing a D&D session transcript to identify highlight moments.

%s
%s
## CRITICAL: Source of Truth
The transcript at .pipeline/transcript.json is your ONLY source of information.
Identify highlights based ONLY on what is described in the transcript. Do NOT infer events from other sources.

## Your Task
Read the transcript and identify the most memorable, exciting, or funny moments from THIS session only.

## Instructions
1. Read the full transcript
2. Identify 5-15 highlight moments across these categories:
   - **epic**: Critical hits, clutch saves, dramatic victories, near-death experiences
   - **funny**: Table jokes, absurd plans, unexpected outcomes, cheese-related incidents
   - **dramatic**: Character moments, roleplay scenes, betrayals, reveals
   - **tactical**: Brilliant tactical plays, creative ability usage, clever combos

3. For each highlight, determine the EXACT start and end timestamps (in milliseconds) from the transcript segments
4. Write the results as a JSON array to .pipeline/highlights.json

## Output Format
Write a JSON array to .pipeline/highlights.json with this exact structure:
[
  {
    "id": "epic-crit-01",
    "type": "epic",
    "title": "Short descriptive title",
    "description": "2-3 sentence description of what happened",
    "start_ms": 345000,
    "end_ms": 362000,
    "speakers": ["aelara", "gareth"],
    "transcript_excerpt": "Key dialogue from the moment...",
    "tags": ["combat", "critical-hit"]
  }
]

## Important
- Use the RESOLVED speaker names from the transcript (not SPEAKER_00 labels)
- Timestamps must be exact millisecond values from the transcript segments
- Add 5 seconds of padding before and after each moment for context
- The id should be descriptive: type + short-slug (e.g., "funny-cheese-throw", "epic-nat-20")
- Only include moments that are clearly evidenced in the transcript dialogue
- Do NOT include moments that are clearly non-game (talking to children, ordering food, etc.)
- Session ID for reference: %s
`, buildPartySection(), notesSection, sessionID)
}
