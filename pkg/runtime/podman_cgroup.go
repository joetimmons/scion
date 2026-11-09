// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package runtime

import (
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
)

// errCgroupMoveDenied means this process cannot join the delegated server cgroup.
var errCgroupMoveDenied = errors.New("cgroup move denied")

// cgroupMountPath and cgroupSelfFile are the cgroup v2 locations. Tests
// point them at a temporary tree.
var (
	cgroupMountPath = "/sys/fs/cgroup"
	cgroupSelfFile  = "/proc/self/cgroup"
)

// delegatedServiceCgroup is the sibling of an absolute agents cgroup.
// /scion-svc/agents places this server in /scion-svc/server so the agents
// cgroup can keep domain controllers without holding the server process.
func delegatedServiceCgroup(parent string) (string, error) {
	if !strings.HasPrefix(parent, "/") || strings.Contains(parent, "..") {
		return "", fmt.Errorf("cgroup_parent %q is not an absolute cgroup path", parent)
	}
	cleaned := path.Clean(parent)
	domain := path.Dir(cleaned)
	if domain == "/" || domain == "." {
		return "", fmt.Errorf("cgroup_parent %q has no domain above the agents cgroup", parent)
	}
	return domain + "/server", nil
}

func currentCgroupPath() (string, error) {
	raw, err := os.ReadFile(cgroupSelfFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", cgroupSelfFile, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			if rest == "" {
				return "/", nil
			}
			return rest, nil
		}
	}
	return "", fmt.Errorf("no cgroup v2 entry in %s", cgroupSelfFile)
}

func cgroupFSPath(cgroupPath string) string {
	return cgroupMountPath + path.Clean(cgroupPath)
}

// joinDelegatedServiceCgroup moves this process to the server sibling of
// parent when parent is an absolute cgroup path such as /scion-svc/agents.
// A restart whose shell is still inside that agents cgroup (or any other
// cgroup) otherwise cannot create container children there, and the memory
// and pid limits do not apply. A relative name such as scion.slice is left
// to Podman and is not a move.
func joinDelegatedServiceCgroup(parent string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	server, err := delegatedServiceCgroup(parent)
	if err != nil {
		return err
	}
	current, err := currentCgroupPath()
	if err != nil {
		return err
	}
	if current == server || strings.HasPrefix(current, server+"/") {
		return nil
	}
	domain := path.Dir(server)
	for _, dir := range []string{cgroupFSPath(domain), cgroupFSPath(server), cgroupFSPath(parent)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			if isPermissionErr(err) {
				return fmt.Errorf("%w: create cgroup %s: %w", errCgroupMoveDenied, dir, err)
			}
			return fmt.Errorf("create cgroup %s: %w", dir, err)
		}
	}
	if err := enableDomainControllers(domain); err != nil {
		if isPermissionErr(err) {
			return fmt.Errorf("%w: %w", errCgroupMoveDenied, err)
		}
		return err
	}
	procs := cgroupFSPath(server) + "/cgroup.procs"
	f, err := os.OpenFile(procs, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		if isPermissionErr(err) {
			return fmt.Errorf("%w: place this server in cgroup %s: %w", errCgroupMoveDenied, server, err)
		}
		return fmt.Errorf("place this server in cgroup %s: %w", server, err)
	}
	_, werr := f.Write([]byte(strconv.Itoa(os.Getpid())))
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("place this server in cgroup %s: %w", server, werr)
	}
	if cerr != nil {
		return fmt.Errorf("place this server in cgroup %s: %w", server, cerr)
	}
	return nil
}

// placePodmanCgroup chooses the cgroup podman will parent containers under.
// The preferred parent is the requested delegated path, after this process
// moves to its server sibling. When that move is denied, as it is for a
// restart from a shell in /agent, containers are parented under a child of
// the caller's own cgroup if that cgroup already delegates memory, pids,
// and cpu. Otherwise the caller gets an error and no container is started.
func placePodmanCgroup(requested string) (string, error) {
	if runtime.GOOS != "linux" {
		return requested, nil
	}
	err := joinDelegatedServiceCgroup(requested)
	if err == nil {
		return requested, nil
	}
	if !errors.Is(err, errCgroupMoveDenied) {
		return "", err
	}
	return fallbackCallerCgroup(requested, err)
}

func fallbackCallerCgroup(requested string, moveErr error) (string, error) {
	current, err := currentCgroupPath()
	if err != nil {
		return "", err
	}
	server, _ := delegatedServiceCgroup(requested)
	ctl := cgroupFSPath(current) + "/cgroup.subtree_control"
	existing, readErr := os.ReadFile(ctl)
	if current == "/" || readErr != nil || !cgroupDelegates(existing, "memory", "pids", "cpu") {
		return "", fmt.Errorf("refusing to start containers without a cgroup limit: cannot move this server into %s (%v), and cgroup %s does not delegate memory, pids, and cpu. Start scion from a shell that can write %s, or delegate those controllers to %s", server, moveErr, current, server, current)
	}
	child := path.Clean(current) + "/scion-agents"
	if err := os.MkdirAll(cgroupFSPath(child), 0o755); err != nil {
		return "", fmt.Errorf("refusing to start containers without a cgroup limit: cannot create %s under %s: %w", child, current, err)
	}
	return child, nil
}

func isPermissionErr(err error) bool {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return os.IsPermission(pathErr.Err)
	}
	return os.IsPermission(err)
}

func cgroupDelegates(control []byte, names ...string) bool {
	have := string(control)
	for _, name := range names {
		if !strings.Contains(have, name) {
			return false
		}
	}
	return true
}

func enableDomainControllers(domain string) error {
	ctl := cgroupFSPath(domain) + "/cgroup.subtree_control"
	existing, err := os.ReadFile(ctl)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", ctl, err)
	}
	have := string(existing)
	var missing []string
	for _, controller := range []string{"memory", "pids", "cpu"} {
		if !strings.Contains(have, controller) {
			missing = append(missing, "+"+controller)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// A cgroup control file takes a write of "+controller". Truncating it
	// is not how the kernel enables controllers.
	f, err := os.OpenFile(ctl, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("enable controllers on %s: %w", domain, err)
	}
	_, werr := f.Write([]byte(strings.Join(missing, " ") + "\n"))
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("enable controllers on %s: %w", domain, werr)
	}
	if cerr != nil {
		return fmt.Errorf("enable controllers on %s: %w", domain, cerr)
	}
	return nil
}
