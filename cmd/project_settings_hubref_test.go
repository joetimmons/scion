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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectSettingsSet_HubProjectIDUsesAuthenticatedClient(t *testing.T) {
	hermeticCLIEnv(t)
	const (
		projectID = "2643552f-7fb7-4f81-a790-31a5c4280330"
		token     = "scion_dev_project_settings_test"
	)
	t.Setenv("SCION_DEV_TOKEN", token)
	t.Setenv("SCION_HUB_TOKEN", "")
	t.Setenv("SCION_AUTH_TOKEN", "")
	t.Setenv("SCION_DEV_TOKEN_FILE", "")

	var body map[string]interface{}
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/"+projectID:
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id": projectID, "name": "oase", "slug": "oase",
			})
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/projects/"+projectID+"/settings":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			_ = json.NewEncoder(w).Encode(map[string]string{
				"defaultAgentRole": "baseline",
				"maxAgentRole":     "full",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	home := os.Getenv("HOME")
	t.Chdir(home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".scion"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".scion", "settings.yaml"), []byte("project_id: local\n"), 0o644))

	origProject, origHub := projectPath, hubEndpoint
	t.Cleanup(func() { projectPath, hubEndpoint = origProject, origHub })
	projectPath = projectID
	hubEndpoint = srv.URL

	cmd := projectSettingsSetCmd
	cmd.SetContext(context.Background())
	stdout, _ := captureStdIO(t, func() {
		require.NoError(t, cmd.RunE(cmd, []string{
			"defaultAgentRole", "baseline", "maxAgentRole", "full",
		}))
	})
	assert.Contains(t, stdout, "Saved.")
	assert.Equal(t, "Bearer "+token, auth)
	assert.Equal(t, "baseline", body["defaultAgentRole"])
	assert.Equal(t, "full", body["maxAgentRole"])
	_, namedProject := body["projectId"]
	assert.False(t, namedProject, "the settings body must not retarget the project: %v", body)
}
