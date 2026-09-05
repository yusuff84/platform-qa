package spec

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// fetchClient downloads specifications: 30s overall timeout, TLS
// certificate verification disabled (self-signed internal stands).
var fetchClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // internal stands use self-signed certs
	},
}

// Fetch downloads or reads a specification document from a URL or local file path.
func Fetch(ctx context.Context, target string) ([]byte, error) {
	return FetchWithAuth(ctx, target, "")
}

// FetchWithAuth downloads or reads a specification with an optional Bearer token.
func FetchWithAuth(ctx context.Context, target, token string) ([]byte, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("адрес или путь к спецификации обязателен")
	}

	if strings.HasPrefix(target, "file://") {
		target = strings.TrimPrefix(target, "file://")
	}

	// 1. If it does not start with http(s), treat as local filesystem path
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		data, err := os.ReadFile(target)
		if err != nil {
			return nil, fmt.Errorf("чтение локального файла спецификации %q: %w", target, err)
		}
		if len(data) > maxBodyBytes {
			return nil, fmt.Errorf("спецификация превышает лимит %d байт", maxBodyBytes)
		}
		return data, nil
	}

	// 2. HTTP/HTTPS download
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("некорректный url %q: %w", target, err)
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}

	resp, err := fetchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("не удалось скачать спецификацию: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("при скачивании спецификации получен статус %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("не удалось прочитать спецификацию: %w", err)
	}
	if len(data) > maxBodyBytes {
		return nil, fmt.Errorf("спецификация превышает лимит %d байт", maxBodyBytes)
	}
	return data, nil
}
