package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.temporal.io/sdk/client"

	"dio/internal/pipeline"
	temporalClient "dio/internal/temporal"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "process":
		if len(os.Args) < 3 {
			fmt.Println("Usage: sessionctl process <recording-path> [<more-paths>...] [session-id] [--notes <path>] [--date YYYY-MM-DD]")
			fmt.Println()
			fmt.Println("Start a session processing workflow via Temporal.")
			fmt.Println("Multiple recording paths are treated as consecutive audio files for one session.")
			fmt.Println()
			fmt.Println("Options:")
			fmt.Println("  --notes <path>      Path to session notes file (context for AI)")
			fmt.Println("  --date YYYY-MM-DD   Session date (skips metadata extraction)")
			fmt.Println()
			fmt.Println("Examples:")
			fmt.Println("  # Single file")
			fmt.Println("  sessionctl process /data/recordings/session.wav --date 2026-04-17")
			fmt.Println()
			fmt.Println("  # Multiple files from a device that split recording at the 4GB WAV limit")
			fmt.Println("  sessionctl process /data/recordings/a.WAV /data/recordings/b.WAV /data/recordings/c.WAV --date 2026-04-17")
			fmt.Println()
			fmt.Println("Environment variables:")
			fmt.Println("  TEMPORAL_HOST - Temporal frontend address (default: localhost:7233)")
			os.Exit(1)
		}
		runProcess(os.Args[2:])
	case "confirm-speakers":
		if len(os.Args) < 3 {
			fmt.Println("Usage: sessionctl confirm-speakers <workflow-id> [--mappings <json>]")
			fmt.Println()
			fmt.Println("Send speaker confirmation signal to a running workflow.")
			os.Exit(1)
		}
		runConfirmSpeakers(os.Args[2:])
	case "set-date":
		if len(os.Args) < 4 {
			fmt.Println("Usage: sessionctl set-date <workflow-id> <YYYY-MM-DD>")
			fmt.Println()
			fmt.Println("Send session date signal to a running workflow.")
			os.Exit(1)
		}
		runSetDate(os.Args[2], os.Args[3])
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Session Processing CLI")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  process <recording-path> [id]     - Start session processing workflow")
	fmt.Println("  confirm-speakers <workflow-id>    - Send speaker confirmation signal")
	fmt.Println("  set-date <workflow-id> <date>     - Send session date (YYYY-MM-DD)")
	fmt.Println()
	fmt.Println("Environment variables:")
	fmt.Println("  TEMPORAL_HOST      - Temporal frontend address (default: localhost:7233)")
	fmt.Println("  PIPELINE_DATA_DIR  - Data directory (default: /data)")
	fmt.Println("  PIPELINE_GH_OWNER  - GitHub owner for the target repo")
	fmt.Println("  PIPELINE_GH_REPO   - GitHub repo name for the target repo")
}

// isRecordingPath reports whether arg looks like an audio file path.
func isRecordingPath(arg string) bool {
	if strings.ContainsAny(arg, `/\`) {
		return true
	}
	lower := strings.ToLower(arg)
	for _, ext := range []string{".wav", ".flac", ".mp3", ".mp4", ".m4a"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

func runProcess(args []string) {
	var recordingPaths []string
	sessionID := ""
	notesPath := ""
	sessionDate := ""

	// Collect path args first; stop at first flag. Then parse flags and
	// session-id from the remainder.
	seenFlag := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "--") {
			seenFlag = true
			switch arg {
			case "--notes":
				if i+1 < len(args) {
					i++
					notesPath = args[i]
				}
			case "--date":
				if i+1 < len(args) {
					i++
					sessionDate = args[i]
				}
			}
			continue
		}
		if !seenFlag && isRecordingPath(arg) {
			recordingPaths = append(recordingPaths, arg)
			continue
		}
		// Bare token: first one is sessionID, extras are warned and ignored.
		if sessionID == "" {
			sessionID = arg
		} else {
			fmt.Fprintf(os.Stderr, "warning: ignoring extra positional arg %q\n", arg)
		}
	}

	if len(recordingPaths) == 0 {
		fmt.Fprintln(os.Stderr, "Error: at least one recording path is required")
		os.Exit(1)
	}

	if sessionID == "" {
		base := filepath.Base(recordingPaths[0])
		ext := filepath.Ext(base)
		sessionID = strings.TrimSuffix(base, ext)
		sessionID = strings.TrimPrefix(sessionID, "session-")
	}

	c, err := temporalClient.NewClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	workflowOpts := client.StartWorkflowOptions{
		ID:        fmt.Sprintf("session-%s", sessionID),
		TaskQueue: pipeline.TaskQueue,
	}

	input := pipeline.SessionProcessingInput{
		RecordingPaths: recordingPaths,
		SessionID:      sessionID,
		SessionDate:    sessionDate,
		NotesPath:      notesPath,
	}

	we, err := c.ExecuteWorkflow(context.Background(), workflowOpts, pipeline.SessionProcessingWorkflow, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting workflow: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Workflow started!\n")
	fmt.Printf("  Workflow ID: %s\n", we.GetID())
	fmt.Printf("  Run ID:      %s\n", we.GetRunID())
	fmt.Printf("  Session:     %s\n", sessionID)
	fmt.Printf("\nMonitor at: Temporal UI or run:\n")
	fmt.Printf("  sessionctl confirm-speakers %s\n", we.GetID())
}

func runConfirmSpeakers(args []string) {
	workflowID := args[0]

	var mappings []pipeline.SpeakerMatch
	for i, arg := range args {
		if arg == "--mappings" && i+1 < len(args) {
			if err := json.Unmarshal([]byte(args[i+1]), &mappings); err != nil {
				fmt.Fprintf(os.Stderr, "Error parsing mappings JSON: %v\n", err)
				os.Exit(1)
			}
		}
	}

	c, err := temporalClient.NewClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	err = c.SignalWorkflow(context.Background(), workflowID, "", "ConfirmSpeakers", mappings)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error sending signal: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Speaker confirmation signal sent to workflow %s\n", workflowID)
}

func runSetDate(workflowID, date string) {
	c, err := temporalClient.NewClient()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	err = c.SignalWorkflow(context.Background(), workflowID, "", "SetSessionDate", date)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error sending signal: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Session date %s sent to workflow %s\n", date, workflowID)
}
