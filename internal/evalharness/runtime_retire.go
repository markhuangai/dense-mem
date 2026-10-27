package evalharness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
)

func retireEvalRuntime(ctx context.Context, cmd RuntimeCommand, receipt *runtimeReceipt) error {
	if receipt.State == "retired" {
		return verifyRetiredRuntime(ctx, *receipt)
	}
	if receipt.State == "preparing" {
		if err := captureRuntimeResources(ctx, receipt, nil, false); err != nil {
			return err
		}
		if err := saveRuntimeReceipt(*receipt); err != nil {
			return err
		}
	}
	locks, err := lockRuntimeResources(*receipt)
	if err != nil {
		return err
	}
	defer func() {
		for _, lock := range locks {
			lock.Close()
		}
	}()
	planPath, err := runtimeRetentionPlanPath(cmd.RetentionPlan, *receipt)
	if err != nil {
		return err
	}
	var plan runtimeRetentionPlan
	if err := readPairJSON(planPath, &plan); err != nil {
		return fmt.Errorf("retirement requires a readable retention plan: %w", err)
	}
	if err := validateRuntimeRetention(&plan, *receipt); err != nil {
		return err
	}
	planHash, err := canonicalJSONHash(plan)
	if err != nil {
		return err
	}
	if err := verifyRetirementOwnership(ctx, *receipt); err != nil {
		return err
	}
	if receipt.Retirement == nil {
		protected, err := captureProtectedRuntimes(ctx, plan.ProtectedRuntimes)
		if err != nil {
			return err
		}
		archive, err := archiveRuntimeEvidence(plan)
		if err != nil {
			return err
		}
		receipt.Retirement = &runtimeRetirement{PlanHash: planHash, Archive: archive, Protected: protected,
			Dispositions: map[string]string{}, Helpers: map[string]runtimeContainer{}, StartedAt: time.Now().UTC()}
		receipt.State = "retiring"
		if err := saveRuntimeReceipt(*receipt); err != nil {
			return err
		}
	} else if receipt.State != "retiring" || receipt.Retirement.PlanHash != planHash {
		return fmt.Errorf("retirement intent or retention plan changed")
	}
	if err := verifyRuntimeArchive(receipt.Retirement.Archive); err != nil {
		return err
	}
	if err := verifyProtectedRuntimes(ctx, receipt.Retirement.Protected); err != nil {
		return err
	}
	containers := slices.Clone(receipt.Containers)
	slices.SortStableFunc(containers, func(a, b runtimeContainer) int {
		if a.Service == "server" && b.Service != "server" {
			return -1
		}
		if b.Service == "server" && a.Service != "server" {
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, container := range containers {
		present, err := runtimePresent(ctx, "container", container.ID)
		if err != nil {
			return err
		}
		if present {
			actual, err := inspectRuntimeContainer(ctx, container.ID)
			if err != nil {
				return err
			}
			if !sameRuntimeContainer(container, actual) {
				return fmt.Errorf("container identity changed before retirement")
			}
			if err := stopRuntimeContainer(ctx, actual, cmd.StopTimeout); err != nil {
				return err
			}
			if _, err := runtimeDocker(ctx, "container", "rm", container.ID); err != nil {
				return err
			}
		}
		if err := recordRuntimeDisposition(receipt, "container:"+container.ID, "removed"); err != nil {
			return err
		}
	}
	for _, bind := range receipt.Binds {
		if err := retireRuntimeBind(ctx, cmd, receipt, bind); err != nil {
			return err
		}
	}
	for _, network := range receipt.Networks {
		if err := retireRuntimeResource(ctx, receipt, "network", network.ID, network.Owned); err != nil {
			return err
		}
	}
	for _, volume := range receipt.Volumes {
		if err := retireRuntimeResource(ctx, receipt, "volume", volume.Name, volume.Owned); err != nil {
			return err
		}
	}
	for _, image := range receipt.Images {
		if err := retireRuntimeResource(ctx, receipt, "image", image.ID, image.Owned); err != nil {
			return err
		}
	}
	if err := verifyRetirementReadback(ctx, *receipt); err != nil {
		return err
	}
	if err := verifyProtectedRuntimes(ctx, receipt.Retirement.Protected); err != nil {
		return err
	}
	now := time.Now().UTC()
	receipt.State, receipt.Retirement.VerifiedAt = "retired", &now
	return saveRuntimeReceipt(*receipt)
}

func lockRuntimeResources(receipt runtimeReceipt) ([]*os.File, error) {
	dir, err := runtimeLockDirectory()
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for _, monitor := range receipt.MonitorDirs {
		paths = append(paths, filepath.Join(monitor, "paired.lock"))
	}
	for _, container := range receipt.Containers {
		if !pairSHA.MatchString("sha256:" + container.ID) {
			return nil, fmt.Errorf("invalid recorded container identity")
		}
		paths = append(paths, filepath.Join(dir, container.ID+".lock"))
	}
	slices.Sort(paths)
	var locks []*os.File
	for _, path := range slices.Compact(paths) {
		lock, err := runtimeLock(path)
		if err != nil {
			for _, held := range locks {
				held.Close()
			}
			return nil, err
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func stopRuntimeContainer(ctx context.Context, container runtimeContainer, timeout time.Duration) error {
	if !container.Running {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Infinite Docker grace prevents its default SIGKILL escalation; the caller bounds observation.
	if _, err := runtimeDocker(bounded, "container", "stop", "--signal=SIGTERM", "--timeout=-1", container.ID); err != nil {
		return fmt.Errorf("graceful container stop incomplete; no force removal attempted: %w", err)
	}
	actual, err := inspectRuntimeContainer(ctx, container.ID)
	if err != nil {
		return err
	}
	if actual.Running {
		return fmt.Errorf("container remained running after graceful stop")
	}
	return nil
}

func verifyRetirementOwnership(ctx context.Context, receipt runtimeReceipt) error {
	all, err := allRuntimeContainers(ctx)
	if err != nil {
		return err
	}
	for _, actual := range all {
		if actual.Project == receipt.Project && !slices.ContainsFunc(receipt.Containers, func(c runtimeContainer) bool { return c.ID == actual.ID }) {
			return fmt.Errorf("unrecorded container now occupies the retired Compose project")
		}
	}
	for _, expected := range receipt.Containers {
		found := false
		for _, actual := range all {
			if actual.ID == expected.ID {
				found = true
				if !sameRuntimeContainer(expected, actual) {
					return fmt.Errorf("recorded container identity changed")
				}
			} else if actual.Name == expected.Name {
				return fmt.Errorf("recorded container name now identifies another container")
			}
		}
		if !found && receipt.State != "retiring" {
			return fmt.Errorf("container disappeared before retirement intent")
		}
	}
	for _, bind := range receipt.Binds {
		if !pairWithin(receipt.Directory, bind.Identity.Path) {
			return fmt.Errorf("disposable bind is outside its runtime directory")
		}
		if err := verifyRuntimePath(bind.Identity); err != nil {
			if receipt.State != "retiring" || !os.IsNotExist(err) {
				return err
			}
		}
	}
	for _, volume := range receipt.Volumes {
		present, err := runtimePresent(ctx, "volume", volume.Name)
		if err != nil {
			return err
		}
		if !present {
			if receipt.State != "retiring" {
				return fmt.Errorf("volume disappeared before retirement intent")
			}
			continue
		}
		actual, err := inspectRuntimeVolume(ctx, volume.Name, receipt.Project)
		if err != nil {
			return err
		}
		if actual.CreatedAt != volume.CreatedAt || actual.Mountpoint != volume.Mountpoint || actual.Project != volume.Project || actual.Owner != volume.Owner {
			return fmt.Errorf("recorded volume identity changed")
		}
	}
	return nil
}

func captureProtectedRuntimes(ctx context.Context, directories []string) ([]runtimeContainer, error) {
	var result []runtimeContainer
	for _, dir := range directories {
		receipt, err := readRuntimeReceipt(dir)
		if err != nil {
			return nil, err
		}
		if err := verifyActiveRuntime(ctx, receipt); err != nil {
			return nil, err
		}
		for _, expected := range receipt.Containers {
			actual, err := inspectRuntimeContainer(ctx, expected.ID)
			if err != nil {
				return nil, err
			}
			if !expected.Fixture && (!actual.Running || (actual.Health != "" && actual.Health != "healthy")) {
				return nil, fmt.Errorf("protected runtime is not healthy")
			}
			result = append(result, actual)
		}
	}
	return result, nil
}

func verifyProtectedRuntimes(ctx context.Context, expected []runtimeContainer) error {
	for _, container := range expected {
		actual, err := inspectRuntimeContainer(ctx, container.ID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(container, actual) {
			return fmt.Errorf("protected runtime identity, mounts, restart count or health changed")
		}
	}
	return nil
}

func recordRuntimeDisposition(receipt *runtimeReceipt, key, disposition string) error {
	receipt.Retirement.Dispositions[key] = disposition
	return saveRuntimeReceipt(*receipt)
}

func runtimeReferenced(ctx context.Context, receipt runtimeReceipt, kind, id string) (bool, error) {
	all, err := allRuntimeContainers(ctx)
	if err != nil {
		return false, err
	}
	for _, container := range all {
		if slices.ContainsFunc(receipt.Containers, func(c runtimeContainer) bool { return c.ID == container.ID }) ||
			slices.ContainsFunc(runtimeHelperContainers(receipt), func(c runtimeContainer) bool { return c.ID == container.ID }) {
			continue
		}
		if kind == "image" && container.ImageID == id || kind == "network" && slices.Contains(container.NetworkIDs, id) {
			return true, nil
		}
		for _, mount := range container.Mounts {
			if kind == "bind" && mount.Type == "bind" && runtimeOverlap(mount.Source, id) && runtimeExecutorContainer(ctx, container) {
				continue
			}
			if kind == "volume" && mount.Type == "volume" && mount.Name == id || kind == "bind" && mount.Type == "bind" && runtimeOverlap(mount.Source, id) {
				return true, nil
			}
		}
	}
	return false, nil
}

func runtimeHelperContainers(receipt runtimeReceipt) []runtimeContainer {
	var helpers []runtimeContainer
	if receipt.Retirement != nil {
		for _, container := range receipt.Retirement.Helpers {
			helpers = append(helpers, container)
		}
	}
	return helpers
}

func retireRuntimeResource(ctx context.Context, receipt *runtimeReceipt, kind, id string, owned bool) error {
	key := kind + ":" + id
	if !owned {
		return recordRuntimeDisposition(receipt, key, "retained_external")
	}
	if referenced, err := runtimeReferenced(ctx, *receipt, kind, id); err != nil {
		return err
	} else if referenced {
		return recordRuntimeDisposition(receipt, key, "retained_shared")
	}
	present, err := runtimePresent(ctx, kind, id)
	if err != nil {
		return err
	}
	if present {
		if kind == "volume" {
			actual, err := inspectRuntimeVolume(ctx, id, receipt.Project)
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(receipt.Volumes, func(v runtimeVolume) bool {
				return v.Name == actual.Name && v.CreatedAt == actual.CreatedAt && v.Mountpoint == actual.Mountpoint && v.Project == actual.Project && v.Owner == actual.Owner
			}) {
				return fmt.Errorf("volume identity changed before removal")
			}
		}
		if kind == "image" {
			actual, err := inspectRuntimeImage(ctx, id)
			if err != nil {
				return err
			}
			if len(actual.Tags) > 1 || !slices.ContainsFunc(receipt.Images, func(i runtimeImage) bool { return i.ID == id && slices.Equal(i.Tags, actual.Tags) }) {
				return recordRuntimeDisposition(receipt, key, "retained_shared_tags")
			}
		}
		if _, err := runtimeDocker(ctx, kind, "rm", id); err != nil {
			return err
		}
	}
	return recordRuntimeDisposition(receipt, key, "removed")
}

func verifyRetiredRuntime(ctx context.Context, receipt runtimeReceipt) error {
	if receipt.State != "retired" || receipt.Retirement == nil || receipt.Retirement.VerifiedAt == nil {
		return fmt.Errorf("verified retirement is incomplete")
	}
	return verifyRetirementReadback(ctx, receipt)
}

func verifyRetirementReadback(ctx context.Context, receipt runtimeReceipt) error {
	if receipt.Retirement == nil {
		return fmt.Errorf("retirement intent is missing")
	}
	if err := verifyRuntimeArchive(receipt.Retirement.Archive); err != nil {
		return err
	}
	keys := []string{}
	for _, container := range receipt.Containers {
		keys = append(keys, "container:"+container.ID)
	}
	for _, network := range receipt.Networks {
		keys = append(keys, "network:"+network.ID)
	}
	for _, volume := range receipt.Volumes {
		keys = append(keys, "volume:"+volume.Name)
	}
	for _, image := range receipt.Images {
		keys = append(keys, "image:"+image.ID)
	}
	for _, bind := range receipt.Binds {
		keys = append(keys, "bind:"+bind.Identity.Path)
	}
	for _, helper := range runtimeHelperContainers(receipt) {
		if helper.ID == "" {
			return fmt.Errorf("cleanup helper creation has not been reconciled")
		}
		keys = append(keys, "container:"+helper.ID)
	}
	for _, key := range keys {
		disposition := receipt.Retirement.Dispositions[key]
		if strings.HasPrefix(disposition, "retained_") && !strings.HasPrefix(key, "container:") {
			continue
		}
		if disposition != "removed" {
			return fmt.Errorf("retirement has an unfinished resource: %s", key)
		}
		kind, id, _ := strings.Cut(key, ":")
		if kind == "bind" {
			if _, err := os.Lstat(id); !os.IsNotExist(err) {
				return errors.Join(fmt.Errorf("retired bind storage is not proven absent"), err)
			}
		} else {
			present, err := runtimePresent(ctx, kind, id)
			if err != nil {
				return err
			}
			if present {
				return fmt.Errorf("retired %s is still present", kind)
			}
		}
	}
	return nil
}
