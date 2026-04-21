package main

import (
	"log"
	"os"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"dio/internal/pipeline"
)

func main() {
	temporalHost := os.Getenv("TEMPORAL_HOST")
	if temporalHost == "" {
		temporalHost = "localhost:7233"
	}

	c, err := client.Dial(client.Options{HostPort: temporalHost})
	if err != nil {
		log.Fatalf("Failed to connect to Temporal: %v", err)
	}
	defer c.Close()

	w := worker.New(c, pipeline.TaskQueue, worker.Options{
		MaxConcurrentWorkflowTaskPollers: 2,
		MaxConcurrentActivityTaskPollers: 2,
	})

	pipeline.RegisterWorkflowAndActivities(w)

	log.Printf("Starting session pipeline worker on queue %q (Temporal: %s)", pipeline.TaskQueue, temporalHost)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Fatalf("Worker failed: %v", err)
	}
}
