package consolestream

import (
	"fmt"

	"github.com/docker/docker/client"
)

// initDockerClient initializes a Docker or Podman client based on runtime
func initDockerClient(runtime string) (*client.Client, error) {
	var opts []client.Opt

	switch runtime {
	case "podman":
		// Podman uses a different socket path
		opts = append(opts, client.WithHost("unix:///var/run/podman/podman.sock"))
	case "docker", "":
		// Use default Docker configuration from environment
		opts = append(opts, client.FromEnv)
	default:
		return nil, fmt.Errorf("unsupported container runtime: %s (must be 'docker' or 'podman')", runtime)
	}

	// Auto-negotiate API version for compatibility
	opts = append(opts, client.WithAPIVersionNegotiation())

	return client.NewClientWithOpts(opts...)
}
