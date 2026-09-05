package mobilecontract_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"locali-e2e-engine/pkg/mobilecontract"
)

func TestParseGitLabRepoURL(t *testing.T) {
	tests := []struct {
		input       string
		defaultSrv  string
		wantServer  string
		wantProject string
		wantErr     bool
	}{
		{
			input:       "https://lokaligitlabru.ru/app/locali-director-flutter.git",
			wantServer:  "https://lokaligitlabru.ru",
			wantProject: "app/locali-director-flutter",
		},
		{
			input:       "https://gitlab.com/group/subgroup/project",
			wantServer:  "https://gitlab.com",
			wantProject: "group/subgroup/project",
		},
		{
			input:       "git@lokaligitlabru.ru:app/locali-director-flutter.git",
			wantServer:  "https://lokaligitlabru.ru",
			wantProject: "app/locali-director-flutter",
		},
		{
			input:       "app/locali-director-flutter",
			defaultSrv:  "https://custom-gitlab.io",
			wantServer:  "https://custom-gitlab.io",
			wantProject: "app/locali-director-flutter",
		},
		{
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			srv, proj, err := mobilecontract.ParseGitLabRepoURL(tt.input, tt.defaultSrv)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantServer, srv)
				assert.Equal(t, tt.wantProject, proj)
			}
		})
	}
}

func TestFetchGitLabBranches_Mock(t *testing.T) {
	mockBranches := []mobilecontract.GitLabBranch{
		{Name: "main", Default: true},
		{Name: "dev", Default: false},
		{Name: "feat/order-screen", Default: false},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "test-gl-token", r.Header.Get("PRIVATE-TOKEN"))
		assert.Contains(t, r.RequestURI, "app%2Fproject")
		_ = json.NewEncoder(w).Encode(mockBranches)
	}))
	defer srv.Close()

	branches, err := mobilecontract.FetchGitLabBranches(context.Background(), srv.URL, "app/project", "test-gl-token")
	require.NoError(t, err)
	require.Len(t, branches, 3)
	assert.Equal(t, "main", branches[0].Name)
	assert.True(t, branches[0].Default)
	assert.Equal(t, "dev", branches[1].Name)
}

func TestDownloadAndScanGitLabBranch_Mock(t *testing.T) {
	// Create mock tar.gz containing a dart model
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	dartContent := []byte(`
class UserDTO {
  final String id;
  UserDTO({required this.id});
}
`)
	hdr := &tar.Header{
		Name: "project-main/lib/user.dart",
		Mode: 0o644,
		Size: int64(len(dartContent)),
	}
	err := tw.WriteHeader(hdr)
	require.NoError(t, err)
	_, err = tw.Write(dartContent)
	require.NoError(t, err)
	_ = tw.Close()
	_ = gzw.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-gzip")
		_, _ = w.Write(buf.Bytes())
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	models, err := mobilecontract.DownloadAndScanGitLabBranch(context.Background(), srv.URL, "my/app", "main", "tok", tmpDir)
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "UserDTO", models[0].Name)
	assert.Equal(t, mobilecontract.PlatformFlutter, models[0].Platform)

	// Verify file was extracted into temp directory
	extractedFile := filepath.Join(tmpDir, "gitlab_repos", "my/app", "main", "project-main/lib/user.dart")
	assert.FileExists(t, extractedFile)
}
