package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	sdkclient "github.com/docker/go-sdk/client"
	sdkcontainer "github.com/docker/go-sdk/container"
	sdkwait "github.com/docker/go-sdk/container/wait"
	dockercontainer "github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
)

const (
	checkpointSuccessMarker = "Checkpoint successful!"
	restoreEntrypointChange = "ENTRYPOINT [\"java\",\"-XX:CRaCRestoreFrom=/checkpoint\"]"
	maxLogLines             = 40
	expectedCheckpointExit  = 137
)

type options struct {
	containerName string
	image         string
	finalImage    string
	timeout       time.Duration
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("crac-checkpoint", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var opts options
	fs.StringVar(&opts.containerName, "container-name", "", "name of the temporary checkpoint container")
	fs.StringVar(&opts.image, "image", "", "image used to create the CRaC checkpoint")
	fs.StringVar(&opts.finalImage, "final-image", "", "image reference for the committed restore image")
	fs.DurationVar(&opts.timeout, "timeout", 0, "optional timeout for the checkpoint run; zero disables it")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if opts.containerName == "" {
		return errors.New("missing --container-name")
	}
	if opts.image == "" {
		return errors.New("missing --image")
	}
	if opts.finalImage == "" {
		return errors.New("missing --final-image")
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	ctx := context.Background()
	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}

	cli, err := sdkclient.New(ctx, sdkclient.WithDockerHost(resolveDockerHost()))
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	checkpointContainer, err := sdkcontainer.Run(
		ctx,
		sdkcontainer.WithClient(cli),
		sdkcontainer.WithImage(opts.image),
		sdkcontainer.WithName(opts.containerName),
		sdkcontainer.WithWaitStrategy(sdkwait.ForExit()),
		sdkcontainer.WithAdditionalHostConfigModifier(func(hostConfig *dockercontainer.HostConfig) {
			hostConfig.Privileged = true
		}),
	)
	if err != nil {
		return fmt.Errorf("run checkpoint container: %w", err)
	}

	state, err := checkpointContainer.State(ctx)
	if err != nil {
		return fmt.Errorf("inspect checkpoint container state: %w", err)
	}

	logs, err := readLogs(ctx, checkpointContainer)
	if err != nil {
		return fmt.Errorf("read checkpoint container logs: %w", err)
	}

	if err := validateCheckpoint(state, logs); err != nil {
		return err
	}

	if state.ExitCode == expectedCheckpointExit {
		fmt.Fprintln(os.Stdout, "CRaC checkpoint container exited with 137 after a successful checkpoint; continuing.")
	}

	if _, err := cli.ContainerCommit(ctx, checkpointContainer.ID(), dockerclient.ContainerCommitOptions{
		Reference: opts.finalImage,
		Changes:   []string{restoreEntrypointChange},
	}); err != nil {
		return fmt.Errorf("commit checkpoint container: %w", err)
	}

	return nil
}

func resolveDockerHost() string {
	if host := os.Getenv("DOCKER_HOST"); host != "" {
		return host
	}

	return dockerclient.DefaultDockerHost
}

func readLogs(ctx context.Context, checkpointContainer *sdkcontainer.Container) (string, error) {
	reader, err := checkpointContainer.Logs(ctx)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	logs, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}

	return string(logs), nil
}

func validateCheckpoint(state *dockercontainer.State, logs string) error {
	if state == nil {
		return errors.New("checkpoint container state is unavailable")
	}

	switch state.ExitCode {
	case 0:
		return nil
	case expectedCheckpointExit:
		if strings.Contains(logs, checkpointSuccessMarker) {
			return nil
		}
	}

	message := fmt.Sprintf("checkpoint container exited with code %d", state.ExitCode)
	tail := logTail(logs, maxLogLines)
	if tail == "" {
		return errors.New(message)
	}

	return fmt.Errorf("%s\n\nLast logs:\n%s", message, tail)
}

func logTail(logs string, maxLines int) string {
	normalized := strings.ReplaceAll(logs, "\r\n", "\n")
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return ""
	}

	lines := strings.Split(normalized, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	return strings.Join(lines, "\n")
}
