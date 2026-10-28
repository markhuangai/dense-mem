package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
)

func runtimeDocker(ctx context.Context, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(bounded, "docker", args...).Output()
	if err != nil {
		// Compose diagnostics can contain interpolated operational credentials.
		return nil, fmt.Errorf("docker %s failed: %w", args[0], err)
	}
	return output, nil
}

func runtimeList(ctx context.Context, kind string) ([]string, error) {
	args := []string{kind, "ls", "--quiet"}
	if kind == "container" || kind == "image" {
		args = append(args, "--all")
	}
	if kind != "volume" {
		args = append(args, "--no-trunc")
	}
	output, err := runtimeDocker(ctx, args...)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(output))
	sort.Strings(ids)
	return slices.Compact(ids), nil
}

func runtimePresent(ctx context.Context, kind, id string) (bool, error) {
	ids, err := runtimeList(ctx, kind)
	return slices.Contains(ids, id), err
}

func inspectRuntimeContainer(ctx context.Context, id string) (runtimeContainer, error) {
	var raw []struct {
		ID, Name, Image, Created string
		RestartCount             int
		Config                   struct {
			Labels   map[string]string
			Hostname string
			Volumes  map[string]json.RawMessage
		}
		HostConfig struct {
			Mounts             []struct{ Type, Source, Target string }
			Binds, VolumesFrom []string
		}
		State struct {
			Running   bool
			StartedAt string
			Status    string
			Health    struct{ Status string }
		}
		Mounts          []runtimeMount
		NetworkSettings struct {
			Networks map[string]struct{ NetworkID string }
		}
	}
	data, err := runtimeDocker(ctx, "container", "inspect", id)
	if err != nil {
		return runtimeContainer{}, err
	}
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) != 1 {
		return runtimeContainer{}, fmt.Errorf("invalid Docker container inspection")
	}
	c := raw[0]
	for i := range c.Mounts {
		mount := &c.Mounts[i]
		if mount.Type != "volume" || len(c.HostConfig.VolumesFrom) != 0 {
			continue
		}
		_, mount.Anonymous = c.Config.Volumes[mount.Destination]
		for _, requested := range c.HostConfig.Mounts {
			if requested.Target == mount.Destination {
				mount.Anonymous = requested.Type == "volume" && requested.Source == ""
			}
		}
		for _, bind := range c.HostConfig.Binds {
			if parts := strings.Split(bind, ":"); len(parts) > 1 && parts[1] == mount.Destination {
				mount.Anonymous = false
			}
		}
	}
	networks := []string{}
	for name, network := range c.NetworkSettings.Networks {
		if network.NetworkID == "" {
			// Created containers name their network before Docker assigns the attachment ID.
			resolved, err := inspectRuntimeNetwork(ctx, name, c.Config.Labels["com.docker.compose.project"])
			if err != nil {
				return runtimeContainer{}, err
			}
			network.NetworkID = resolved.ID
		}
		networks = append(networks, network.NetworkID)
	}
	sort.Strings(networks)
	sort.Slice(c.Mounts, func(i, j int) bool { return c.Mounts[i].Destination < c.Mounts[j].Destination })
	return runtimeContainer{ID: c.ID, Name: strings.TrimPrefix(c.Name, "/"), Hostname: c.Config.Hostname, ImageID: c.Image,
		Project: c.Config.Labels["com.docker.compose.project"], Service: c.Config.Labels["com.docker.compose.service"],
		Owner: c.Config.Labels[runtimeOwnerLabel], Created: c.Created, Mounts: c.Mounts, NetworkIDs: networks,
		Running: c.State.Running, StartedAt: c.State.StartedAt, RestartCount: c.RestartCount, Health: c.State.Health.Status, Status: c.State.Status}, nil
}

func allRuntimeContainers(ctx context.Context) ([]runtimeContainer, error) {
	ids, err := runtimeList(ctx, "container")
	if err != nil {
		return nil, err
	}
	return inspectRuntimeContainers(ctx, ids)
}

func inspectRuntimeContainers(ctx context.Context, ids []string) ([]runtimeContainer, error) {
	containers := make([]runtimeContainer, 0, len(ids))
	for _, id := range ids {
		container, err := inspectRuntimeContainer(ctx, id)
		if err != nil {
			// Another Docker user can remove a listed container before inspection.
			present, presenceErr := runtimePresent(ctx, "container", id)
			if presenceErr != nil || present {
				return nil, errors.Join(err, presenceErr)
			}
			continue
		}
		containers = append(containers, container)
	}
	return containers, nil
}

func runtimeExecutorContainer(ctx context.Context, container runtimeContainer) bool {
	hostname, err := os.Hostname()
	if err != nil || container.Hostname != hostname {
		return false
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return false
	}
	pid, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return false
	}
	mount, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return false
	}
	// The command's own workspace mount is not a competing evaluation's storage claim.
	identity, err := runtimeDocker(ctx, "container", "exec", container.ID, "sh", "-c", "cat /proc/sys/kernel/random/boot_id; readlink /proc/1/ns/pid; readlink /proc/1/ns/mnt")
	return err == nil && slices.Equal(strings.Fields(string(identity)), []string{strings.TrimSpace(string(boot)), pid, mount})
}

