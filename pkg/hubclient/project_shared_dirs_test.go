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

package hubclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GoogleCloudPlatform/scion/pkg/api"
)

func TestProjectSharedDirs_ListCreateDelete(t *testing.T) {
	const projectID = "2643552f-7fb7-4f81-a790-31a5c4280330"
	var posted map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scion_dev_test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/"+projectID+"/shared-dirs":
			_, _ = w.Write([]byte(`{"sharedDirs":[{"name":"tools","target":"/scion-volumes/tools"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/projects/"+projectID+"/shared-dirs":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Errorf("decode body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"tools"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/projects/"+projectID+"/shared-dirs/tools":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, WithBearerToken("scion_dev_test"))
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := client.Projects().ListSharedDirs(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0].Name != "tools" || dirs[0].Target != "/scion-volumes/tools" {
		t.Fatalf("list = %+v", dirs)
	}
	created, err := client.Projects().CreateSharedDir(context.Background(), projectID, api.SharedDir{Name: "tools"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "tools" {
		t.Fatalf("created = %+v", created)
	}
	if _, ok := posted["target"]; ok {
		t.Fatalf("create body included target: %v", posted)
	}
	if posted["name"] != "tools" {
		t.Fatalf("create body = %v", posted)
	}
	if err := client.Projects().DeleteSharedDir(context.Background(), projectID, "tools"); err != nil {
		t.Fatal(err)
	}
}
