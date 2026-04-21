package pipeline

import (
	"go.temporal.io/sdk/worker"
)

// RegisterWorkflowAndActivities registers the pipeline workflow and all
// activity implementations with the given Temporal worker.
func RegisterWorkflowAndActivities(w worker.Worker) {
	w.RegisterWorkflow(SessionProcessingWorkflow)
	w.RegisterActivity(ConcatAndChunkAudioActivity)
	w.RegisterActivity(ExtractSessionDateActivity)
	w.RegisterActivity(ExtractAudioActivity)
	w.RegisterActivity(TranscribeAudioActivity)
	w.RegisterActivity(DiarizeAudioActivity)
	w.RegisterActivity(MergeTranscriptsActivity)
	w.RegisterActivity(ProposeSpeakerMappingsActivity)
	w.RegisterActivity(ApplySpeakerMappingsActivity)
	w.RegisterActivity(PrepareWorkspaceActivity)
	w.RegisterActivity(GenerateRecapActivity)
	w.RegisterActivity(DetectHighlightsActivity)
	w.RegisterActivity(ExtractClipsActivity)
	w.RegisterActivity(AssembleReelActivity)
	w.RegisterActivity(GenerateHighlightContentActivity)
	w.RegisterActivity(CommitAndPushActivity)
	w.RegisterActivity(CreatePRActivity)
}
