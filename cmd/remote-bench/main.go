package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

const (
	defaultSSHUser          = "root"
	defaultSSHPort          = 22
	defaultStrictHostKey    = "accept-new"
	defaultIdentityFileName = "spring-boot-startup-bench-ed25519"
	defaultRemoteDir        = "/opt/spring-boot-startup/workspace"
	defaultRunnerScript     = "/usr/local/bin/run-spring-boot-startup-benchmarks.sh"
	defaultReadyMarker      = "/opt/spring-boot-startup/.host-ready"
)

type config struct {
	host               string
	port               int
	user               string
	identity           string
	localDir           string
	remoteDir          string
	runnerScript       string
	readyMarker        string
	taskName           string
	strictHostKeyCheck string
	sshPath            string
	localResultsDir    string
	remoteResultsDir   string
	sshTarget          string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := parseFlags()
	if err != nil {
		return err
	}

	if err := ensureWorkspace(cfg.localDir); err != nil {
		return err
	}

	if err := ensureSSHBinary(cfg.sshPath); err != nil {
		return err
	}

	if err := ensureIdentityFile(cfg.identity); err != nil {
		return err
	}

	fmt.Println("Checking remote bootstrap marker")
	if err := checkReady(cfg); err != nil {
		return err
	}

	fmt.Printf("Uploading workspace to %s:%s\n", cfg.host, cfg.remoteDir)
	if err := uploadWorkspace(cfg); err != nil {
		return err
	}

	fmt.Printf("Running remote task %s\n", cfg.taskName)
	if err := runRemoteBenchmark(cfg); err != nil {
		return err
	}

	fmt.Printf("Downloading remote results from %s\n", cfg.remoteResultsDir)
	if err := downloadResults(cfg); err != nil {
		return err
	}

	fmt.Printf("Benchmark completed. Results synced to %s\n", cfg.localResultsDir)
	return nil
}

func parseFlags() (config, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return config{}, fmt.Errorf("resolve current working directory: %w", err)
	}

	identityPath, err := defaultIdentityPath()
	if err != nil {
		return config{}, err
	}

	fs := flag.NewFlagSet("remote-bench", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var cfg config
	fs.StringVar(&cfg.host, "host", "", "Hetzner server IPv4 or DNS name")
	fs.IntVar(&cfg.port, "port", defaultSSHPort, "SSH port")
	fs.StringVar(&cfg.user, "user", defaultSSHUser, "SSH user")
	fs.StringVar(&cfg.identity, "identity", identityPath, "path to the SSH private key created for the Pulumi host")
	fs.StringVar(&cfg.localDir, "local-dir", workingDir, "local benchmark workspace root")
	fs.StringVar(&cfg.remoteDir, "remote-dir", defaultRemoteDir, "remote workspace path used for the uploaded repository")
	fs.StringVar(&cfg.runnerScript, "runner-script", defaultRunnerScript, "remote helper script written by cloud-init")
	fs.StringVar(&cfg.readyMarker, "ready-marker", defaultReadyMarker, "remote marker file created after bootstrap")
	fs.StringVar(&cfg.taskName, "task", "bench:all", "task target to run remotely")
	fs.StringVar(&cfg.strictHostKeyCheck, "strict-host-key-checking", defaultStrictHostKey, "SSH StrictHostKeyChecking value, for example accept-new, yes, or no")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return config{}, err
	}
	if fs.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	if cfg.host == "" {
		return config{}, errors.New("missing required --host")
	}
	if cfg.port <= 0 {
		return config{}, errors.New("--port must be greater than zero")
	}
	if cfg.taskName == "" {
		return config{}, errors.New("--task must not be empty")
	}

	if cfg.localDir, err = filepath.Abs(cfg.localDir); err != nil {
		return config{}, fmt.Errorf("resolve --local-dir: %w", err)
	}
	if cfg.identity, err = filepath.Abs(cfg.identity); err != nil {
		return config{}, fmt.Errorf("resolve --identity: %w", err)
	}
	cfg.remoteDir = path.Clean(filepath.ToSlash(cfg.remoteDir))
	if cfg.remoteDir == "." || cfg.remoteDir == "/" || !strings.HasPrefix(cfg.remoteDir, "/") {
		return config{}, errors.New("--remote-dir must be an absolute path below / and must not be /")
	}
	cfg.runnerScript = path.Clean(filepath.ToSlash(cfg.runnerScript))
	cfg.readyMarker = path.Clean(filepath.ToSlash(cfg.readyMarker))

	cfg.localResultsDir = filepath.Join(cfg.localDir, "results")
	cfg.remoteResultsDir = path.Join(filepath.ToSlash(cfg.remoteDir), "results")
	cfg.sshTarget = fmt.Sprintf("%s@%s", cfg.user, cfg.host)
	cfg.sshPath = "ssh"
	return cfg, nil
}

func defaultIdentityPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(homeDir, ".ssh", defaultIdentityFileName), nil
}

func ensureWorkspace(localDir string) error {
	info, err := os.Stat(localDir)
	if err != nil {
		return fmt.Errorf("stat --local-dir %q: %w", localDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--local-dir %q is not a directory", localDir)
	}
	if _, err := os.Stat(filepath.Join(localDir, "Taskfile.yml")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%q does not look like the benchmark repository root; Taskfile.yml is missing", localDir)
		}
		return fmt.Errorf("stat Taskfile.yml in %q: %w", localDir, err)
	}
	return nil
}

func ensureSSHBinary(sshPath string) error {
	if _, err := exec.LookPath(sshPath); err != nil {
		return fmt.Errorf("find %s in PATH: %w", sshPath, err)
	}
	return nil
}

