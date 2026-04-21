package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// SessionProcessingWorkflow orchestrates the full session processing pipeline.
func SessionProcessingWorkflow(ctx workflow.Context, input SessionProcessingInput) (*SessionProcessingResult, error) {
	logger := workflow.GetLogger(ctx)
	logger.Info("Session workflow started", "session_id", input.SessionID, "recording", input.RecordingPaths[0])

	// Resolve data directory — /data on k8s (PV mount), configurable via PIPELINE_DATA_DIR
	var dataDir string
	_ = workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
		if d := os.Getenv("PIPELINE_DATA_DIR"); d != "" {
			return d
		}
		return "/data"
	}).Get(&dataDir)

	// Activity options
	shortOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 15 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}
	longOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 6 * time.Hour,
		HeartbeatTimeout:    2 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}

	// Step 0: Resolve session date from recording metadata or signal
	sessionDate := input.SessionDate
	if sessionDate == "" {
		var dateResult ExtractSessionDateResult
		dateCtx := workflow.WithActivityOptions(ctx, shortOpts)
		if err := workflow.ExecuteActivity(dateCtx, ExtractSessionDateActivity, ExtractSessionDateInput{
			RecordingPath: input.RecordingPaths[0],
		}).Get(ctx, &dateResult); err != nil {
			logger.Warn("Failed to extract date from metadata", "error", err)
		} else if dateResult.Found {
			sessionDate = dateResult.Date
			logger.Info("Session date from recording metadata", "date", sessionDate)
		}

		if sessionDate == "" {
			logger.Info("No session date found in metadata, waiting for SetSessionDate signal",
				"hint", "Send signal with: dio set-date <workflow-id> <YYYY-MM-DD>",
			)
			signalChan := workflow.GetSignalChannel(ctx, "SetSessionDate")
			signalChan.Receive(ctx, &sessionDate)
			logger.Info("Session date received via signal", "date", sessionDate)
		}
	}

	// Step 1: Concat + chunk audio into silence-aligned ~20-minute segments.
	concatPath := filepath.Join(dataDir, "audio", input.SessionID+".wav")
	chunkDir := filepath.Join(dataDir, "chunks", input.SessionID)
	var chunkResult ConcatAndChunkResult
	chunkCtx := workflow.WithActivityOptions(ctx, longOpts) // chunking 4.8 GiB can take minutes
	if err := workflow.ExecuteActivity(chunkCtx, ConcatAndChunkAudioActivity, ConcatAndChunkInput{
		RecordingPaths:          input.RecordingPaths,
		ConcatOutputPath:        concatPath,
		ChunkOutputDir:          chunkDir,
		TargetChunkSeconds:      1200,
		SilenceToleranceSeconds: 60,
	}).Get(ctx, &chunkResult); err != nil {
		return nil, fmt.Errorf("concat and chunk: %w", err)
	}
	logger.Info("Audio chunked",
		"chunks", len(chunkResult.Chunks),
		"total_duration_seconds", chunkResult.TotalDurationSeconds,
		"concat_path", chunkResult.ConcatAudioPath,
	)

	// Step 2: Per chunk, run Whisper then pyannote as separate activities so
	// each can retry independently. Pyannote + Whisper is memory-bound;
	// parallel execution across chunks would OOM the 8 GiB worker.
	chunkDiarizations := make([]DiarizeResult, 0, len(chunkResult.Chunks))
	activityCtx := workflow.WithActivityOptions(ctx, longOpts)
	for _, chunk := range chunkResult.Chunks {
		rawPath := filepath.Join(dataDir, "transcripts", input.SessionID, fmt.Sprintf("chunk-%02d.raw.json", chunk.Index))
		finalPath := filepath.Join(dataDir, "transcripts", input.SessionID, fmt.Sprintf("chunk-%02d.json", chunk.Index))

		// Step 2a: Whisper transcription (per chunk)
		var tr TranscribeAudioResult
		if err := workflow.ExecuteActivity(activityCtx, TranscribeAudioActivity, TranscribeAudioInput{
			AudioPath:          chunk.Path,
			Model:              TranscriptionModel,
			ComputeType:        ComputeType,
			OutputPath:         rawPath,
			ModelDir:           filepath.Join(dataDir, "models"),
			StartOffsetSeconds: chunk.StartOffsetSeconds,
			ChunkIndex:         chunk.Index,
		}).Get(ctx, &tr); err != nil {
			return nil, fmt.Errorf("transcribe chunk %d: %w", chunk.Index, err)
		}

		// Step 2b: pyannote diarization (per chunk)
		var dr DiarizeResult
		if err := workflow.ExecuteActivity(activityCtx, DiarizeAudioActivity, DiarizeInput{
			AudioPath:          chunk.Path,
			RawTranscriptPath:  tr.RawTranscriptPath,
			OutputPath:         finalPath,
			MinSpeakers:        MinSpeakers,
			MaxSpeakers:        MaxSpeakers,
			LabelPrefix:        chunk.LabelPrefix,
			ComputeType:        ComputeType,
			StartOffsetSeconds: chunk.StartOffsetSeconds,
			ChunkIndex:         chunk.Index,
		}).Get(ctx, &dr); err != nil {
			return nil, fmt.Errorf("diarize chunk %d: %w", chunk.Index, err)
		}
		// Defensive: populate bookkeeping in case activity didn't echo
		dr.ChunkIndex = chunk.Index
		dr.StartOffsetSeconds = chunk.StartOffsetSeconds
		chunkDiarizations = append(chunkDiarizations, dr)
	}

	// Step 3: Merge per-chunk transcripts into a single session transcript.
	mergedPath := filepath.Join(dataDir, "transcripts", input.SessionID+".json")
	var mergeResult MergeTranscriptsResult
	mergeCtx := workflow.WithActivityOptions(ctx, shortOpts)
	if err := workflow.ExecuteActivity(mergeCtx, MergeTranscriptsActivity, MergeTranscriptsInput{
		ChunkResults:    chunkDiarizations,
		OutputPath:      mergedPath,
		SessionID:       input.SessionID,
		ConcatAudioPath: chunkResult.ConcatAudioPath,
	}).Get(ctx, &mergeResult); err != nil {
		return nil, fmt.Errorf("merge transcripts: %w", err)
	}

	logger.Info("Transcription complete", "segments", mergeResult.SegmentCount, "speakers", mergeResult.SpeakerCount)

	// Step 4: Propose speaker mappings
	var proposeResult ProposeMappingsResult
	proposeCtx := workflow.WithActivityOptions(ctx, shortOpts)
	if err := workflow.ExecuteActivity(proposeCtx, ProposeSpeakerMappingsActivity, ProposeMappingsInput{
		TranscriptPath: mergeResult.TranscriptPath,
		KnownPlayers:   KnownPlayers,
	}).Get(ctx, &proposeResult); err != nil {
		return nil, fmt.Errorf("propose speaker mappings: %w", err)
	}
	logger.Info("Speaker mappings proposed",
		"mappings", len(proposeResult.Mappings),
		"confidence", proposeResult.Confidence,
	)

	// If confidence is below threshold, wait for human confirmation via Signal.
	confirmedMappings := proposeResult.Mappings
	if proposeResult.Confidence < AutoAcceptThreshold {
		logger.Info("Speaker confidence below threshold, waiting for confirmation signal",
			"confidence", proposeResult.Confidence,
			"threshold", AutoAcceptThreshold,
		)

		signalChan := workflow.GetSignalChannel(ctx, "ConfirmSpeakers")
		signalChan.Receive(ctx, &confirmedMappings)
		logger.Info("Speaker confirmation received via signal")
	} else {
		logger.Info("All speakers auto-accepted", "confidence", proposeResult.Confidence)
	}

	// Step 5: Apply confirmed speaker mappings to transcript
	var applyResult ApplyMappingsResult
	applyCtx := workflow.WithActivityOptions(ctx, shortOpts)
	if err := workflow.ExecuteActivity(applyCtx, ApplySpeakerMappingsActivity, ApplyMappingsInput{
		TranscriptPath: mergeResult.TranscriptPath,
		Mappings:       confirmedMappings,
		OutputPath:     mergeResult.TranscriptPath, // overwrite in place
	}).Get(ctx, &applyResult); err != nil {
		return nil, fmt.Errorf("apply speaker mappings: %w", err)
	}
	logger.Info("Speaker mappings applied", "mapped_speakers", applyResult.MappedSpeakers)

	// Read env vars deterministically via SideEffect
	var ghOwner string
	_ = workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
		return os.Getenv("PIPELINE_GH_OWNER")
	}).Get(&ghOwner)
	var ghRepo string
	_ = workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
		return os.Getenv("PIPELINE_GH_REPO")
	}).Get(&ghRepo)

	claudeOpts := ClaudeOptions{
		Model:        ClaudeModel,
		AllowedTools: []string{"Read", "Glob", "Grep", "Edit", "Write"},
		Owner:        ghOwner,
		Repo:         ghRepo,
	}

	claudeActivityOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 90 * time.Minute,
		HeartbeatTimeout:    5 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	}

	// Step 6: Prepare workspace (clone repo)
	var workspaceResult PrepareWorkspaceResult
	prepCtx := workflow.WithActivityOptions(ctx, shortOpts)
	if err := workflow.ExecuteActivity(prepCtx, PrepareWorkspaceActivity, PrepareWorkspaceInput{
		SessionID: input.SessionID,
		Options:   claudeOpts,
	}).Get(ctx, &workspaceResult); err != nil {
		return nil, fmt.Errorf("prepare workspace: %w", err)
	}

	// Steps 7+8: Parallel Claude activities (recap + highlight detection)
	var recapResult *ClaudeAnalysisResult
	var highlightResult *DetectHighlightsResult
	var recapErr, highlightErr error
	var recapDone, highlightDone bool

	workflow.Go(ctx, func(gCtx workflow.Context) {
		recapCtx := workflow.WithActivityOptions(gCtx, claudeActivityOpts)
		recapErr = workflow.ExecuteActivity(recapCtx, GenerateRecapActivity, GenerateRecapInput{
			TranscriptPath: applyResult.MappedTranscriptPath,
			SessionID:      input.SessionID,
			SessionDate:    sessionDate,
			WorkspacePath:  workspaceResult.WorkspacePath,
			NotesPath:      input.NotesPath,
			ClaudeOptions:  claudeOpts,
		}).Get(gCtx, &recapResult)
		recapDone = true
	})

	workflow.Go(ctx, func(gCtx workflow.Context) {
		highlightCtx := workflow.WithActivityOptions(gCtx, claudeActivityOpts)
		highlightErr = workflow.ExecuteActivity(highlightCtx, DetectHighlightsActivity, DetectHighlightsInput{
			TranscriptPath: applyResult.MappedTranscriptPath,
			SessionID:      input.SessionID,
			NotesPath:      input.NotesPath,
			ClaudeOptions:  claudeOpts,
		}).Get(gCtx, &highlightResult)
		highlightDone = true
	})

	_ = workflow.Await(ctx, func() bool { return recapDone && highlightDone })

	if recapErr != nil {
		return nil, fmt.Errorf("generate recap: %w", recapErr)
	}
	if highlightErr != nil {
		return nil, fmt.Errorf("detect highlights: %w", highlightErr)
	}

	logger.Info("Claude analysis complete",
		"recap_tokens", recapResult.TokensUsed,
		"highlights", len(highlightResult.Highlights),
	)

	highlightCount := len(highlightResult.Highlights)

	// Step 9: Extract clips
	clipOutputDir := filepath.Join(dataDir, "clips", input.SessionID)
	var clipsResult ExtractClipsResult
	clipOpts := workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Minute,
		HeartbeatTimeout:    1 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
	}
	clipCtx := workflow.WithActivityOptions(ctx, clipOpts)
	if err := workflow.ExecuteActivity(clipCtx, ExtractClipsActivity, ExtractClipsInput{
		// Clip extraction cuts by session-absolute timestamps; it must
		// operate on the concatenated full-session audio, not the first
		// input fragment.
		AudioPath:  chunkResult.ConcatAudioPath,
		Highlights: highlightResult.Highlights,
		OutputDir:  clipOutputDir,
		PaddingMs:  HighlightPaddingMs,
	}).Get(ctx, &clipsResult); err != nil {
		logger.Warn("Clip extraction failed, continuing without clips", "error", err)
	}

	// Step 10: Assemble highlight reel
	var reelResult AssembleReelResult
	if len(clipsResult.Clips) > 0 {
		reelPath := filepath.Join(clipOutputDir, "highlight-reel.mp4")
		reelCtx := workflow.WithActivityOptions(ctx, shortOpts)
		if err := workflow.ExecuteActivity(reelCtx, AssembleReelActivity, AssembleReelInput{
			Clips:      clipsResult.Clips,
			OutputPath: reelPath,
		}).Get(ctx, &reelResult); err != nil {
			logger.Warn("Reel assembly failed", "error", err)
		}
	}

	// Step 11: Generate highlight content pages
	if highlightCount > 0 {
		highlightContentCtx := workflow.WithActivityOptions(ctx, claudeActivityOpts)
		var highlightContentResult ClaudeAnalysisResult
		// Use clips with embedded clip_path if available, fall back to raw highlights
		highlightsForContent := highlightResult.Highlights
		if len(clipsResult.Clips) > 0 {
			highlightsForContent = clipsResult.Clips
		}
		if err := workflow.ExecuteActivity(highlightContentCtx, GenerateHighlightContentActivity, GenerateHighlightContentInput{
			Highlights:    highlightsForContent,
			SessionID:     input.SessionID,
			WorkspacePath: workspaceResult.WorkspacePath,
			ClaudeOptions: claudeOpts,
		}).Get(ctx, &highlightContentResult); err != nil {
			logger.Warn("Highlight content generation failed", "error", err)
		}
	}

	// Step 11: Commit and push
	var commitResult CommitAndPushResult
	commitCtx := workflow.WithActivityOptions(ctx, shortOpts)
	commitMsg := fmt.Sprintf("feat: add session %s artifacts", input.SessionID)
	if err := workflow.ExecuteActivity(commitCtx, CommitAndPushActivity, CommitAndPushInput{
		WorkspacePath: workspaceResult.WorkspacePath,
		BranchName:    workspaceResult.BranchName,
		CommitMessage: commitMsg,
	}).Get(ctx, &commitResult); err != nil {
		return nil, fmt.Errorf("commit and push: %w", err)
	}

	// Step 12: Create PR
	var prResult PRResult
	if commitResult.CommitSHA != "" {
		prCtx := workflow.WithActivityOptions(ctx, shortOpts)
		prBody := fmt.Sprintf("## Session %s\n\nAuto-generated session content:\n- Session recap\n- Strategy guides\n- %d highlight moments\n\nGenerated by the session processing pipeline.", input.SessionID, highlightCount)
		if err := workflow.ExecuteActivity(prCtx, CreatePRActivity, CreatePRInput{
			Owner:      claudeOpts.Owner,
			Repo:       claudeOpts.Repo,
			BranchName: workspaceResult.BranchName,
			Title:      fmt.Sprintf("Session %s artifacts", input.SessionID),
			Body:       prBody,
		}).Get(ctx, &prResult); err != nil {
			logger.Warn("PR creation failed", "error", err)
		}
	}

	logger.Info("Session workflow complete",
		"session_id", input.SessionID,
		"pr_url", prResult.PRURL,
		"highlights", highlightCount,
		"reel_duration", reelResult.DurationSeconds,
	)

	return &SessionProcessingResult{
		SessionID:      input.SessionID,
		SessionDate:    sessionDate,
		TranscriptPath: applyResult.MappedTranscriptPath,
		HighlightCount: highlightCount,
		PRURL:          prResult.PRURL,
	}, nil
}
