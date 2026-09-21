// Command fetchspec downloads the OpenAPI specification from Apidog.
//
// The access token is taken from APIDOG_TOKEN and the optional sprint branch
// from APIDOG_BRANCH_ID. Without a branch the main branch is exported.
// Variables missing from the environment are read from the .env file,
// so `make generate` works without exporting anything.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	exportURL      = "https://api.apidog.com/v1/projects/1382426/export-openapi"
	apiVersion     = "2024-03-28"
	requestTimeout = time.Minute
	maxErrorBody   = 512
)

type exportRequest struct {
	Scope        exportScope   `json:"scope"`
	Options      exportOptions `json:"options"`
	OASVersion   string        `json:"oasVersion"`
	ExportFormat string        `json:"exportFormat"`
	BranchID     int64         `json:"branchId,omitempty"`
}

type exportScope struct {
	Type string `json:"type"`
}

type exportOptions struct {
	IncludeApidogExtensionProperties bool `json:"includeApidogExtensionProperties"`
	AddFoldersToTags                 bool `json:"addFoldersToTags"`
}

func main() {
	out := flag.String("o", "openapi.yaml", "file to write the specification to")
	envFile := flag.String("env-file", ".env", "file with APIDOG_TOKEN and APIDOG_BRANCH_ID")
	flag.Parse()

	if err := run(*out, *envFile); err != nil {
		fmt.Fprintln(os.Stderr, "fetchspec:", err)
		os.Exit(1)
	}
}

func run(out, envFile string) error {
	fileEnv, err := readEnvFile(envFile)
	if err != nil {
		return err
	}
	lookup := func(key string) string {
		if value, ok := os.LookupEnv(key); ok {
			return value
		}
		return fileEnv[key]
	}

	token := lookup("APIDOG_TOKEN")
	if token == "" {
		return errors.New("APIDOG_TOKEN is not set: create a personal token in Apidog " +
			"(Account Settings → API Access Token) and put it into .env, " +
			"see «Как менять контракт» in README.md")
	}

	req := exportRequest{
		Scope:        exportScope{Type: "ALL"},
		OASVersion:   "3.1",
		ExportFormat: "YAML",
	}
	branch := "main"
	if raw := lookup("APIDOG_BRANCH_ID"); raw != "" {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id <= 0 {
			return fmt.Errorf("APIDOG_BRANCH_ID must be a positive number, got %q", raw)
		}
		req.BranchID = id
		branch = "sprint branch " + raw
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	spec, err := export(ctx, token, req)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, spec, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}

	fmt.Fprintf(os.Stderr, "fetchspec: exported %s to %s\n", branch, out)
	return nil
}

func export(ctx context.Context, token string, body exportRequest) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exportURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Apidog-Api-Version", apiVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request Apidog: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("apidog rejected the token (%s): check APIDOG_TOKEN", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("apidog responded %s: %s", resp.Status, truncate(data))
	case !bytes.HasPrefix(data, []byte("openapi:")):
		return nil, fmt.Errorf("apidog returned something other than an OpenAPI spec: %s", truncate(data))
	}
	return data, nil
}

// readEnvFile reads KEY=VALUE pairs; a missing file is not an error.
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	env := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		env[strings.TrimSpace(key)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return env, nil
}

func truncate(data []byte) string {
	if len(data) > maxErrorBody {
		data = data[:maxErrorBody]
	}
	return strings.TrimSpace(string(data))
}
