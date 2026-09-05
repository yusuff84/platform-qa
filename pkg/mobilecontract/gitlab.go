package mobilecontract

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var gitlabHTTPClient = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // support self-hosted internal GitLab instances
	},
}

// GitLabBranch represents a branch returned from the GitLab API.
type GitLabBranch struct {
	Name    string `json:"name"`
	Default bool   `json:"default"`
}

// ParseGitLabRepoURL decomposes full repo URLs into GitLab server URL and project path.
// e.g. "https://lokaligitlabru.ru/app/locali-director-flutter.git" -> "https://lokaligitlabru.ru", "app/locali-director-flutter"
// e.g. "git@gitlab.com:group/project.git" -> "https://gitlab.com", "group/project"
// e.g. "app/locali-director-flutter" -> "https://gitlab.com", "app/locali-director-flutter"
func ParseGitLabRepoURL(rawURL, defaultServer string) (serverURL, projectPath string, err error) {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return "", "", fmt.Errorf("URL репозитория не указан")
	}

	// Handle SSH format: git@host:group/project.git
	if strings.HasPrefix(raw, "git@") {
		parts := strings.Split(strings.TrimPrefix(raw, "git@"), ":")
		if len(parts) == 2 {
			serverURL = "https://" + parts[0]
			projectPath = strings.TrimSuffix(parts[1], ".git")
			projectPath = strings.Trim(projectPath, "/")
			return serverURL, projectPath, nil
		}
	}

	// Handle HTTP(S) format
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		u, parseErr := url.Parse(raw)
		if parseErr != nil {
			return "", "", fmt.Errorf("некорректный URL %q: %w", raw, parseErr)
		}
		serverURL = fmt.Sprintf("%s://%s", u.Scheme, u.Host)
		path := strings.TrimSuffix(u.Path, ".git")
		path = strings.Trim(path, "/")
		if path == "" {
			return "", "", fmt.Errorf("URL не содержит пути к проекту")
		}
		return serverURL, path, nil
	}

	// Fallback to relative project path (e.g. "group/project")
	server := strings.TrimSpace(defaultServer)
	if server == "" {
		server = "https://gitlab.com"
	}
	server = strings.TrimRight(server, "/")
	projectPath = strings.TrimSuffix(raw, ".git")
	projectPath = strings.Trim(projectPath, "/")
	return server, projectPath, nil
}

// FetchGitLabBranches queries the GitLab API for repository branches.
func FetchGitLabBranches(ctx context.Context, serverURL, projectPath, token string) ([]GitLabBranch, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	encodedProject := url.PathEscape(projectPath)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/branches?per_page=100", serverURL, encodedProject)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания запроса к GitLab: %w", err)
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("PRIVATE-TOKEN", strings.TrimSpace(token))
	}

	resp, err := gitlabHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("не удалось подключиться к GitLab (%s): %w", serverURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("GitLab API вернул HTTP %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var branches []GitLabBranch
	if err := json.NewDecoder(resp.Body).Decode(&branches); err != nil {
		return nil, fmt.Errorf("ошибка разбора списка веток: %w", err)
	}

	return branches, nil
}

// DownloadAndScanGitLabBranch pulls the branch archive from GitLab, unpacks relevant DTO files (.dart, .swift, .kt),
// and parses them into MobileModel slice.
func DownloadAndScanGitLabBranch(ctx context.Context, serverURL, projectPath, branch, token, targetDir string) ([]MobileModel, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	encodedProject := url.PathEscape(projectPath)
	encodedBranch := url.QueryEscape(branch)

	// GitLab archive API endpoint (tar.gz or zip)
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/archive.tar.gz?ref=%s", serverURL, encodedProject, encodedBranch)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания запроса архива: %w", err)
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("PRIVATE-TOKEN", strings.TrimSpace(token))
	}

	resp, err := gitlabHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("не удалось скачать архив ветки %s: %w", branch, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("скачивание ветки %s отклонено (HTTP %d): %s", branch, resp.StatusCode, string(bodyBytes))
	}

	// Prepare clean extraction directory
	extractDir := filepath.Join(targetDir, "gitlab_repos", projectPath, branch)
	_ = os.RemoveAll(extractDir)
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return nil, fmt.Errorf("создание директории %s: %w", extractDir, err)
	}

	// Unpack tar.gz (filtering for mobile source files only to be fast and light)
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения архива: %w", err)
	}

	if err := unpackTarGzMobileFiles(data, extractDir); err != nil {
		// Try zip fallback if server sent zip
		if zerr := unpackZipMobileFiles(data, extractDir); zerr != nil {
			return nil, fmt.Errorf("ошибка распаковки архива GitLab: %w (tar.gz: %v)", zerr, err)
		}
	}

	return ScanDirectory(extractDir)
}

func unpackTarGzMobileFiles(data []byte, destDir string) error {
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Security: prevent ZipSlip / directory traversal
		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") || strings.HasPrefix(cleanName, "/") {
			continue
		}

		// Keep only .dart, .swift, .kt files
		ext := strings.ToLower(filepath.Ext(cleanName))
		if ext != ".dart" && ext != ".swift" && ext != ".kt" {
			continue
		}

		targetPath := filepath.Join(destDir, cleanName)
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		if _, err := io.Copy(outFile, io.LimitReader(tr, 10<<20)); err != nil {
			outFile.Close()
			return err
		}
		outFile.Close()
	}
	return nil
}

func unpackZipMobileFiles(data []byte, destDir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}

	for _, file := range zr.File {
		cleanName := filepath.Clean(file.Name)
		if strings.HasPrefix(cleanName, "..") || strings.HasPrefix(cleanName, "/") {
			continue
		}

		ext := strings.ToLower(filepath.Ext(cleanName))
		if ext != ".dart" && ext != ".swift" && ext != ".kt" {
			continue
		}

		targetPath := filepath.Join(destDir, cleanName)
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}

		rc, err := file.Open()
		if err != nil {
			return err
		}
		outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(outFile, io.LimitReader(rc, 10<<20))
		outFile.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