func ensureIdentityFile(identity string) error {
	if _, err := os.Stat(identity); err != nil {
		return fmt.Errorf("stat --identity %q: %w", identity, err)
	}
	return nil
}

func uploadWorkspace(cfg config) error {
	cmd := exec.Command(cfg.sshPath, sshArgs(cfg, remoteUploadCommand(cfg.remoteDir))...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create upload stdin pipe: %w", err)
	}

	errorCh := make(chan error, 1)
	go func() {
		defer stdin.Close()
		errorCh <- writeWorkspaceArchive(stdin, cfg.localDir)
	}()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ssh upload command: %w", err)
	}

	archiveErr := <-errorCh
	if err := cmd.Wait(); err != nil {
		if archiveErr != nil {
			return fmt.Errorf("upload workspace: %w; remote command: %v", archiveErr, err)
		}
		return fmt.Errorf("upload workspace via ssh: %w", err)
	}
	if archiveErr != nil {
		return fmt.Errorf("archive workspace: %w", archiveErr)
	}

	return nil
}

func checkReady(cfg config) error {
	command := fmt.Sprintf("test -f %s", shQuote(cfg.readyMarker))
	if err := runSSHCommand(cfg, command); err != nil {
		return fmt.Errorf("remote bootstrap marker %s is not present yet: %w", cfg.readyMarker, err)
	}
	return nil
}

func runRemoteBenchmark(cfg config) error {
	command := fmt.Sprintf("REPO_DIR=%s %s %s", shQuote(cfg.remoteDir), shQuote(cfg.runnerScript), shQuote(cfg.taskName))
	return runSSHCommand(cfg, command)
}

func downloadResults(cfg config) error {
	if err := cleanLocalResults(cfg.localResultsDir); err != nil {
		return err
	}

	cmd := exec.Command(cfg.sshPath, sshArgs(cfg, remoteDownloadCommand(cfg.remoteDir))...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create download stdout pipe: %w", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ssh download command: %w", err)
	}

	if err := extractResultsArchive(stdout, cfg.localDir); err != nil {
		_ = cmd.Wait()
		return fmt.Errorf("extract results archive: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("download results via ssh: %w", err)
	}

	return nil
}

func runSSHCommand(cfg config, remoteCommand string) error {
	cmd := exec.Command(cfg.sshPath, sshArgs(cfg, remoteCommand)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh command failed: %w", err)
	}
	return nil
}

func sshArgs(cfg config, remoteCommand string) []string {
	args := []string{
		"-i", cfg.identity,
		"-p", fmt.Sprintf("%d", cfg.port),
		"-o", "BatchMode=yes",
	}
	if cfg.strictHostKeyCheck != "" {
		args = append(args, "-o", "StrictHostKeyChecking="+cfg.strictHostKeyCheck)
	}
	args = append(args, cfg.sshTarget, remoteCommand)
	return args
}

func remoteUploadCommand(remoteDir string) string {
	quotedDir := shQuote(remoteDir)
	return fmt.Sprintf("rm -rf %s && mkdir -p %s && tar -xzf - -C %s", quotedDir, quotedDir, quotedDir)
}

func remoteDownloadCommand(remoteDir string) string {
	quotedDir := shQuote(remoteDir)
	return fmt.Sprintf("cd %s && tar -czf - results", quotedDir)
}

func writeWorkspaceArchive(writer io.Writer, localDir string) error {
	gzipWriter := gzip.NewWriter(writer)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	return filepath.WalkDir(localDir, func(currentPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relPath, err := filepath.Rel(localDir, currentPath)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		archivePath := filepath.ToSlash(relPath)
		if shouldSkipArchivePath(archivePath, entry) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = archivePath
		if entry.IsDir() {
			header.Name += "/"
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		file, err := os.Open(currentPath)
		if err != nil {
			return err
		}
		defer file.Close()

		if _, err := io.Copy(tarWriter, file); err != nil {
			return err
		}
		return nil
	})
}

func shouldSkipArchivePath(archivePath string, entry fs.DirEntry) bool {
	baseName := path.Base(archivePath)
	switch baseName {
	case ".git", ".pulumi", "results":
		return true
	}
	if strings.HasSuffix(baseName, ".exe") || strings.HasSuffix(baseName, ".log") {
		return true
	}
	if matched, _ := path.Match("run/pulumi/Pulumi.*.yaml", archivePath); matched {
		return true
	}
	if entry.IsDir() && strings.HasPrefix(archivePath, "run/pulumi/.pulumi") {
		return true
	}
	return false
}

func cleanLocalResults(resultsDir string) error {
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		return fmt.Errorf("create local results directory: %w", err)
	}

	patterns := []string{
		filepath.Join(resultsDir, "*.json"),
		filepath.Join(resultsDir, "summary.md"),
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("glob %s: %w", pattern, err)
		}
		for _, match := range matches {
			if err := os.Remove(match); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove %s: %w", match, err)
			}
		}
	}
	return nil
}

func extractResultsArchive(reader io.Reader, destinationRoot string) error {
	gzipReader, err := gzip.NewReader(reader)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		targetPath := filepath.Join(destinationRoot, filepath.FromSlash(header.Name))
		cleanTargetPath := filepath.Clean(targetPath)
		cleanRoot := filepath.Clean(destinationRoot) + string(os.PathSeparator)
		if !strings.HasPrefix(cleanTargetPath, cleanRoot) && cleanTargetPath != filepath.Clean(destinationRoot) {
			return fmt.Errorf("refusing to extract outside destination root: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(cleanTargetPath, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(cleanTargetPath), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(cleanTargetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, tarReader); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		}
	}
}

func shQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
