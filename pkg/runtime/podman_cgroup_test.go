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
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestDelegatedServiceCgroup(t *testing.T) {
	got, err := delegatedServiceCgroup("/scion-svc/agents")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/scion-svc/server" {
		t.Fatalf("server cgroup = %q", got)
	}
	if _, err := delegatedServiceCgroup("scion.slice"); err == nil {
		t.Fatal("relative slice was accepted as a delegated path")
	}
	if _, err := delegatedServiceCgroup("/agents"); err == nil {
		t.Fatal("top-level agents cgroup was accepted")
	}
}

func TestJoinDelegatedServiceCgroupMovesOffAgent(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup files are linux")
	}
	root := t.TempDir()
	self := filepath.Join(root, "self.cgroup")
	if err := os.WriteFile(self, []byte("0::/agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mount := filepath.Join(root, "cgroup")
	prevMount, prevSelf := cgroupMountPath, cgroupSelfFile
	cgroupMountPath, cgroupSelfFile = mount, self
	t.Cleanup(func() {
		cgroupMountPath, cgroupSelfFile = prevMount, prevSelf
	})
	domain := filepath.Join(mount, "scion-svc")
	if err := os.MkdirAll(domain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(domain, "cgroup.subtree_control"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := joinDelegatedServiceCgroup("/scion-svc/agents"); err != nil {
		t.Fatal(err)
	}
	procs, err := os.ReadFile(filepath.Join(domain, "server", "cgroup.procs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(procs)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("server cgroup.procs = %q", procs)
	}
	ctl, err := os.ReadFile(filepath.Join(domain, "cgroup.subtree_control"))
	if err != nil {
		t.Fatal(err)
	}
	for _, controller := range []string{"+memory", "+pids", "+cpu"} {
		if !strings.Contains(string(ctl), controller) {
			t.Fatalf("subtree_control %q missing %s", ctl, controller)
		}
	}
	if _, err := os.Stat(filepath.Join(domain, "agents")); err != nil {
		t.Fatal(err)
	}
	// A second start, already in the server cgroup, does not rewrite it.
	if err := os.WriteFile(self, []byte("0::/scion-svc/server\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(domain, "server", "cgroup.procs"), []byte("stay\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := joinDelegatedServiceCgroup("/scion-svc/agents"); err != nil {
		t.Fatal(err)
	}
	procs, err = os.ReadFile(filepath.Join(domain, "server", "cgroup.procs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(procs)) != "stay" {
		t.Fatalf("already-placed server was moved: %q", procs)
	}
}
