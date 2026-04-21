package pipeline

import "time"

const TaskQueue = "session-processing"

// Known players for the campaign
var KnownPlayers = []Player{
	{Name: "Aelara", Character: "Aelara Silverleaf", Class: "Wood Elf Hunter Ranger"},
	{Name: "Brennan", Character: "Brennan Voss", Class: "Lore Bard"},
	{Name: "Corwin", Character: "Corwin Vex", Class: "War Magic Wizard"},
	{Name: "Delia", Character: "Delia Sunmantle", Class: "Life Domain Cleric"},
	{Name: "Edan", Character: "Edan Thornroot", Class: "Forest Druid"},
	{Name: "Fiora", Character: "Fiora Wardenheart", Class: "Paladin"},
	{Name: "Gareth", Role: "dm"},
}

// Pipeline constants
const (
	TranscriptionModel  = "small"
	ComputeType         = "int8"
	MinSpeakers         = 5
	MaxSpeakers         = 8
	HighlightPaddingMs  = 5000
	ClaudeModel         = "sonnet"
	AutoAcceptThreshold = 0.80
)

// ChunkLabelSeparator is used to join a chunk label prefix with a speaker label.
// e.g. "chunk_0:" + "SPEAKER_00" → "chunk_0:SPEAKER_00"
const ChunkLabelSeparator = ":"

// Player represents a known campaign participant.
type Player struct {
	Name      string `json:"name"`
	Character string `json:"character"`
	Class     string `json:"class,omitempty"`
	Role      string `json:"role,omitempty"`
}

// SessionProcessingInput is the top-level workflow input.
type SessionProcessingInput struct {
	RecordingPaths []string `json:"recording_paths"`
	SessionID      string   `json:"session_id"`
	SessionDate    string   `json:"session_date,omitempty"` // YYYY-MM-DD, resolved from metadata or signal
	NotesPath      string   `json:"notes_path,omitempty"`   // optional path to session notes file
}

