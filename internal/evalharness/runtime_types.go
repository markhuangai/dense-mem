package evalharness

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const runtimeSchema = "dense-mem.eval.runtime.v1"
const runtimeReceiptName = "runtime-resources.json"
const runtimeOwnerLabel = "io.dense-mem.eval.runtime"

type RuntimeCommand struct {
	Action        string
	Directory     string
	Project       string
	ComposeFile   string
	RetentionPlan string
	Supersedes    string
	New           bool
	Fixtures      []string
	StopTimeout   time.Duration
}

type runtimeReceipt struct {
	SchemaVersion  string             `json:"schema_version"`
	ID             string             `json:"runtime_id"`
	Directory      string             `json:"directory"`
	Project        string             `json:"project"`
	State          string             `json:"state"`
	CreatedAt      time.Time          `json:"created_at"`
	ComposeFile    string             `json:"compose_file,omitempty"`
	ComposeHash    string             `json:"compose_sha256,omitempty"`
	Supersedes     string             `json:"supersedes,omitempty"`
	InitialImages  []string           `json:"initial_image_ids,omitempty"`
	InitialVolumes []string           `json:"initial_volumes,omitempty"`
	Containers     []runtimeContainer `json:"containers"`
	Networks       []runtimeNetwork   `json:"networks"`
	Volumes        []runtimeVolume    `json:"volumes"`
	Images         []runtimeImage     `json:"images"`
	Binds          []runtimeBind      `json:"binds"`
	MonitorDirs    []string           `json:"monitor_dirs,omitempty"`
	Retirement     *runtimeRetirement `json:"retirement,omitempty"`
}

type runtimeFileIdentity struct {
	Path   string `json:"path"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type runtimeBind struct {
	Identity runtimeFileIdentity `json:"identity"`
	ImageID  string              `json:"cleanup_image_id"`
}

type runtimeMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name,omitempty"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
	Anonymous   bool   `json:"anonymous,omitempty"`
}

type runtimeContainer struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Hostname     string         `json:"hostname"`
	ImageID      string         `json:"image_id"`
	Project      string         `json:"project"`
	Service      string         `json:"service"`
	Owner        string         `json:"owner,omitempty"`
	Created      string         `json:"created"`
	Mounts       []runtimeMount `json:"mounts"`
	NetworkIDs   []string       `json:"network_ids"`
	Running      bool           `json:"running"`
	StartedAt    string         `json:"started_at"`
	RestartCount int            `json:"restart_count"`
	Health       string         `json:"health"`
	Status       string         `json:"status"`
	Fixture      bool           `json:"fixture,omitempty"`
}

type runtimeNetwork struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Project string `json:"project"`
	Owner   string `json:"owner,omitempty"`
	Owned   bool   `json:"owned"`
}

type runtimeVolume struct {
	Name       string `json:"name"`
	CreatedAt  string `json:"created_at"`
	Mountpoint string `json:"mountpoint"`
	Project    string `json:"project"`
	Owner      string `json:"owner,omitempty"`
	Owned      bool   `json:"owned"`
}

type runtimeImage struct {
	ID    string   `json:"id"`
	Tags  []string `json:"tags"`
	Owner string   `json:"owner,omitempty"`
	Owned bool     `json:"owned"`
}

type runtimeRetirement struct {
	PlanHash     string                      `json:"plan_sha256"`
	Archive      runtimeArchive              `json:"archive"`
	Protected    []runtimeContainer          `json:"protected_containers"`
	Dispositions map[string]string           `json:"dispositions"`
	Helpers      map[string]runtimeContainer `json:"helpers"`
	StartedAt    time.Time                   `json:"started_at"`
	VerifiedAt   *time.Time                  `json:"verified_at,omitempty"`
}

func runtimeCleanPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("runtime path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil || abs == string(filepath.Separator) {
		return "", fmt.Errorf("invalid runtime path")
	}
	for current := abs; current != string(filepath.Separator); current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("runtime path contains a symlink: %s", current)
		}
	}
	return abs, nil
}

func runtimePathIdentity(path string) (runtimeFileIdentity, error) {
	abs, err := runtimeCleanPath(path)
	if err != nil {
		return runtimeFileIdentity{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return runtimeFileIdentity{}, err
	}
	if !info.IsDir() {
		return runtimeFileIdentity{}, fmt.Errorf("runtime storage is not a directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return runtimeFileIdentity{}, fmt.Errorf("runtime storage requires Unix file identities")
	}
	return runtimeFileIdentity{Path: abs, Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func verifyRuntimePath(expected runtimeFileIdentity) error {
	actual, err := runtimePathIdentity(expected.Path)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("runtime storage identity changed: %s", expected.Path)
	}
	return nil
}

func runtimeOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func runtimeLock(path string) (*os.File, error) {
	if _, err := runtimeCleanPath(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, fmt.Errorf("runtime is owned by another process: %w", err)
	}
	return file, nil
}

func readRuntimeReceipt(dir string) (runtimeReceipt, error) {
	var receipt runtimeReceipt
	if err := readPairJSON(filepath.Join(dir, runtimeReceiptName), &receipt); err != nil {
		return receipt, err
	}
	if receipt.SchemaVersion != runtimeSchema || receipt.Directory != dir || receipt.ID == "" || !pairName.MatchString(receipt.Project) {
		return receipt, fmt.Errorf("invalid runtime receipt identity")
	}
	switch receipt.State {
	case "preparing", "active", "retiring", "retired":
	default:
		return receipt, fmt.Errorf("invalid runtime lifecycle state")
	}
	return receipt, nil
}

func saveRuntimeReceipt(receipt runtimeReceipt) error {
	return writePairJSON(filepath.Join(receipt.Directory, runtimeReceiptName), receipt)
}
