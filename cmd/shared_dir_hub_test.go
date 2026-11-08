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

package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSharedDirUsesHub_UUIDNotLocalDirectory(t *testing.T) {
	hermeticCLIEnv(t)
	orig := projectPath
	t.Cleanup(func() { projectPath = orig })

	projectPath = "2643552f-7fb7-4f81-a790-31a5c4280330"
	assert.True(t, sharedDirUsesHub())

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "oase"), 0o755))
	t.Chdir(dir)
	projectPath = "oase"
	assert.False(t, sharedDirUsesHub(), "a relative directory is the local settings path, not the hub slug")
}

func TestSharedDirListAndCreate_HubProjectUsesDevAuthClient(t *testing.T) {
	hermeticCLIEnv(t)
	const (
		projectID = "2643552f-7fb7-4f81-a790-31a5c4280330"
		token     = "scion_dev_shared_dir_test"
	)
	t.Setenv("SCION_DEV_TOKEN", token)
	t.Setenv("SCION_HUB_TOKEN", "")
	t.Setenv("SCION_AUTH_TOKEN", "")
	t.Setenv("SCION_DEV_TOKEN_FILE", "")

	var mu sync.Mutex
	var auths []string
	var created map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/"+projectID:
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id": projectID, "name": "oase", "slug": "oase",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/"+projectID+"/shared-dirs":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"sharedDirs": []map[string]string{{
					"name":   "tools",
					"target": "/scion-volumes/tools",
				}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects/"+projectID+"/shared-dirs":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&created))
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "scratchpad"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	home := os.Getenv("HOME")
	t.Chdir(home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".scion"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".scion", "settings.yaml"), []byte("project_id: local\n"), 0o644))

	origProject, origHub, origYes := projectPath, hubEndpoint, autoConfirm
	t.Cleanup(func() {
		projectPath, hubEndpoint, autoConfirm = origProject, origHub, origYes
	})
	projectPath = projectID
	hubEndpoint = srv.URL
	autoConfirm = true

	stdout, _ := captureStdIO(t, func() {
		require.NoError(t, sharedDirListCmd.RunE(sharedDirListCmd, nil))
	})
	assert.Contains(t, stdout, "tools")
	assert.Contains(t, stdout, "/scion-volumes/tools")
	assert.Contains(t, stdout, filepath.Join(home, ".scion", "project-configs", "oase__2643552f", "shared-dirs", "tools"))

	stdout, _ = captureStdIO(t, func() {
		require.NoError(t, sharedDirCreateCmd.RunE(sharedDirCreateCmd, []string{"scratchpad"}))
	})
	assert.Contains(t, stdout, projectID)
	assert.Contains(t, stdout, "/scion-volumes/scratchpad")
	assert.Equal(t, "scratchpad", created["name"])
	_, sentTarget := created["target"]
	assert.False(t, sentTarget)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, auths)
	for _, auth := range auths {
		assert.Equal(t, "Bearer "+token, auth)
	}
}

func TestSharedDirList_LocalDirectoryDoesNotCallHub(t *testing.T) {
	hermeticCLIEnv(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	projectDir := filepath.Join(t.TempDir(), ".scion")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, "settings.yaml"), []byte("version: 1\n"), 0o644))

	origProject, origHub := projectPath, hubEndpoint
	t.Cleanup(func() { projectPath, hubEndpoint = origProject, origHub })
	projectPath = projectDir
	hubEndpoint = srv.URL

	stdout, _ := captureStdIO(t, func() {
		require.NoError(t, sharedDirListCmd.RunE(sharedDirListCmd, nil))
	})
	assert.True(t, strings.Contains(stdout, "No shared directories") || stdout == "")
	assert.Zero(t, calls)
}