func inspectRuntimeNetwork(ctx context.Context, id, project string) (runtimeNetwork, error) {
	var raw []struct {
		ID, Name string
		Labels   map[string]string
	}
	data, err := runtimeDocker(ctx, "network", "inspect", "--", id)
	if err != nil {
		return runtimeNetwork{}, err
	}
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) != 1 {
		return runtimeNetwork{}, fmt.Errorf("invalid Docker network inspection")
	}
	n := raw[0]
	owner := n.Labels["com.docker.compose.project"]
	return runtimeNetwork{ID: n.ID, Name: n.Name, Project: owner, Owner: n.Labels[runtimeOwnerLabel], Owned: owner == project}, nil
}

func inspectRuntimeVolume(ctx context.Context, name, project string) (runtimeVolume, error) {
	var raw []struct {
		Name, CreatedAt, Mountpoint string
		Labels                      map[string]string
	}
	data, err := runtimeDocker(ctx, "volume", "inspect", name)
	if err != nil {
		return runtimeVolume{}, err
	}
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) != 1 {
		return runtimeVolume{}, fmt.Errorf("invalid Docker volume inspection")
	}
	v := raw[0]
	owner := v.Labels["com.docker.compose.project"]
	return runtimeVolume{Name: v.Name, CreatedAt: v.CreatedAt, Mountpoint: v.Mountpoint, Project: owner, Owner: v.Labels[runtimeOwnerLabel], Owned: owner == project}, nil
}

func inspectRuntimeImage(ctx context.Context, id string) (runtimeImage, error) {
	var raw []struct {
		ID       string
		RepoTags []string
		Config   struct{ Labels map[string]string }
	}
	data, err := runtimeDocker(ctx, "image", "inspect", id)
	if err != nil {
		return runtimeImage{}, err
	}
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) != 1 {
		return runtimeImage{}, fmt.Errorf("invalid Docker image inspection")
	}
	sort.Strings(raw[0].RepoTags)
	return runtimeImage{ID: raw[0].ID, Tags: raw[0].RepoTags, Owner: raw[0].Config.Labels[runtimeOwnerLabel]}, nil
}

func sameRuntimeContainer(a, b runtimeContainer) bool {
	return a.ID == b.ID && a.Name == b.Name && a.ImageID == b.ImageID && a.Project == b.Project &&
		a.Service == b.Service && a.Owner == b.Owner && a.Created == b.Created && reflect.DeepEqual(a.Mounts, b.Mounts) && reflect.DeepEqual(a.NetworkIDs, b.NetworkIDs)
}