// SessionProcessingResult is the top-level workflow output.
type SessionProcessingResult struct {
	SessionID      string `json:"session_id"`
	SessionDate    string `json:"session_date,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	RecapPath      string `json:"recap_path,omitempty"`
	HighlightCount int    `json:"highlight_count,omitempty"`
	PRURL          string `json:"pr_url,omitempty"`
}

// ExtractSessionDateInput is the input for reading recording metadata.
type ExtractSessionDateInput struct {
	RecordingPath string `json:"recording_path"`
}

// ExtractSessionDateResult holds the date extracted from recording metadata.
type ExtractSessionDateResult struct {
	Date  string `json:"date,omitempty"`  // YYYY-MM-DD if found
	Found bool   `json:"found"`
}

// ---------- Activity I/O types ----------

type ExtractAudioInput struct {
	RecordingPath string `json:"recording_path"`
	OutputPath    string `json:"output_path"`
}

type ExtractAudioResult struct {
	AudioPath      string  `json:"audio_path"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// ---------- Transcribe (Whisper only) ----------

type TranscribeAudioInput struct {
	AudioPath          string  `json:"audio_path"`
	Model              string  `json:"model"`
	ComputeType        string  `json:"compute_type"`
	OutputPath         string  `json:"output_path"`
	ModelDir           string  `json:"model_dir,omitempty"`
	StartOffsetSeconds float64 `json:"start_offset_seconds,omitempty"`
	ChunkIndex         int     `json:"chunk_index,omitempty"`
}

type TranscribeAudioResult struct {
	RawTranscriptPath  string  `json:"raw_transcript_path"`
	SegmentCount       int     `json:"segment_count"`
	DurationSeconds    float64 `json:"duration_seconds"`
	ChunkIndex         int     `json:"chunk_index,omitempty"`
	StartOffsetSeconds float64 `json:"start_offset_seconds,omitempty"`
}

// ---------- Diarize (pyannote + speaker assignment) ----------

type DiarizeInput struct {
	AudioPath          string  `json:"audio_path"`
	RawTranscriptPath  string  `json:"raw_transcript_path"`
	OutputPath         string  `json:"output_path"`
	MinSpeakers        int     `json:"min_speakers"`
	MaxSpeakers        int     `json:"max_speakers"`
	LabelPrefix        string  `json:"label_prefix,omitempty"`
	ComputeType        string  `json:"compute_type,omitempty"`
	StartOffsetSeconds float64 `json:"start_offset_seconds,omitempty"`
	ChunkIndex         int     `json:"chunk_index,omitempty"`
}

type DiarizeResult struct {
	TranscriptPath     string  `json:"transcript_path"`
	SpeakerCount       int     `json:"speaker_count"`
	SegmentCount       int     `json:"segment_count"`
	DurationSeconds    float64 `json:"duration_seconds,omitempty"`
	ChunkIndex         int     `json:"chunk_index,omitempty"`
	StartOffsetSeconds float64 `json:"start_offset_seconds,omitempty"`
}

// ---------- Raw transcript JSON mirror ----------

type RawTranscriptSegment struct {
	StartMs int64  `json:"start_ms"`
	EndMs   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

type RawTranscript struct {
	SchemaVersion      string                 `json:"schema_version"`
	SessionID          string                 `json:"session_id"`
	AudioFile          string                 `json:"audio_file"`
	DurationSeconds    float64                `json:"duration_seconds"`
	Provider           string                 `json:"provider"`
	Model              string                 `json:"model"`
	Quantization       string                 `json:"quantization"`
	StartOffsetSeconds float64                `json:"start_offset_seconds"`
	Segments           []RawTranscriptSegment `json:"segments"`
	Metadata           map[string]interface{} `json:"metadata,omitempty"`
}

// ---------- Chunking types ----------

type ChunkInfo struct {
	Index              int     `json:"index"`
	Path               string  `json:"path"`
	StartOffsetSeconds float64 `json:"start_offset_seconds"`
	DurationSeconds    float64 `json:"duration_seconds"`
	LabelPrefix        string  `json:"label_prefix"` // e.g. "chunk_0:"
}

type ConcatAndChunkInput struct {
	RecordingPaths          []string `json:"recording_paths"`
	ConcatOutputPath        string   `json:"concat_output_path"`
	ChunkOutputDir          string   `json:"chunk_output_dir"`
	TargetChunkSeconds      float64  `json:"target_chunk_seconds"`
	SilenceToleranceSeconds float64  `json:"silence_tolerance_seconds"`
}

type ConcatAndChunkResult struct {
	ConcatAudioPath      string      `json:"concat_audio_path"`
	TotalDurationSeconds float64     `json:"total_duration_seconds"`
	Chunks               []ChunkInfo `json:"chunks"`
}

type MergeTranscriptsInput struct {
	ChunkResults    []DiarizeResult `json:"chunk_results"`
	OutputPath      string          `json:"output_path"`
	SessionID       string          `json:"session_id"`
	ConcatAudioPath string          `json:"concat_audio_path"`
}

type MergeTranscriptsResult struct {
	TranscriptPath  string  `json:"transcript_path"`
	SpeakerCount    int     `json:"speaker_count"`
	SegmentCount    int     `json:"segment_count"`
	DurationSeconds float64 `json:"duration_seconds"`
}

type SpeakerMatch struct {
	SpeakerLabel string  `json:"speaker_label"`
	PlayerName   string  `json:"player_name"`
	Character    string  `json:"character"`
	Confidence   float64 `json:"confidence"`
}

type ProposeMappingsInput struct {
	TranscriptPath string   `json:"transcript_path"`
	KnownPlayers   []Player `json:"known_players"`
	ClaudeOptions  ClaudeOptions `json:"claude_options"`
}

type ProposeMappingsResult struct {
	Mappings   []SpeakerMatch `json:"mappings"`
	Confidence float64        `json:"confidence"`
	Reasoning  string         `json:"reasoning"`
}

type ApplyMappingsInput struct {
	TranscriptPath string         `json:"transcript_path"`
	Mappings       []SpeakerMatch `json:"mappings"`
	OutputPath     string         `json:"output_path"`
}

type ApplyMappingsResult struct {
	MappedTranscriptPath string `json:"mapped_transcript_path"`
	MappedSpeakers       int    `json:"mapped_speakers"`
}

type PrepareWorkspaceInput struct {
	SessionID string        `json:"session_id"`
	RepoURL   string        `json:"repo_url"`
	Options   ClaudeOptions `json:"options"`
}

type PrepareWorkspaceResult struct {
	WorkspacePath string `json:"workspace_path"`
	BranchName    string `json:"branch_name"`
}

type GenerateRecapInput struct {
	TranscriptPath string        `json:"transcript_path"`
	SessionID      string        `json:"session_id"`
	SessionDate    string        `json:"session_date,omitempty"`
	WorkspacePath  string        `json:"workspace_path"`
	NotesPath      string        `json:"notes_path,omitempty"`
	ClaudeOptions  ClaudeOptions `json:"claude_options"`
}

type DetectHighlightsInput struct {
	TranscriptPath string        `json:"transcript_path"`
	SessionID      string        `json:"session_id"`
	NotesPath      string        `json:"notes_path,omitempty"`
	ClaudeOptions  ClaudeOptions `json:"claude_options"`
}

type DetectHighlightsResult struct {
	Highlights []Highlight `json:"highlights"`
}

type Highlight struct {
	ID                string   `json:"id"`
	Type              string   `json:"type"`
	Title             string   `json:"title"`
	Description       string   `json:"description"`
	StartMs           int64    `json:"start_ms"`
	EndMs             int64    `json:"end_ms"`
	Speakers          []string `json:"speakers"`
	TranscriptExcerpt string   `json:"transcript_excerpt"`
	ClipPath          string   `json:"clip_path,omitempty"`
	Tags              []string `json:"tags,omitempty"`
}

type ExtractClipsInput struct {
	AudioPath  string      `json:"audio_path"`
	Highlights []Highlight `json:"highlights"`
	OutputDir  string      `json:"output_dir"`
	PaddingMs  int         `json:"padding_ms"`
}

type ExtractClipsResult struct {
	Clips []Highlight `json:"clips"`
}

type AssembleReelInput struct {
	Clips     []Highlight `json:"clips"`
	OutputPath string     `json:"output_path"`
}

type AssembleReelResult struct {
	ReelPath        string  `json:"reel_path"`
	DurationSeconds float64 `json:"duration_seconds"`
}

type GenerateHighlightContentInput struct {
	Highlights    []Highlight   `json:"highlights"`
	SessionID     string        `json:"session_id"`
	WorkspacePath string        `json:"workspace_path"`
	ClaudeOptions ClaudeOptions `json:"claude_options"`
}

type CommitAndPushInput struct {
	WorkspacePath string `json:"workspace_path"`
	BranchName    string `json:"branch_name"`
	CommitMessage string `json:"commit_message"`
}

type CommitAndPushResult struct {
	CommitSHA string `json:"commit_sha"`
}

type CreatePRInput struct {
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	BranchName string `json:"branch_name"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	GHToken    string `json:"-"`
}

type PRResult struct {
	PRURL    string `json:"pr_url"`
	PRNumber int    `json:"pr_number"`
}

// ---------- Claude integration ----------

type ClaudeOptions struct {
	RepoURL      string   `json:"repo_url,omitempty"`
	GHToken      string   `json:"-"`
	OAuthToken   string   `json:"-"`
	Model        string   `json:"model,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Owner        string   `json:"owner,omitempty"`
	Repo         string   `json:"repo,omitempty"`
	BranchName   string   `json:"branch_name,omitempty"`
}

type ClaudeAnalysisResult struct {
	Analysis         string   `json:"analysis"`
	ChangedFilePaths []string `json:"changed_file_paths,omitempty"`
	TokensUsed       int      `json:"tokens_used,omitempty"`
	SessionID        string   `json:"session_id,omitempty"`
	ConversationLog  string   `json:"conversation_log,omitempty"`
}

// ---------- Voice profiles ----------

type VoiceProfile struct {
	PlayerName       string           `json:"player_name"`
	CharacterName    string           `json:"character_name"`
	SessionsCount    int              `json:"sessions_count"`
	SpeakingPatterns SpeakingPatterns `json:"speaking_patterns"`
	LastUpdated      time.Time        `json:"last_updated"`
}

type SpeakingPatterns struct {
	AvgWordsPerTurn    float64  `json:"avg_words_per_turn"`
	AvgTurnDurationMs  float64  `json:"avg_turn_duration_ms"`
	SpeakingRatio      float64  `json:"speaking_ratio"`
	FrequentPhrases    []string `json:"frequent_phrases,omitempty"`
}

// ---------- Transcript types ----------

type Segment struct {
	Speaker         string  `json:"speaker"`
	ResolvedSpeaker string  `json:"resolved_speaker,omitempty"`
	StartMs         int64   `json:"start_ms"`
	EndMs           int64   `json:"end_ms"`
	Text            string  `json:"text"`
	Confidence      float64 `json:"confidence"`
}

type SessionTranscript struct {
	SessionID       string            `json:"session_id"`
	RecordingFile   string            `json:"recording_file"`
	DurationSeconds float64           `json:"duration_seconds"`
	Provider        string            `json:"provider"`
	Model           string            `json:"model"`
	Speakers        []string          `json:"speakers"`
	Segments        []Segment         `json:"segments"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}
