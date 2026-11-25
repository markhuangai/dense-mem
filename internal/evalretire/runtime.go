package evalretire

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
)

func RunRuntimeCommand(ctx context.Context, cmd RuntimeCommand, out io.Writer) error {
	switch cmd.Action {
	case "record", "retire", "verify":
	default:
		return fmt.Errorf("maintenance action must be record, retire or verify")
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
	if fresh && cmd.Action != "record" {
		return fmt.Errorf("missing runtime ownership receipt; explicitly record the prepared runtime first")
	}
	if fresh && !runtimeName.MatchString(cmd.Project) {
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