func captureRuntimeResources(ctx context.Context, receipt *runtimeReceipt, fixtureIDs []string, adopted bool) error {
	original := receipt
	working := *receipt
	receipt = &working
	previousImages := slices.Clone(receipt.Images)
	previousVolumes := slices.Clone(receipt.Volumes)
	previousNetworks := slices.Clone(receipt.Networks)
	all, err := allRuntimeContainers(ctx)
	if err != nil {
		return err
	}
	containers := []runtimeContainer{}
	images, networks, volumes, binds := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	anonymousVolumes := map[string]bool{}
	for _, container := range all {
		fixture := slices.Contains(fixtureIDs, container.ID)
		if fixture {
			if container.Running || container.Status != "exited" || container.Owner != receipt.ID {
				return fmt.Errorf("fixture must be terminal and carry this runtime's exact ownership label")
			}
			container.Fixture = true
		} else if container.Project != receipt.Project {
			continue
		}
		known := slices.ContainsFunc(receipt.Containers, func(c runtimeContainer) bool { return sameRuntimeContainer(c, container) })
		if !known && container.Owner != receipt.ID && !(adopted && container.Owner == "") {
			return fmt.Errorf("unrecorded container lacks this runtime's ownership label")
		}
		containers = append(containers, container)
		images[container.ImageID] = true
		for _, id := range container.NetworkIDs {
			networks[id] = true
		}
		for _, mount := range container.Mounts {
			if mount.Type == "volume" {
				volumes[mount.Name] = true
				if mount.Anonymous {
					anonymousVolumes[mount.Name] = true
				}
			}
			if mount.Type != "bind" || !mount.RW {
				continue
			}
			identity, err := runtimePathIdentity(mount.Source)
			if err != nil {
				return err
			}
			if !pairWithin(receipt.Directory, identity.Path) {
				return fmt.Errorf("writable runtime bind must be a strict child of --runtime-dir")
			}
			if !binds[identity.Path] {
				for _, previous := range receipt.Binds {
					if previous.Identity.Path == identity.Path && previous.Identity != identity {
						return fmt.Errorf("runtime bind identity changed during resource capture")
					}
				}
				binds[identity.Path] = true
				if !slices.ContainsFunc(receipt.Binds, func(b runtimeBind) bool { return b.Identity.Path == identity.Path }) {
					receipt.Binds = append(receipt.Binds, runtimeBind{Identity: identity, ImageID: container.ImageID})
				}
			}
		}
	}
	for _, fixture := range fixtureIDs {
		if !slices.ContainsFunc(containers, func(c runtimeContainer) bool { return c.ID == fixture && c.Fixture }) {
			return fmt.Errorf("explicit terminal fixture is missing")
		}
	}
	for _, previous := range receipt.Containers {
		if !slices.ContainsFunc(containers, func(c runtimeContainer) bool { return sameRuntimeContainer(previous, c) }) {
			return fmt.Errorf("recorded runtime container identity changed")
		}
	}
	receipt.Containers = containers
	for kind, set := range map[string]map[string]bool{"network": networks, "volume": volumes} {
		ids, err := runtimeDocker(ctx, kind, "ls", "--quiet", "--filter", "label=com.docker.compose.project="+receipt.Project)
		if err != nil {
			return err
		}
		for _, id := range strings.Fields(string(ids)) {
			set[id] = true
		}
	}
	receipt.Networks = nil
	for id := range networks {
		network, err := inspectRuntimeNetwork(ctx, id, receipt.Project)
		if err != nil {
			return err
		}
		known := slices.ContainsFunc(previousNetworks, func(n runtimeNetwork) bool {
			return n.ID == network.ID && n.Owner == network.Owner && n.Project == network.Project
		})
		if network.Owned && !known && network.Owner != receipt.ID && !(adopted && network.Owner == "") {
			return fmt.Errorf("unrecorded network lacks this runtime's ownership label")
		}
		if !slices.ContainsFunc(receipt.Networks, func(n runtimeNetwork) bool { return n.ID == network.ID }) {
			receipt.Networks = append(receipt.Networks, network)
		}
	}
	for _, previous := range previousNetworks {
		if !slices.ContainsFunc(receipt.Networks, func(n runtimeNetwork) bool {
			return n.ID == previous.ID && n.Owner == previous.Owner && n.Project == previous.Project
		}) {
			return fmt.Errorf("recorded network identity changed")
		}
	}
	receipt.Volumes = nil
	for name := range volumes {
		volume, err := inspectRuntimeVolume(ctx, name, receipt.Project)
		if err != nil {
			return err
		}
		known := slices.ContainsFunc(previousVolumes, func(v runtimeVolume) bool {
			return v.Name == volume.Name && v.CreatedAt == volume.CreatedAt && v.Owner == volume.Owner
		})
		if volume.Project == receipt.Project && !known && volume.Owner != receipt.ID && !(adopted && volume.Owner == "") {
			return fmt.Errorf("unrecorded volume lacks this runtime's ownership label")
		}
		// Only a source-free creation request proves anonymous allocation by this container.
		if volume.Project == "" && anonymousVolumes[name] && (!slices.Contains(receipt.InitialVolumes, name) || adopted) {
			volume.Owned = true
		}
		for _, previous := range previousVolumes {
			if previous.Name == name {
				if previous.CreatedAt != volume.CreatedAt || previous.Mountpoint != volume.Mountpoint || previous.Project != volume.Project || previous.Owner != volume.Owner {
					return fmt.Errorf("recorded volume identity changed")
				}
				volume.Owned = previous.Owned
			}
		}
		receipt.Volumes = append(receipt.Volumes, volume)
	}
	built, err := runtimeDocker(ctx, "image", "ls", "--quiet", "--no-trunc", "--filter", "label="+runtimeOwnerLabel+"="+receipt.ID)
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(built)) {
		images[id] = true
	}
	receipt.Images = nil
	for id := range images {
		image, err := inspectRuntimeImage(ctx, id)
		if err != nil {
			return err
		}
		image.Owned = !adopted && image.Owner == receipt.ID && !slices.Contains(receipt.InitialImages, id)
		for _, previous := range previousImages {
			if previous.ID == id {
				image.Owned, image.Tags = previous.Owned, previous.Tags
			}
		}
		receipt.Images = append(receipt.Images, image)
	}
	sort.Slice(receipt.Containers, func(i, j int) bool { return receipt.Containers[i].ID < receipt.Containers[j].ID })
	sort.Slice(receipt.Networks, func(i, j int) bool { return receipt.Networks[i].ID < receipt.Networks[j].ID })
	sort.Slice(receipt.Volumes, func(i, j int) bool { return receipt.Volumes[i].Name < receipt.Volumes[j].Name })
	sort.Slice(receipt.Images, func(i, j int) bool { return receipt.Images[i].ID < receipt.Images[j].ID })
	sort.Slice(receipt.Binds, func(i, j int) bool { return receipt.Binds[i].Identity.Path < receipt.Binds[j].Identity.Path })
	*original = *receipt
	return nil
}
