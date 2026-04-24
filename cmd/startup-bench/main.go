package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHost = "127.0.0.1"
)

type benchmarkResult struct {
	Variant         string      `json:"variant"`
	Image           string      `json:"image"`
	DockerRunArgs   []string    `json:"dockerRunArgs,omitempty"`
	Endpoint        string      `json:"endpoint"`
	RunsRequested   int         `json:"runsRequested"`
	SuccessfulRuns  int         `json:"successfulRuns"`
	Timeout         string      `json:"timeout"`
	PollInterval    string      `json:"pollInterval"`
	Host            string      `json:"host"`
	ContainerPort   int         `json:"containerPort"`
	ImageSizeBytes  int64       `json:"imageSizeBytes"`
	ImageSizeHuman  string      `json:"imageSizeHuman"`
	GeneratedAt     time.Time   `json:"generatedAt"`
	Summary         summary     `json:"summary"`
	Runs            []runRecord `json:"runs"`
	FailureMessage  string      `json:"failureMessage,omitempty"`
	FailureLogsTail string      `json:"failureLogsTail,omitempty"`
}

type runRecord struct {
	Iteration       int       `json:"iteration"`
	ContainerName   string    `json:"containerName"`
	ContainerID     string    `json:"containerId,omitempty"`
	HostPort        int       `json:"hostPort"`
	StartedAt       time.Time `json:"startedAt"`
	CompletedAt     time.Time `json:"completedAt"`
	DurationMS      float64   `json:"durationMs,omitempty"`
	HTTPStatusCode  int       `json:"httpStatusCode,omitempty"`
	Error           string    `json:"error,omitempty"`
	ContainerLogs   string    `json:"containerLogs,omitempty"`
	LastPollFailure string    `json:"lastPollFailure,omitempty"`
}

type summary struct {
	MinMS    float64 `json:"minMs"`
	MaxMS    float64 `json:"maxMs"`
	MeanMS   float64 `json:"meanMs"`
	MedianMS float64 `json:"medianMs"`
	P95MS    float64 `json:"p95Ms"`
}

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	subcommand := os.Args[1]
	args := os.Args[2:]

	var err error
	switch subcommand {
	case "run":
		err = runBenchmark(args)
	case "report":
		err = writeReport(args)
	case "help", "--help", "-h":
		printUsage()
		return
	default:
		err = fmt.Errorf("unknown subcommand %q", subcommand)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runBenchmark(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var dockerRunArgs stringSliceFlag
	fs.Var(&dockerRunArgs, "docker-run-arg", "additional docker run argument, repeat to pass more than one")

	variant := fs.String("variant", "", "logical variant name")
	image := fs.String("image", "", "docker image reference")
	endpoint := fs.String("endpoint", "/owners/1", "HTTP endpoint to probe")
	runs := fs.Int("runs", 10, "number of cold starts to measure")
	timeout := fs.Duration("timeout", 2*time.Minute, "maximum time to wait for a successful response")
	pollInterval := fs.Duration("poll-interval", 200*time.Millisecond, "delay between HTTP probes")
	host := fs.String("host", defaultHost, "host interface for HTTP probes")
	containerPort := fs.Int("container-port", 8080, "container port exposed by the application")
	output := fs.String("output", "", "path to write JSON benchmark output")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *variant == "" {
		return errors.New("run requires --variant")
	}
	if *image == "" {
		return errors.New("run requires --image")
	}
	if *runs <= 0 {
		return errors.New("run requires --runs to be greater than zero")
	}
	if *timeout <= 0 {
		return errors.New("run requires --timeout to be greater than zero")
	}
	if *pollInterval <= 0 {
		return errors.New("run requires --poll-interval to be greater than zero")
	}

	imageSizeBytes, _ := inspectImageSize(*image)
	result := benchmarkResult{
		Variant:        *variant,
		Image:          *image,
		DockerRunArgs:  append([]string(nil), dockerRunArgs...),
		Endpoint:       *endpoint,
		RunsRequested:  *runs,
		Timeout:        timeout.String(),
		PollInterval:   pollInterval.String(),
		Host:           *host,
		ContainerPort:  *containerPort,
		ImageSizeBytes: imageSizeBytes,
		ImageSizeHuman: humanBytes(imageSizeBytes),
		GeneratedAt:    time.Now().UTC(),
	}

	var durations []float64
	for iteration := 1; iteration <= *runs; iteration++ {
		record, err := executeRun(*variant, *image, dockerRunArgs, *endpoint, *host, *containerPort, *timeout, *pollInterval, iteration)
		result.Runs = append(result.Runs, record)
		if err != nil {
			result.FailureMessage = err.Error()
			result.FailureLogsTail = record.ContainerLogs
			result.SuccessfulRuns = len(durations)
			result.Summary = summarizeDurations(durations)
			if *output != "" {
				if writeErr := writeJSON(*output, result); writeErr != nil {
					return fmt.Errorf("%w; additionally failed to write JSON output: %v", err, writeErr)
				}
			}
			return err
		}

		durations = append(durations, record.DurationMS)
	}

	result.SuccessfulRuns = len(durations)
	result.Summary = summarizeDurations(durations)

	if *output != "" {
		if err := writeJSON(*output, result); err != nil {
			return err
		}
	}

	printRunSummary(result, *output)
	return nil
}

func writeReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	inputDir := fs.String("input-dir", "results", "directory containing per-variant benchmark JSON files")
	output := fs.String("output", "", "path to write markdown summary")

	if err := fs.Parse(args); err != nil {
		return err
	}

	pattern := filepath.Join(*inputDir, "*.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no benchmark JSON files found in %s", *inputDir)
	}

	results := make([]benchmarkResult, 0, len(files))
	for _, file := range files {
		var result benchmarkResult
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(content, &result); err != nil {
			return fmt.Errorf("decode %s: %w", file, err)
		}
		results = append(results, result)
	}

	sort.Slice(results, func(i, j int) bool {
		return variantOrder(results[i].Variant) < variantOrder(results[j].Variant)
	})

	markdown := renderMarkdown(results)
	if *output != "" {
		if err := os.MkdirAll(filepath.Dir(*output), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(*output, []byte(markdown), 0o644); err != nil {
			return err
		}
	}

	fmt.Print(markdown)
	if *output != "" {
		fmt.Printf("\nWrote markdown summary to %s\n", *output)
	}
	return nil
}

