package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/charmbracelet/huh"
	"github.com/tidwall/jsonc"
)

type dockerInspect struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

type metadataEntry struct {
	RemoteUser      string `json:"remoteUser"`
	WorkspaceFolder string `json:"workspaceFolder"`
}

type containerInfo struct {
	id              string
	name            string
	localFolder     string
	remoteUser      string
	workspaceFolder string
}

// parseMetadata parses the devcontainer.metadata label of docker inspect
// and returns the effective remoteUser and workspaceFolder.
func parseMetadata(raw string) (remoteUser, workspaceFolder string) {
	var entries []metadataEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return
	}
	for _, e := range entries {
		if e.RemoteUser != "" {
			remoteUser = e.RemoteUser
		}
		if e.WorkspaceFolder != "" {
			workspaceFolder = e.WorkspaceFolder
		}
	}
	return
}

// readDevcontainerConfig reads devcontainer.json
// and returns the explicit remoteUser (or containerUser) and workspaceFolder, if set.
func readDevcontainerConfig(path string) (remoteUser, workspaceFolder string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var cfg struct {
		RemoteUser      string `json:"remoteUser"`
		ContainerUser   string `json:"containerUser"`
		WorkspaceFolder string `json:"workspaceFolder"`
	}
	if err := json.Unmarshal(jsonc.ToJSON(data), &cfg); err != nil {
		return
	}
	remoteUser = cfg.RemoteUser
	if remoteUser == "" {
		remoteUser = cfg.ContainerUser
	}
	workspaceFolder = cfg.WorkspaceFolder
	return
}

// containerCWD maps a host path to its equivalent path inside the container.
// If hostCWD is a subdirectory of c.localFolder, the relative suffix is appended to c.workspaceFolder
// (e.g. ~/Code/proj/apps/web → /workspaces/proj/apps/web).
// Otherwise it returns c.workspaceFolder unchanged.
func containerCWD(c containerInfo, hostCWD string) string {
	rel, err := filepath.Rel(c.localFolder, hostCWD)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return c.workspaceFolder
	}
	return filepath.Join(c.workspaceFolder, rel)
}

// getShell asks the container for the user's login shell via getent passwd,
// falling back to probing common shells.
func getShell(id, user string) string {
	args := []string{"exec"}
	if user != "" {
		args = append(args, "-u", user)
	}
	args = append(args, id, "getent", "passwd", user)
	if out, err := exec.Command("docker", args...).Output(); err == nil {
		parts := strings.Split(strings.TrimSpace(string(out)), ":")
		// user:pass:uid:gid:comment:home:shell
		if len(parts) >= 7 && parts[6] != "" {
			return parts[6]
		}
	}
	for _, sh := range []string{"/bin/bash", "/bin/zsh", "/bin/sh"} {
		if exec.Command("docker", "exec", id, "test", "-x", sh).Run() == nil {
			return sh
		}
	}
	return "/bin/sh"
}

func listContainers() ([]containerInfo, error) {
	// Step 1: get IDs of running devcontainers
	out, err := exec.Command("docker", "ps",
		"--filter", "label=devcontainer.local_folder",
		"-q").Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	ids := strings.Fields(strings.TrimSpace(string(out)))
	if len(ids) == 0 {
		return nil, nil
	}

	// Step 2: run docker inspect for all IDs
	inspectArgs := append([]string{"inspect"}, ids...)
	out, err = exec.Command("docker", inspectArgs...).Output()
	if err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	var inspected []dockerInspect
	if err := json.Unmarshal(out, &inspected); err != nil {
		return nil, fmt.Errorf("parse inspect output: %w", err)
	}

	// Step 3: parse each entry into containerInfo
	containers := make([]containerInfo, 0, len(inspected))
	for _, i := range inspected {
		labels := i.Config.Labels
		localFolder := labels["devcontainer.local_folder"]
		configFile := labels["devcontainer.config_file"]

		// Priority 1: metadata label
		remoteUser, workspaceFolder := parseMetadata(labels["devcontainer.metadata"])

		// Priority 2: explicit values in devcontainer.json override metadata
		if configFile != "" {
			if u, w := readDevcontainerConfig(configFile); u != "" || w != "" {
				if u != "" {
					remoteUser = u
				}
				if w != "" {
					workspaceFolder = w
				}
			}
		}

		// Priority 3: default workspace path
		if workspaceFolder == "" && localFolder != "" {
			workspaceFolder = "/workspaces/" + filepath.Base(localFolder)
		}

		containers = append(containers, containerInfo{
			id:              i.ID,
			name:            strings.TrimPrefix(i.Name, "/"),
			localFolder:     localFolder,
			remoteUser:      remoteUser,
			workspaceFolder: workspaceFolder,
		})
	}
	return containers, nil
}

func pick(containers []containerInfo) (containerInfo, error) {
	if len(containers) == 1 {
		return containers[0], nil
	}

	labels := make([]string, len(containers))
	for i, c := range containers {
		labels[i] = fmt.Sprintf("%-24s %s", c.name, c.localFolder)
	}

	var chosen int
	options := make([]huh.Option[int], len(containers))
	for i, label := range labels {
		options[i] = huh.NewOption(label, i)
	}

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[int]().
				Title("Select a devcontainer").
				Options(options...).
				Value(&chosen),
		),
	).Run()
	if err != nil {
		return containerInfo{}, err
	}
	return containers[chosen], nil
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	containers, err := listContainers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(containers) == 0 {
		fmt.Println("No running devcontainers found.")
		os.Exit(0)
	}

	// Narrow down to containers whose localFolder contains cwd
	var candidates []containerInfo
	for _, c := range containers {
		rel, err := filepath.Rel(c.localFolder, cwd)
		if err == nil && !strings.HasPrefix(rel, "..") {
			candidates = append(candidates, c)
		}
	}
	// Disregard if it is run in a completely unrelated directory
	if len(candidates) == 0 {
		candidates = containers
	}

	chosen, err := pick(candidates)
	if err != nil {
		// huh returns a sentinel on Ctrl-C; treat as clean exit
		os.Exit(0)
	}

	shell := getShell(chosen.id, chosen.remoteUser)

	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		fmt.Fprintf(os.Stderr, "docker not found in PATH\n")
		os.Exit(1)
	}

	targetDir := containerCWD(chosen, cwd)

	args := []string{"docker", "exec", "-it"}
	if chosen.remoteUser != "" {
		args = append(args, "-u", chosen.remoteUser)
	}
	if targetDir != "" {
		args = append(args, "-w", targetDir)
	}
	args = append(args, chosen.id, shell, "-l")

	if err := syscall.Exec(dockerPath, args, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "exec: %v\n", err)
		os.Exit(1)
	}
}
