package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/google/uuid"
)

func RunRuntimeCommand(ctx context.Context, cmd RuntimeCommand, out io.Writer) error {
	switch cmd.Action {
	case "start", "record", "retire", "verify":
	default:
		return fmt.Errorf("runtime action must be start, record, retire or verify")
	}
	dir, err := runtimeCleanPath(cmd.Directory)
	if err != nil {
		return err
	}
	cmd.Directory = dir
	receipt, err := readRuntimeReceipt(dir)
	fresh := os.IsNotExist(err)
	if err != nil && !fresh {
		return err
	}
	if fresh && cmd.Action == "start" && (cmd.New == (cmd.Supersedes != "")) {
		return fmt.Errorf("first start requires exactly one of --new-runtime or --supersedes")
	}
	if fresh && cmd.Action != "start" && cmd.Action != "record" {
		return fmt.Errorf("missing runtime ownership receipt; explicitly record the prepared runtime first")
	}
	if fresh && cmd.Action == "start" && cmd.Project == "" {
		cmd.Project = os.Getenv("DENSE_MEM_EVAL_COMPOSE_PROJECT")
		if cmd.Project == "" {
			cmd.Project = "densemem_eval"
		}
	}
	if fresh && !pairName.MatchString(cmd.Project) {
		return fmt.Errorf("an explicit valid Compose project is required")
	}
	if cmd.StopTimeout == 0 {
		cmd.StopTimeout = time.Minute
	}
	if cmd.StopTimeout < 100*time.Millisecond || cmd.StopTimeout > 5*time.Minute {
		return fmt.Errorf("runtime stop timeout must be between 100ms and 5m")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := runtimeLock(filepath.Join(dir, "runtime.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if !fresh {
		receipt, err = readRuntimeReceipt(dir)
		if err != nil {
			return err
		}
		if cmd.Project != "" && cmd.Project != receipt.Project {
			return fmt.Errorf("evaluation Compose project differs from the runtime receipt")
		}
		cmd.Project = receipt.Project
	} else {
		if _, err := os.Lstat(filepath.Join(dir, runtimeReceiptName)); err == nil {
			return fmt.Errorf("runtime was recorded concurrently; refresh its receipt")
		} else if !os.IsNotExist(err) {
			return err
		}
		receipt = runtimeReceipt{SchemaVersion: runtimeSchema, ID: uuid.NewString(), Directory: dir, Project: cmd.Project, State: "preparing", CreatedAt: time.Now().UTC()}
	}
	lockDir, err := runtimeLockDirectory()
	if err != nil {
		return err
	}
	projectLock, err := runtimeLock(filepath.Join(lockDir, "project-"+receipt.Project+".lock"))
	if err != nil {
		return err
	}
	defer projectLock.Close()
	switch cmd.Action {
	case "start":
		err = startEvalRuntime(ctx, cmd, &receipt, fresh)
	case "record":
		if !fresh && receipt.State != "active" && receipt.State != "preparing" {
			return fmt.Errorf("cannot enroll resources in a %s runtime", receipt.State)
		}
		for _, container := range receipt.Containers {
			if container.Fixture && !slices.Contains(cmd.Fixtures, container.ID) {
				cmd.Fixtures = append(cmd.Fixtures, container.ID)
			}
		}
		err = captureRuntimeResources(ctx, &receipt, cmd.Fixtures, fresh)
		if err == nil && len(receipt.Containers) == 0 {
			err = fmt.Errorf("no prepared containers match the explicit Compose project")
		}
		if err == nil {
			receipt.State = "active"
			err = saveRuntimeReceipt(receipt)
		}
	case "retire":
		err = retireEvalRuntime(ctx, cmd, &receipt)
	case "verify":
		if receipt.State == "retired" {
			err = verifyRetiredRuntime(ctx, receipt)
		} else if receipt.State == "active" {
			err = verifyActiveRuntime(ctx, receipt)
		} else {
			err = fmt.Errorf("runtime is %s; preparation or retirement is incomplete", receipt.State)
		}
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]string{"runtime_id": receipt.ID, "state": receipt.State, "receipt": filepath.Join(dir, runtimeReceiptName)})
}

func runtimeLockDirectory() (string, error) {
	dir := filepath.Join("/tmp", fmt.Sprintf("dense-mem-paired-locks-%d", os.Getuid()))
	if _, err := runtimeCleanPath(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

func startEvalRuntime(ctx context.Context, cmd RuntimeCommand, receipt *runtimeReceipt, fresh bool) error {
	if receipt.State == "retired" || receipt.State == "retiring" {
		return fmt.Errorf("a %s runtime cannot resume", receipt.State)
	}
	if !fresh && (cmd.New || cmd.Supersedes != "") {
		return fmt.Errorf("existing runtime resumes without a new or replacement declaration")
	}
	if receipt.State == "active" {
		locks, err := lockRuntimeResources(*receipt)
		if err != nil {
			return err
		}
		defer func() {
			for _, lock := range locks {
				lock.Close()
			}
		}()
		if err := verifyActiveRuntime(ctx, *receipt); err != nil {
			return err
		}
		for _, container := range receipt.Containers {
			if container.Fixture {
				continue
			}
			current, err := inspectRuntimeContainer(ctx, container.ID)
			if err != nil {
				return err
			}
			if !current.Running {
				if _, err := runtimeDocker(ctx, "container", "start", container.ID); err != nil {
					return err
				}
			}
		}
		return verifyActiveRuntime(ctx, *receipt)
	}
	if fresh && cmd.Supersedes != "" {
		oldDir, err := runtimeCleanPath(cmd.Supersedes)
		if err != nil {
			return err
		}
		if runtimeOverlap(oldDir, receipt.Directory) {
			return fmt.Errorf("replacement and retired runtime directories must not overlap")
		}
		old, err := readRuntimeReceipt(oldDir)
		if err != nil {
			return err
		}
		if err := verifyRetiredRuntime(ctx, old); err != nil {
			return fmt.Errorf("replacement blocked: %w", err)
		}
		receipt.Supersedes = oldDir
	}
	if !fresh && receipt.Supersedes != "" {
		old, err := readRuntimeReceipt(receipt.Supersedes)
		if err != nil {
			return err
		}
		if err := verifyRetiredRuntime(ctx, old); err != nil {
			return fmt.Errorf("replacement blocked: %w", err)
		}
	}
	if cmd.ComposeFile == "" {
		cmd.ComposeFile = receipt.ComposeFile
	}
	if cmd.ComposeFile == "" {
		cmd.ComposeFile = "examples/docker-compose.evaluation.yml"
	}
	compose, err := runtimeCleanPath(cmd.ComposeFile)
	if err != nil {
		return err
	}
	hash, err := PairFileHash(compose)
	if err != nil {
		return err
	}
	if !fresh && (receipt.ComposeFile != compose || receipt.ComposeHash != hash) {
		return fmt.Errorf("interrupted startup Compose source changed")
	}
	receipt.ComposeFile, receipt.ComposeHash = compose, hash
	if fresh {
		if err := assertUnusedRuntimeProject(ctx, receipt.Project); err != nil {
			return err
		}
		receipt.InitialImages, err = runtimeList(ctx, "image")
		if err != nil {
			return err
		}
		receipt.InitialVolumes, err = runtimeList(ctx, "volume")
		if err != nil {
			return err
		}
	}
	if !fresh {
		if err := captureRuntimeResources(ctx, receipt, nil, false); err != nil {
			return err
		}
	}
	if err := prepareRuntimeCompose(ctx, receipt, fresh); err != nil {
		return err
	}
	for _, previous := range receipt.Containers {
		actual, err := inspectRuntimeContainer(ctx, previous.ID)
		if err != nil {
			return err
		}
		if !sameRuntimeContainer(previous, actual) {
			return fmt.Errorf("interrupted startup container identity changed")
		}
	}
	for _, bind := range receipt.Binds {
		if err := verifyRuntimePath(bind.Identity); err != nil {
			return err
		}
	}
	if err := saveRuntimeReceipt(*receipt); err != nil {
		return err
	}
	_, upErr := runRuntimeCompose(ctx, *receipt, true, "up", "--detach", "--build", "--no-recreate", "--wait", "--wait-timeout", "120")
	// A failed or interrupted up can still have allocated durable Docker resources.
	captureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	captureErr := captureRuntimeResources(captureCtx, receipt, nil, false)
	if upErr == nil && captureErr == nil && len(receipt.Containers) > 0 {
		receipt.State = "active"
	}
	if upErr == nil && len(receipt.Containers) == 0 {
		upErr = fmt.Errorf("evaluation Compose startup produced no recorded containers")
	}
	return errors.Join(upErr, captureErr, saveRuntimeReceipt(*receipt))
}

func assertUnusedRuntimeProject(ctx context.Context, project string) error {
	for _, kind := range []string{"container", "network", "volume"} {
		args := []string{kind, "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + project}
		if kind == "container" {
			args = append(args, "--all")
		}
		data, err := runtimeDocker(ctx, args...)
		if err != nil {
			return err
		}
		if len(data) > 0 {
			return fmt.Errorf("evaluation Compose project already owns resources; use its receipt or explicit record")
		}
	}
	return nil
}

func runRuntimeCompose(ctx context.Context, receipt runtimeReceipt, overlay bool, args ...string) ([]byte, error) {
	command := []string{"compose", "--project-name", receipt.Project, "--file", receipt.ComposeFile}
	if overlay {
		command = append(command, "--file", filepath.Join(receipt.Directory, "runtime-compose.json"))
	}
	command = append(command, args...)
	bounded, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	process := exec.CommandContext(bounded, "docker", command...)
	process.Env = append(os.Environ(), "V1_COMPOSE_DATA_DIR="+receipt.Directory)
	data, err := process.Output()
	if err != nil {
		return nil, fmt.Errorf("evaluation Compose %s failed: %w", args[0], err)
	}
	return data, nil
}

func prepareRuntimeCompose(ctx context.Context, receipt *runtimeReceipt, fresh bool) error {
	data, err := runRuntimeCompose(ctx, *receipt, false, "config", "--format", "json")
	if err != nil {
		return err
	}
	var config struct {
		Services map[string]struct {
			Build   json.RawMessage
			Volumes []struct {
				Type, Source string
				ReadOnly     bool `json:"read_only"`
			}
		}
		Volumes map[string]struct {
			Name     string
			External bool
		}
		Networks map[string]struct {
			Name     string
			External bool
		}
	}
	if err := json.Unmarshal(data, &config); err != nil || len(config.Services) == 0 {
		return fmt.Errorf("invalid evaluation Compose configuration")
	}
	all, err := allRuntimeContainers(ctx)
	if err != nil {
		return err
	}
	services := map[string]any{}
	for name, service := range config.Services {
		override := map[string]any{"labels": map[string]string{runtimeOwnerLabel: receipt.ID}}
		if len(service.Build) != 0 && string(service.Build) != "null" {
			override["build"] = map[string]any{"labels": map[string]string{runtimeOwnerLabel: receipt.ID}}
		}
		services[name] = override
		for _, mount := range service.Volumes {
			if mount.Type == "volume" && mount.Source != "" && !mount.ReadOnly {
				name := config.Volumes[mount.Source].Name
				if name == "" {
					return fmt.Errorf("named runtime volume has no resolved identity")
				}
				for _, container := range all {
					if !fresh && container.Project == receipt.Project && container.Owner == receipt.ID {
						continue
					}
					if slices.ContainsFunc(container.Mounts, func(existing runtimeMount) bool { return existing.Type == "volume" && existing.Name == name }) {
						return fmt.Errorf("runtime named-volume storage overlaps another container's mount")
					}
				}
			}
			if mount.Type != "bind" || mount.ReadOnly {
				continue
			}
			path, err := runtimeCleanPath(mount.Source)
			if err != nil {
				return err
			}
			if !pairWithin(receipt.Directory, path) {
				return fmt.Errorf("writable runtime storage must be inside --runtime-dir")
			}
			for _, container := range all {
				if !fresh && container.Project == receipt.Project && container.Owner == receipt.ID {
					continue
				}
				for _, existing := range container.Mounts {
					if existing.Type == "bind" && runtimeOverlap(path, existing.Source) {
						if runtimeExecutorContainer(ctx, container) {
							continue
						}
						return fmt.Errorf("runtime storage overlaps another container's mount")
					}
				}
			}
			if fresh {
				entries, err := os.ReadDir(path)
				if err != nil && !os.IsNotExist(err) {
					return err
				}
				if len(entries) != 0 {
					return fmt.Errorf("new runtime storage is not empty; enroll existing storage explicitly")
				}
			}
		}
	}
	networks, volumes := map[string]any{}, map[string]any{}
	for name, network := range config.Networks {
		if !network.External {
			networks[name] = map[string]any{"labels": map[string]string{runtimeOwnerLabel: receipt.ID}}
		}
	}
	for name, volume := range config.Volumes {
		if !volume.External {
			volumes[name] = map[string]any{"labels": map[string]string{runtimeOwnerLabel: receipt.ID}}
		}
	}
	return writePairJSON(filepath.Join(receipt.Directory, "runtime-compose.json"), map[string]any{"services": services, "networks": networks, "volumes": volumes})
}

func verifyActiveRuntime(ctx context.Context, receipt runtimeReceipt) error {
	if receipt.State != "active" {
		return fmt.Errorf("runtime is %s, not active", receipt.State)
	}
	for _, expected := range receipt.Containers {
		actual, err := inspectRuntimeContainer(ctx, expected.ID)
		if err != nil {
			return err
		}
		if !sameRuntimeContainer(expected, actual) {
			return fmt.Errorf("runtime container identity or mounts changed")
		}
	}
	for _, bind := range receipt.Binds {
		if err := verifyRuntimePath(bind.Identity); err != nil {
			return err
		}
	}
	return nil
}

func CheckPairRuntimeLifecycle(c PairConfig, side string) error {
	release, err := pairRelease(c, side)
	if err != nil {
		return err
	}
	dir, err := runtimeCleanPath(release.Runtime.StateDir)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(dir, runtimeReceiptName)); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	lock, err := runtimeLock(filepath.Join(dir, "runtime.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	receipt, err := readRuntimeReceipt(dir)
	if err != nil {
		return err
	}
	if receipt.State != "active" || receipt.Project != release.Runtime.Project {
		return fmt.Errorf("paired runtime is not active in its recorded project")
	}
	for _, name := range []string{release.Runtime.ServerContainer, release.Runtime.PostgresContainer} {
		if !slices.ContainsFunc(receipt.Containers, func(container runtimeContainer) bool { return container.Name == name }) {
			return fmt.Errorf("paired runtime containers are absent from the ownership receipt")
		}
	}
	if err := verifyActiveRuntime(context.Background(), receipt); err != nil {
		return err
	}
	if !slices.Contains(receipt.MonitorDirs, c.OutDir) {
		receipt.MonitorDirs = append(receipt.MonitorDirs, c.OutDir)
		return saveRuntimeReceipt(receipt)
	}
	return nil
}