func executeRun(variant, image string, dockerRunArgs []string, endpoint, host string, containerPort int, timeout, pollInterval time.Duration, iteration int) (runRecord, error) {
	hostPort, err := reservePort(host)
	if err != nil {
		return runRecord{}, err
	}

	containerName := fmt.Sprintf("startup-bench-%s-%d-%d", sanitize(variant), time.Now().UnixNano(), iteration)
	start := time.Now()
	record := runRecord{
		Iteration:     iteration,
		ContainerName: containerName,
		HostPort:      hostPort,
		StartedAt:     start.UTC(),
	}

	dockerArgs := []string{"run", "-d", "--rm", "--name", containerName}
	dockerArgs = append(dockerArgs, dockerRunArgs...)
	dockerArgs = append(dockerArgs, "-p", fmt.Sprintf("%d:%d", hostPort, containerPort), image)

	runOutput, err := runCommand(context.Background(), "docker", dockerArgs...)
	if err != nil {
		record.Error = fmt.Sprintf("docker run failed: %v", err)
		return record, fmt.Errorf("docker run failed for %s: %w", variant, err)
	}
	record.ContainerID = strings.TrimSpace(runOutput)

	defer stopContainer(containerName)

	client := &http.Client{Timeout: 2 * time.Second}
	targetURL := fmt.Sprintf("http://%s:%d%s", host, hostPort, endpoint)
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		response, reqErr := client.Get(targetURL)
		if reqErr == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			record.HTTPStatusCode = response.StatusCode
			if response.StatusCode == http.StatusOK {
				completed := time.Now()
				record.CompletedAt = completed.UTC()
				record.DurationMS = millis(completed.Sub(start))
				return record, nil
			}
			record.LastPollFailure = fmt.Sprintf("received HTTP %d", response.StatusCode)
		} else {
			record.LastPollFailure = reqErr.Error()
		}

		time.Sleep(pollInterval)
	}

	logs, _ := runCommand(context.Background(), "docker", "logs", containerName)
	record.ContainerLogs = tail(logs, 12000)
	record.Error = fmt.Sprintf("timed out after %s waiting for %s", timeout, targetURL)
	return record, fmt.Errorf("benchmark %s timed out after %s waiting for %s", variant, timeout, targetURL)
}

