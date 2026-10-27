package evalharness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

func retireRuntimeBind(ctx context.Context, cmd RuntimeCommand, receipt *runtimeReceipt, bind runtimeBind) error {
	key := "bind:" + bind.Identity.Path
	if err := verifyRuntimePath(bind.Identity); os.IsNotExist(err) {
		if helper, exists := receipt.Retirement.Helpers[bind.Identity.Path]; exists {
			if err := removeRuntimeHelper(ctx, receipt, helper); err != nil {
				return err
			}
		}
		return recordRuntimeDisposition(receipt, key, "removed")
	} else if err != nil {
		return err
	}
	shared, err := runtimeReferenced(ctx, *receipt, "bind", bind.Identity.Path)
	if err != nil {
		return err
	}
	if shared {
		return recordRuntimeDisposition(receipt, key, "retained_shared")
	}
	if err := os.Remove(bind.Identity.Path); err == nil {
		if helper, exists := receipt.Retirement.Helpers[bind.Identity.Path]; exists {
			if err := removeRuntimeHelper(ctx, receipt, helper); err != nil {
				return err
			}
		}
		return recordRuntimeDisposition(receipt, key, "removed")
	}
	helper, exists := receipt.Retirement.Helpers[bind.Identity.Path]
	if !exists {
		hash := pairBytesHash([]byte(bind.Identity.Path))
		helper = runtimeContainer{Name: "densemem-retire-" + receipt.ID + "-" + hash[len("sha256:"):len("sha256:")+12], ImageID: bind.ImageID, Owner: receipt.ID}
		receipt.Retirement.Helpers[bind.Identity.Path] = helper
		if err := saveRuntimeReceipt(*receipt); err != nil {
			return err
		}
	}
	all, err := allRuntimeContainers(ctx)
	if err != nil {
		return err
	}
	var current runtimeContainer
	for _, container := range all {
		if container.Name == helper.Name {
			current = container
		}
	}
	if current.ID == "" {
		if helper.ID != "" {
			return fmt.Errorf("cleanup helper disappeared while bind storage still exists")
		}
		args := []string{"container", "create", "--name", helper.Name, "--network", "none", "--read-only", "--user", "0", "--security-opt", "no-new-privileges", "--label", runtimeOwnerLabel + "=" + receipt.ID,
			"--mount", "type=bind,source=" + bind.Identity.Path + ",target=/retired", "--entrypoint", "/bin/sh"}
		data, err := runtimeDocker(ctx, "image", "inspect", "--format", "{{json .Config.Volumes}}", bind.ImageID)
		if err != nil {
			return err
		}
		var volumes map[string]json.RawMessage
		if err := json.Unmarshal(data, &volumes); err != nil {
			return fmt.Errorf("invalid cleanup image volume declaration")
		}
		for path := range volumes {
			if path != "/retired" {
				args = append(args, "--tmpfs", path)
			}
		}
		args = append(args, bind.ImageID, "-ec", "find /retired -xdev -depth -mindepth 1 -delete")
		if err := verifyRuntimePath(bind.Identity); err != nil {
			return err
		}
		created, err := runtimeDocker(ctx, args...)
		if err != nil {
			return err
		}
		current, err = inspectRuntimeContainer(ctx, strings.TrimSpace(string(created)))
		if err != nil {
			return err
		}
	}
	if current.Owner != receipt.ID || current.ImageID != bind.ImageID || current.Name != helper.Name || (helper.ID != "" && current.ID != helper.ID) {
		return fmt.Errorf("cleanup helper ownership changed")
	}
	if !slices.ContainsFunc(current.Mounts, func(m runtimeMount) bool {
		return m.Type == "bind" && m.Source == bind.Identity.Path && m.Destination == "/retired" && m.RW
	}) {
		return fmt.Errorf("cleanup helper does not mount the exact retired storage")
	}
	for _, mount := range current.Mounts {
		if mount.Type != "tmpfs" && !(mount.Type == "bind" && mount.Destination == "/retired" && mount.Source == bind.Identity.Path) {
			return fmt.Errorf("cleanup helper has an unexpected mount")
		}
	}
	receipt.Retirement.Helpers[bind.Identity.Path] = current
	if err := saveRuntimeReceipt(*receipt); err != nil {
		return err
	}
	if shared, err := runtimeReferenced(ctx, *receipt, "bind", bind.Identity.Path); err != nil {
		return err
	} else if shared {
		return fmt.Errorf("retired storage acquired another container reference before deletion")
	}
	if err := verifyRuntimePath(bind.Identity); err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, cmd.StopTimeout)
	defer cancel()
	if !current.Running {
		if _, err := runtimeDocker(bounded, "container", "start", current.ID); err != nil {
			return err
		}
	}
	code, err := runtimeDocker(bounded, "container", "wait", current.ID)
	if err != nil {
		return fmt.Errorf("storage cleanup incomplete: %w", err)
	}
	if strings.TrimSpace(string(code)) != "0" {
		return fmt.Errorf("storage cleanup helper failed; retain its receipt and retry after diagnosis")
	}
	if err := removeRuntimeHelper(ctx, receipt, current); err != nil {
		return err
	}
	if err := verifyRuntimePath(bind.Identity); err != nil {
		return err
	}
	if err := os.Remove(bind.Identity.Path); err != nil {
		return fmt.Errorf("retired bind directory remains: %w", err)
	}
	return recordRuntimeDisposition(receipt, key, "removed")
}

func removeRuntimeHelper(ctx context.Context, receipt *runtimeReceipt, helper runtimeContainer) error {
	if helper.ID == "" {
		return fmt.Errorf("cleanup helper identity is unresolved")
	}
	present, err := runtimePresent(ctx, "container", helper.ID)
	if err != nil {
		return err
	}
	if present {
		actual, err := inspectRuntimeContainer(ctx, helper.ID)
		if err != nil {
			return err
		}
		if !sameRuntimeContainer(helper, actual) || actual.Running {
			return fmt.Errorf("cleanup helper changed or is still running")
		}
		if _, err := runtimeDocker(ctx, "container", "rm", helper.ID); err != nil {
			return err
		}
	}
	return recordRuntimeDisposition(receipt, "container:"+helper.ID, "removed")
}
