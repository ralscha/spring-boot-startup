# Remote Benchmark Host

This directory provisions a Hetzner host for the Docker-based startup benchmark and gives you a local Go runner that uploads the workspace, executes the benchmark remotely, and syncs `results/` back.

The cloud-init bootstrap intentionally follows the official installation paths for Go and Docker on Debian:

- Go: https://go.dev/doc/install
- Docker Engine on Debian: https://docs.docker.com/engine/install/debian/

`Task` is also installed because the benchmark contract in the repository is already defined in [Taskfile.yml](../Taskfile.yml).

## Provision the host

1. Install Pulumi and authenticate it normally on your machine.
2. Create a stack in [run/pulumi](./pulumi).
3. Set the Hetzner API token as a secret.
4. Run `pulumi up`.

Example:

```
cd run/pulumi
pulumi stack init dev
pulumi config set hcloud:token --secret
pulumi up
```

Useful optional config values for the Pulumi project:

- `serverName`
- `serverType` default: `ccx23`
- `location` default: `hel1`
- `image` default: `debian-13`
- `sshPrivateKeyPath` default: `<home>/.ssh/spring-boot-startup-bench-ed25519`

The stack exports the server IP, the SSH private key path, and convenience commands for watching cloud-init and bootstrap logs.

## Wait for bootstrap

Cloud-init installs Docker, Go, and `Task`, writes `/usr/local/bin/run-spring-boot-startup-benchmarks.sh`, then reboots the server once.

Wait until the exported `readyCheckCommand` prints `ready`, or check for `/opt/spring-boot-startup/.host-ready` over SSH.

## Upload and run the benchmark

Run the local Go helper from the repository root once the host is back after the reboot:

```
go run ./cmd/remote-bench --host <server-ip>
```

Useful flags:

- `--identity` to point at a non-default SSH key
- `--task` to run a different task than `bench:all`
- `--remote-dir` to use a different remote workspace path
- `--strict-host-key-checking` default: `accept-new`

The runner:

1. Archives the current workspace and uploads it to `/opt/spring-boot-startup/workspace`
2. Executes the remote helper script, which runs `task validate` and then `task bench:all`
3. Downloads the remote `results/` directory back into the local workspace

If you prefer a built binary instead of `go run`, build it locally and execute that binary from the repository root or pass `--local-dir` explicitly.