func inspectImageSize(image string) (int64, error) {
	output, err := runCommand(context.Background(), "docker", "image", "inspect", image, "--format", "{{.Size}}")
	if err != nil {
		return 0, err
	}
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return 0, errors.New("empty docker image size output")
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, err
	}
	return value, nil
}

func summarizeDurations(durations []float64) summary {
	if len(durations) == 0 {
		return summary{}
	}

	sorted := append([]float64(nil), durations...)
	sort.Float64s(sorted)
	minValue := sorted[0]
	maxValue := sorted[len(sorted)-1]
	total := 0.0
	for _, value := range sorted {
		total += value
	}
	mean := total / float64(len(sorted))
	median := percentile(sorted, 0.5)
	p95 := percentile(sorted, 0.95)

	return summary{
		MinMS:    round(minValue),
		MaxMS:    round(maxValue),
		MeanMS:   round(mean),
		MedianMS: round(median),
		P95MS:    round(p95),
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	index := max(int(math.Ceil(p*float64(len(sorted))))-1, 0)
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func renderMarkdown(results []benchmarkResult) string {
	var builder strings.Builder
	builder.WriteString("# Startup Benchmark Summary\n\n")
	builder.WriteString("| Variant | Image | Image Size | Median | p95 | Mean | Min | Max | Successful Runs |\n")
	builder.WriteString("| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n")
	for _, result := range results {
		builder.WriteString(fmt.Sprintf("| %s | %s | %s | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %.2f ms | %d/%d |\n",
			result.Variant,
			result.Image,
			result.ImageSizeHuman,
			result.Summary.MedianMS,
			result.Summary.P95MS,
			result.Summary.MeanMS,
			result.Summary.MinMS,
			result.Summary.MaxMS,
			result.SuccessfulRuns,
			result.RunsRequested,
		))
	}
	builder.WriteString("\n")
	builder.WriteString("The benchmark measures wall-clock time from `docker run` until the first successful `HTTP 200` response from `/owners/1`.\n")
	return builder.String()
}

func variantOrder(variant string) int {
	switch variant {
	case "baseline":
		return 0
	case "extract":
		return 1
	case "aot-cache":
		return 2
	case "spring-aot-aot-cache":
		return 3
	case "crac":
		return 4
	default:
		return 100
	}
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return os.WriteFile(path, content, 0o644)
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, trimmed)
	}
	return string(output), nil
}

func stopContainer(containerName string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = runCommand(ctx, "docker", "stop", "--time", "0", containerName)
}

func reservePort(host string) (int, error) {
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("failed to reserve host port")
	}
	return address.Port, nil
}

func millis(duration time.Duration) float64 {
	return round(float64(duration) / float64(time.Millisecond))
}

func round(value float64) float64 {
	return math.Round(value*100) / 100
}

func humanBytes(bytes int64) string {
	if bytes <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(bytes)
	index := 0
	for value >= 1024 && index < len(units)-1 {
		value /= 1024
		index++
	}
	return fmt.Sprintf("%.2f %s", value, units[index])
}

func sanitize(input string) string {
	replacer := strings.NewReplacer(
		"/", "-",
		":", "-",
		"_", "-",
		" ", "-",
	)
	return replacer.Replace(strings.ToLower(input))
}

func tail(input string, max int) string {
	trimmed := strings.TrimSpace(input)
	if len(trimmed) <= max {
		return trimmed
	}
	return trimmed[len(trimmed)-max:]
}

func printRunSummary(result benchmarkResult, output string) {
	fmt.Printf("Variant: %s\n", result.Variant)
	fmt.Printf("Image: %s\n", result.Image)
	fmt.Printf("Runs: %d\n", result.SuccessfulRuns)
	fmt.Printf("Image Size: %s\n", result.ImageSizeHuman)
	fmt.Printf("Median: %.2f ms\n", result.Summary.MedianMS)
	fmt.Printf("p95: %.2f ms\n", result.Summary.P95MS)
	fmt.Printf("Mean: %.2f ms\n", result.Summary.MeanMS)
	if output != "" {
		fmt.Printf("Wrote JSON results to %s\n", output)
	}
}

func printUsage() {
	message := `startup-bench benchmarks Dockerized Spring Boot startup.

Usage:
  startup-bench run --variant <name> --image <image> [flags]
  startup-bench report --input-dir <dir> --output <file>
`
	fmt.Fprint(os.Stderr, message)
}
