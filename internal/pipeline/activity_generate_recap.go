package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.temporal.io/sdk/activity"
)

// GenerateRecapActivity uses Claude CLI to generate session recap and strategy guides.
func GenerateRecapActivity(ctx context.Context, input GenerateRecapInput) (*ClaudeAnalysisResult, error) {
	logger := activity.GetLogger(ctx)
	logger.Info("Generating session recap", "session_id", input.SessionID)

	// Copy transcript to workspace for Claude to read
	transcriptDest := filepath.Join(input.WorkspacePath, ".pipeline", "transcript.json")
	if err := os.MkdirAll(filepath.Dir(transcriptDest), 0755); err != nil {
		return nil, fmt.Errorf("create .pipeline dir: %w", err)
	}
	transcriptData, err := os.ReadFile(input.TranscriptPath)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}
	if err := os.WriteFile(transcriptDest, transcriptData, 0644); err != nil {
		return nil, fmt.Errorf("write transcript to workspace: %w", err)
	}

	// Copy session notes if provided
	var sessionNotes string
	if input.NotesPath != "" {
		if notesData, err := os.ReadFile(input.NotesPath); err == nil {
			sessionNotes = string(notesData)
			notesDest := filepath.Join(input.WorkspacePath, ".pipeline", "session-notes.txt")
			_ = os.WriteFile(notesDest, notesData, 0644)
			logger.Info("Session notes loaded", "path", input.NotesPath)
		} else {
			logger.Warn("Could not read session notes", "path", input.NotesPath, "error", err)
		}
	}

	// Build prompt
	prompt := buildRecapPrompt(input.SessionID, input.SessionDate, sessionNotes)

	// Resolve API key
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

	// Background heartbeat ticker
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, "claude generating recap...")
			case <-heartbeatDone:
				return
			}
		}
	}()
	defer close(heartbeatDone)

	// Run Claude
	result, err := RunClaude(ctx, input.WorkspacePath, opts, prompt,
		func(msg string, kv ...any) { logger.Info(msg, kv...) },
		func(detail string) { activity.RecordHeartbeat(ctx, detail) },
	)
	if err != nil {
		return nil, fmt.Errorf("claude recap: %w", err)
	}

	// Clean up the .pipeline dir from workspace (don't commit it)
	_ = os.RemoveAll(filepath.Join(input.WorkspacePath, ".pipeline"))

	return result, nil
}

func buildRecapPrompt(sessionID, sessionDate, sessionNotes string) string {
	dateInstruction := ""
	if sessionDate != "" {
		dateInstruction = fmt.Sprintf(`
## Session Date
The session date is: %s
Use this EXACT date in all front matter "date:" fields. Format the title as "Session: <Month Year>" based on this date.
`, sessionDate)
	}

	notesSection := ""
	if sessionNotes != "" {
		notesSection = fmt.Sprintf(`
## Session Notes (from the DM/players)
The following notes were provided about this session. Use them for context when interpreting the transcript — they help clarify who is speaking, what's happening off-mic, and what to ignore in the audio.

%s

Use these notes to:
- Filter out non-game audio (e.g., talking to children, side conversations, phone calls)
- Understand the setting and context for this session
- Correctly attribute dialogue to the right characters
- Identify which parts of the transcript are actual gameplay vs. table chatter
`, sessionNotes)
	}

	return fmt.Sprintf(`You are generating content for a D&D campaign Hugo website.

%s
## Your Task
Read the session transcript at .pipeline/transcript.json and generate:
1. A session recap markdown file
2. Strategy guide markdown files
%s%s
## CRITICAL: Source of Truth
The transcript at .pipeline/transcript.json is your ONLY source of truth for what happened in this session.

- Derive ALL plot details, locations, NPCs, enemies, and events ONLY from the transcript
- Do NOT assume narrative continuity with previous sessions
- Do NOT reference events, locations, or NPCs from existing session recaps unless they are explicitly mentioned in THIS transcript
- Read existing session recaps ONLY for writing style and front matter format — NOT for plot context
- If the transcript is ambiguous about setting or context, describe only what the dialogue confirms
- Ignore any dialogue that is clearly not part of the game (talking to babies/children/pets, ordering food, phone calls, etc.)

## Instructions

### Session Recap
1. Read ONE existing session recap in content/sessions/ to understand the front matter format and writing style
2. Read the transcript JSON — it contains timestamped, speaker-labeled dialogue
3. Write a new session recap to content/sessions/session-%s.md
4. Match the existing front matter pattern exactly (title, date, description, session_name, session_time, session_summary, tags)
5. Write engaging narrative prose — not a dry transcript summary
6. Include combat highlights, character moments, and funny/dramatic beats
7. Reference characters by their character names (not player names)
8. The recap should stand alone — a reader should not need to have read previous sessions to understand it

### Strategy Guides
1. Read ONE existing strategy guide in content/strategy/ for style and format reference
2. Based on where THIS session ended (as described in the transcript), write 2-3 tactical options for next session
3. Create a directory content/strategy/session-%s/ with an _index.md and individual option files
4. Use descriptive slugs for option files (e.g., pursue-the-enemy.md, fortify-and-rest.md)
5. Each option should leverage the party's specific abilities
6. Base tactical analysis ONLY on information present in the transcript — do not invent enemies, locations, or situations not mentioned

### Character Reference
Character names and classes are provided in the prompt header above (via buildPartySection()). Use only those names and classes; do not invent stats or equipment not present in the transcript.

## Style
- Write in an engaging, narrative style — as if telling the story to someone who wasn't there
- D&D-specific terminology is encouraged
- Be specific about dice rolls, damage numbers, and ability uses mentioned in the transcript
- When in doubt about what happened, trust the transcript over any other source
`, buildPartySection(), dateInstruction, notesSection, sessionID, sessionID)
}
