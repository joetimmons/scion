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
	"fmt"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
)

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
			return fmt.Errorf("create cgroup %s: %w", dir, err)
		}
	}
	if err := enableDomainControllers(domain); err != nil {
		return err
	}
	procs := cgroupFSPath(server) + "/cgroup.procs"
	f, err := os.OpenFile(procs, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
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
