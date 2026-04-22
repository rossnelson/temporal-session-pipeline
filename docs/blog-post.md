---
title: "From 5 hours of D&D audio to a pull request: a durable pipeline with Temporal and Claude"
description: "A weekend side-project turned into a practical tour of durable execution, signals, heartbeats, and parallel activities, using Temporal to turn raw session recordings into recaps, highlight reels, and PRs."
tags: [How-To, Temporal Concepts, AI]
---

Once a month, seven of us play Dungeons & Dragons. For about 8 months I tried to write session recaps from memory. They were slow and laborious.

Then I picked up a [Zoom H1 Essential](https://zoomcorp.com/en/us/handheld-recorders/handheld-recorders/h1essential/)
and started leaving it running on the table for the whole session. Suddenly I
had 3-5 hours of raw audio: overlapping voices, combat,
tangents about snacks, a very vocal toddler. Having the recording didn't solve
the problem; it just moved it. With audio in hand I knew I could automate the
process.

So I wired up a pipeline: drop the `.wav` files from the H1 onto a server, wait
a few hours, get a pull request on the campaign site with a session recap, a
strategy guide, a set of highlight pages with embedded audio clips, and a
90-second highlight reel. No manual steps for the happy path.

It's a fun toy, but what made it possible to ship in a weekend, and what has
kept it running reliably across GPU OOMs, model-download timeouts, and at least
one cosmic-ray-grade ffmpeg bug, is that the whole thing is a single Temporal
workflow.

Below I'll walk through six Temporal primitives that did the heavy lifting: long
`StartToCloseTimeout` paired with short `HeartbeatTimeout`, retry policies tuned
per activity class, signals for human-in-the-loop, `workflow.Go` and
`workflow.Await` for parallel activities, `SideEffect` for deterministic env
reads, and the Claude Code CLI wrapped as an activity. The D&D framing is
incidental; the patterns port cleanly to anything long-running and
heterogeneous.

The sanitized reference implementation lives at
[github.com/rossnelson/temporal-session-pipeline](https://github.com/rossnelson/temporal-session-pipeline).
The original project is written
in Go using the Temporal Go SDK; the same patterns translate directly to
TypeScript and Python.

## One box in my closet

I currently have the whole pipeline running on a single Dell tower tucked in a closet:

a mid-range CPU, 32 GiB RAM, no GPU, one SSD, running a k3s single-node cluster.
The recap and highlight activities shell out to the Claude Code CLI against my
existing Claude subscription, so there's no metered cloud bill tied to pipeline
runs.

That hardware constraint is the interesting part of the design, not a footnote
to it. Nearly every choice in the pipeline falls out of "one small box, let's
see how much it can do":

- **Small Whisper model.** I use `whisper-small` with `int8` quantization rather than `large-v3`. Word accuracy drops a few points; runtime drops by an order of magnitude. Good enough for session recaps, and it fits in RAM without swapping.
- **Sequential transcription.** Running pyannote diarization and Whisper decoding for two chunks at once will OOM the worker. One chunk at a time is slower but always finishes.
- **Long timeouts as a first-class design choice.** `StartToCloseTimeout: 6 * time.Hour` isn't a safety margin: it's the budget. I'd rather wait four hours and succeed than try to go fast and fail.
- **Heartbeats over throughput.** Because activities run long, progress visibility matters more than raw speed. Heartbeats are cheap; a silent 90-minute activity is not.

I've run in roughly every conceivable environment over the last 20 years. What
Temporal adds that those setups didn't is that recovery from the inevitable
failures is free. A stuck activity, a pod OOM, a late-night power flicker: the
workflow picks up where it left off and I didn't have to write that glue logic.
On the others I'd have hand-rolled a resume-from-last-checkpoint script and tested
it poorly. Here I didn't have to.

## The pipeline

Here's the shape of the workflow, start to finish:

1. **Resolve session date** from the recording's embedded metadata, or wait on a signal if absent.
2. **Concatenate and chunk** the raw audio (often 4+ GiB split across multiple WAV files) into ~20-minute segments, silence-aligned.
3. **Transcribe each chunk** with Whisper + pyannote diarization, emitting per-speaker segments.
4. **Merge** per-chunk transcripts into a single session transcript with absolute timestamps.
5. **Propose speaker → player mappings** using an LLM over the transcript plus a roster of known players; if confidence is low, wait on a human confirmation signal.
6. **Apply mappings** to replace `SPEAKER_00` with `Aelara Silverleaf (Aelara)` throughout.
7. **In parallel**, run two expensive LLM activities: one writes the session recap, the other detects highlight-worthy moments with their timestamp ranges.
8. **Extract audio clips** for each highlight from the concatenated audio.
9. **Assemble a highlight reel** from the clips.
10. **Generate highlight content pages** in the repo.
11. **Commit, push, open a PR.**

From the operator's perspective, steps 1 through 11 look like a single command:

```bash
./dio process /data/recordings/session.wav --date 2026-04-17
```

Under the hood that's one call to `client.ExecuteWorkflow`. Let's look at the parts that are actually interesting.

## Long-running activities earn heartbeats

Transcription with Whisper on a session this long is not fast. Each 20-minute chunk takes 8–15 minutes of wall clock on a CPU-only worker. A full session is 15 chunks, so the transcription phase alone can run for over two hours.

Two things have to be configured correctly or Temporal will start making bad assumptions about liveness:

```go
longOpts := workflow.ActivityOptions{
    StartToCloseTimeout: 6 * time.Hour,
    HeartbeatTimeout:    2 * time.Minute,
    RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
}
```

The mental model: `StartToCloseTimeout` is the budget for the whole activity. `HeartbeatTimeout` is how long the worker is allowed to go silent before Temporal gives up on the current attempt and schedules a retry, even though the overall timeout is still hours away. That's the behavior you want. A stuck Python subprocess should be caught in minutes, not hours.

The activity itself reads JSON progress events from the child process and heartbeats on each one:

```go
scanner := bufio.NewScanner(stdout)
for scanner.Scan() {
    var p transcribeProgress
    if err := json.Unmarshal(scanner.Bytes(), &p); err != nil {
        continue
    }
    activity.RecordHeartbeat(ctx, p)
}
```

The heartbeat payload gets surfaced in the Temporal UI, so while a chunk is transcribing you can see `{"percent": 38, "segments": 412}` updating in real time. For a pipeline that runs for hours, that visibility is worth the ten lines of code it took to plumb it through.

## Retries that actually help

The three retry attempts in `longOpts` above aren't cargo-culted. The real failure modes on this pipeline are overwhelmingly transient:

- Hugging Face returning 503 while pyannote downloads a model on cold start.
- ffmpeg occasionally hitting an ALSA timing bug on the first pass but succeeding on the second.
- The worker pod getting OOM-killed because the previous activity didn't release a tensor.

None of those are worth writing bespoke error handling for. They're worth saying "try three times, then fail loudly" and moving on. `MaximumAttempts: 3` covers all of it. When the third attempt fails, you get a clean stack trace in the workflow history and can debug from there.

The clip extraction step gets a different policy because ffmpeg failures there usually mean a malformed highlight range, which won't improve with retries:

```go
clipOpts := workflow.ActivityOptions{
    StartToCloseTimeout: 30 * time.Minute,
    HeartbeatTimeout:    1 * time.Minute,
    RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
}
```

Different steps, different policies, and they live right next to the code that uses them, not in a central config file.

## Humans in the loop via signals

Diarization is not a solved problem. If two players have similar voices and one is new to the campaign, the LLM pass that matches `SPEAKER_03` to a player might return 60% confidence instead of 95%. That's too low to auto-accept, and the right answer is to stop and ask a human.

Temporal signals make this almost trivial. The workflow computes a confidence score, and if it's below threshold:

```go
if proposeResult.Confidence < AutoAcceptThreshold {
    notifySlack(mappingsTable, "ConfirmSpeakers")
    signalChan := workflow.GetSignalChannel(ctx, "ConfirmSpeakers")
    signalChan.Receive(ctx, &confirmedMappings)
}
```

The mental model: a workflow waiting on a signal isn't code running somewhere. It's a durable checkpoint in the Temporal cluster. `signalChan.Receive` blocks the workflow (not a goroutine, the *workflow*) until a signal arrives. In the meantime the worker pod can be restarted, the cluster can be upgraded, the laptop that started the workflow can be closed. When the operator finally responds (hours or days later), a CLI command (or any external sender; in the author's personal setup, a Slack bot) sends the signal and the workflow picks up exactly where it left off.

The CLI has a command for the same thing:

```bash
./dio confirm-speakers session-<id> --mappings '[
  {"speaker_label":"SPEAKER_00","player_name":"Aelara","character":"Aelara Silverleaf","confidence":1.0}
]'
```

There's a second signal, `SetSessionDate`, for recordings that don't have a date in their metadata. Same pattern: compute what you can, then ask a human if you need to, without any special infrastructure for "pending workflows that need input."

## Parallel activities for independent work

After the transcript is mapped, two expensive LLM passes run against it: one generates the narrative recap, the other scans for highlight-worthy moments. Neither depends on the other, and each takes 5–15 minutes, so running them serially means waiting an extra 10–15 minutes for nothing.

Temporal's `workflow.Go` runs them concurrently within the same workflow:

```go
var recapErr, highlightErr error
var recapDone, highlightDone bool

workflow.Go(ctx, func(gCtx workflow.Context) {
    recapErr = workflow.ExecuteActivity(gCtx, GenerateRecapActivity, recapInput).
        Get(gCtx, &recapResult)
    recapDone = true
})

workflow.Go(ctx, func(gCtx workflow.Context) {
    highlightErr = workflow.ExecuteActivity(gCtx, DetectHighlightsActivity, highlightInput).
        Get(gCtx, &highlightResult)
    highlightDone = true
})

_ = workflow.Await(ctx, func() bool { return recapDone && highlightDone })
```

The mental model: `workflow.Go` is not a Go goroutine. It's a deterministic coroutine that Temporal records so replays reconstruct the same interleaving. `workflow.Await` blocks until the predicate is true. If the worker crashes halfway through, the replay reconstructs exactly this state: both activities in flight, the await still pending.

Writing this with raw goroutines and channels would work for the happy path and fall apart the first time the worker got rescheduled. The Temporal equivalent is almost the same shape, and it's durable.

## Determinism and `SideEffect`

Workflows must be deterministic. Reading an environment variable inside a workflow is non-deterministic. The value might differ between the original run and a replay. Temporal catches this and crashes the workflow task.

`SideEffect` is the escape hatch. It captures the result of a non-deterministic operation into workflow history, so replays get the original value:

```go
var ghOwner string
_ = workflow.SideEffect(ctx, func(ctx workflow.Context) interface{} {
    return os.Getenv("PIPELINE_GH_OWNER")
}).Get(&ghOwner)
```

I reach for this two or three places: reading config, generating a random workspace ID, checking the current time when a precise clock isn't needed. Once you internalize the rule ("anything that might change between runs goes through SideEffect or an activity"), determinism stops being a trap and starts being a useful discipline.

## Claude Code as an activity

The recap, highlight detection, and highlight-page generation steps are all backed by Claude Code running headlessly inside an activity:

```go
cmd := exec.CommandContext(ctx, "claude",
    "--print",
    "--output-format", "stream-json",
    "--dangerously-skip-permissions",
    "--model", opts.Model,
    "--", prompt,
)
```

The `stream-json` output is parsed line by line, with each event triggering an activity heartbeat:

```go
for scanner.Scan() {
    var ev streamEvent
    if err := json.Unmarshal(scanner.Bytes(), &ev); err == nil {
        heartbeatFn(summarize(ev))
    }
}
```

This is the same integration pattern as the Python transcription activity: wrap
a long-running child process, forward its progress into Temporal's heartbeat
stream, let retries handle failures. The workflow doesn't know or care that this
activity is talking to an LLM; from its perspective it's just another activity
with a 90-minute timeout.

That's the ergonomic win of this stack: LLM calls, ffmpeg invocations, and git
operations all fit the same activity abstraction. You don't need different
plumbing for each.

## What happens when things go wrong

The real value of Temporal on a project like this shows up in the failure modes:

- **Worker pod OOM mid-transcription**: the chunk's activity is retried on a new pod. Chunks already transcribed in this run have their results cached in workflow history, so we don't redo them.
- **Whisper model download times out**: activity retries, the second attempt finds the model already on disk, moves on.
- **Claude Code hits a rate limit**: activity fails, retry policy waits and tries again. The workflow never knew there was a problem.
- **I redeploy the worker mid-session**: SIGTERM drains the worker, in-flight activities are rescheduled. The workflow pauses at most a few seconds.
- **My laptop dies while the workflow is waiting for a speaker-confirmation signal**: nothing happens. The workflow is state in the Temporal cluster, not state on my machine. When I come back, I send the signal, and it continues.

I spent zero time writing the code that handles any of those. It's all inherent to the programming model.

## When to reach for this pattern

Durable workflows aren't free. There's a learning curve, and small tasks don't need the machinery. The shape of problem that's worth wrapping in Temporal looks like:

- **Multi-step, with heterogeneous step types.** If the "pipeline" is one HTTP call, you don't need this. If it's a child-process invocation, then an LLM call, then a git push, with retry semantics that differ per step, yes.
- **Long-running on unreliable infrastructure.** Cheap hardware, preemptible VMs, laptops. Anything where "the worker might go away mid-run" is a realistic failure mode.
- **Needs a human in the loop somewhere.** If any step might ask a person to confirm, correct, or fill in a blank, signals turn "write a queue system" into three lines of code.
- **Idempotent at the step level but not at the whole-job level.** You want to retry a single activity without replaying successful earlier ones. Workflow history does that for free.

If your problem hits three of those four, the effort to learn Temporal pays back inside your first production outage.

## What's next

A few directions that would push this further:

- **Child workflows per chunk** for parallel transcription, if I ever drop a GPU into the tower. Sequential is fine on CPU-only hardware today (see "One box, zero cloud bill") but the workflow shape is ready for it.
- **A schedule** that auto-processes any new recording that lands in the drop directory, using Temporal schedules.
- **Versioning** the workflow itself with `workflow.GetVersion` so I can change the pipeline shape without breaking in-flight workflows.

The full source is in this repo, [github.com/rossnelson/temporal-session-pipeline](https://github.com/rossnelson/temporal-session-pipeline). It's about 2,000 lines of Go, most of which is activity glue; the workflow itself is about 250 lines. It's a good lens for seeing which Temporal features earn their keep on a real, messy, long-running problem.

And if you have your own "I do this by hand every two weeks" chore, it's worth asking whether it's really a pipeline in disguise. Most of mine were.

If you want to go deeper, the [Temporal docs](https://docs.temporal.io) cover the primitives above with more care, and the [community Slack](https://temporal.io/slack) is where I've asked every not-in-the-docs question I've had. If this pattern fits a chore of your own, I'd love to hear what you end up building.
