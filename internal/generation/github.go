// Package generation provides GitHub App integration and codebase indexing
// services used by the code generation pipeline.
package generation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v60/github"
	"golang.org/x/oauth2"
)

// GitHubService provides authenticated GitHub API access via a GitHub App.
// It generates short-lived installation tokens by signing JWTs with the
// App's RSA private key and exchanging them for installation access tokens.
type GitHubService struct {
	appID          string
	privateKeyPath string
	privateKey     []byte
	httpClient     *http.Client
}

// NewGitHubService loads the RSA private key from privateKeyPath, validates it
// can be parsed as a PEM-encoded PKCS#1 or PKCS#8 RSA key, and returns a
// service ready to issue installation tokens. The httpClient controls timeouts
// for all GitHub API calls; pass nil to use a 30-second default.
func NewGitHubService(appID, privateKeyPath string) (*GitHubService, error) {
	if appID == "" {
		return nil, fmt.Errorf("generation.NewGitHubService: appID must not be empty")
	}
	if privateKeyPath == "" {
		return nil, fmt.Errorf("generation.NewGitHubService: privateKeyPath must not be empty")
	}

	keyBytes, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("generation.NewGitHubService: read private key: %w", err)
	}

	// Validate that the key material can be parsed now, not at first use.
	if _, err := jwt.ParseRSAPrivateKeyFromPEM(keyBytes); err != nil {
		return nil, fmt.Errorf("generation.NewGitHubService: parse RSA private key: %w", err)
	}

	return &GitHubService{
		appID:          appID,
		privateKeyPath: privateKeyPath,
		privateKey:     keyBytes,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// generateAppJWT creates a signed RS256 JWT for authenticating as the GitHub
// App itself (not as an installation). The token is valid for 10 minutes.
// GitHub requires iat to be at least 60 seconds in the past to account for
// clock skew.
func (s *GitHubService) generateAppJWT() (string, error) {
	rsaKey, err := jwt.ParseRSAPrivateKeyFromPEM(s.privateKey)
	if err != nil {
		return "", fmt.Errorf("generateAppJWT: parse RSA key: %w", err)
	}

	now := time.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    s.appID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(600 * time.Second)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(rsaKey)
	if err != nil {
		return "", fmt.Errorf("generateAppJWT: sign token: %w", err)
	}
	return signed, nil
}

// GetInstallationToken exchanges an App JWT for an installation access token
// via POST /app/installations/{installation_id}/access_tokens. The token is
// scoped to the repositories accessible to the installation.
func (s *GitHubService) GetInstallationToken(ctx context.Context, installationID int64) (string, error) {
	appJWT, err := s.generateAppJWT()
	if err != nil {
		return "", fmt.Errorf("GetInstallationToken: generate JWT: %w", err)
	}

	url := fmt.Sprintf("https://api.github.com/app/installations/%d/access_tokens", installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", fmt.Errorf("GetInstallationToken: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("GetInstallationToken: http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GetInstallationToken: unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("GetInstallationToken: decode response: %w", err)
	}
	if result.Token == "" {
		return "", fmt.Errorf("GetInstallationToken: empty token in response")
	}
	return result.Token, nil
}

// GetInstallationClient returns an authenticated *github.Client bound to the
// given installation. The client uses a short-lived installation access token
// that is valid for one hour.
func (s *GitHubService) GetInstallationClient(ctx context.Context, installationID int64) (*github.Client, error) {
	token, err := s.GetInstallationToken(ctx, installationID)
	if err != nil {
		return nil, fmt.Errorf("GetInstallationClient: %w", err)
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	client := github.NewClient(tc)
	return client, nil
}

// ListRepoContents returns all file entries under path in the repository,
// recursively expanding sub-directories. Directory entries are not returned —
// only file RepositoryContent records are returned.
func (s *GitHubService) ListRepoContents(
	ctx context.Context,
	client *github.Client,
	owner, repo, path string,
) ([]*github.RepositoryContent, error) {
	_, dirContents, _, err := client.Repositories.GetContents(ctx, owner, repo, path, nil)
	if err != nil {
		return nil, fmt.Errorf("ListRepoContents %s: %w", path, err)
	}

	var files []*github.RepositoryContent
	for _, item := range dirContents {
		switch item.GetType() {
		case "file":
			files = append(files, item)
		case "dir":
			sub, err := s.ListRepoContents(ctx, client, owner, repo, item.GetPath())
			if err != nil {
				// Non-fatal: log and continue so a single unreadable dir does not
				// abort the entire walk.
				continue
			}
			files = append(files, sub...)
		}
	}
	return files, nil
}

// GetFileContent fetches the decoded text content of a single file at path
// in the repository. When ref is empty, the default branch is used.
func (s *GitHubService) GetFileContent(
	ctx context.Context,
	client *github.Client,
	owner, repo, path, ref string,
) (string, error) {
	opts := &github.RepositoryContentGetOptions{}
	if ref != "" {
		opts.Ref = ref
	}

	fileContent, _, _, err := client.Repositories.GetContents(ctx, owner, repo, path, opts)
	if err != nil {
		return "", fmt.Errorf("GetFileContent %s: %w", path, err)
	}
	if fileContent == nil {
		return "", fmt.Errorf("GetFileContent %s: path is a directory, not a file", path)
	}

	// go-github returns content as base64. Use the SDK helper to decode.
	content, err := fileContent.GetContent()
	if err != nil {
		return "", fmt.Errorf("GetFileContent %s: decode content: %w", path, err)
	}
	return content, nil
}

// CreateBranch creates newBranch from the HEAD SHA of baseBranch in the given
// repository. It is idempotent: if the branch already exists, it returns nil.
func (s *GitHubService) CreateBranch(
	ctx context.Context,
	client *github.Client,
	owner, repo, baseBranch, newBranch string,
) error {
	// Resolve the SHA of baseBranch.
	baseRef, _, err := client.Git.GetRef(ctx, owner, repo, "refs/heads/"+baseBranch)
	if err != nil {
		return fmt.Errorf("CreateBranch: get base ref %s: %w", baseBranch, err)
	}

	newRef := &github.Reference{
		Ref: github.String("refs/heads/" + newBranch),
		Object: &github.GitObject{
			SHA: baseRef.Object.SHA,
		},
	}
	_, _, err = client.Git.CreateRef(ctx, owner, repo, newRef)
	if err != nil {
		// 422 means the ref already exists — treat as success.
		if strings.Contains(err.Error(), "422") {
			return nil
		}
		return fmt.Errorf("CreateBranch: create ref %s: %w", newBranch, err)
	}
	return nil
}

// CommitFiles commits all files in the files map (path -> content) to branch
// as a single commit with the given message. It uses the Git Data API:
//
//  1. Creates a blob for each file.
//  2. Gets the current tree SHA of the branch HEAD.
//  3. Creates a new tree with all blobs.
//  4. Creates a commit that points to the new tree.
//  5. Advances the branch ref to the new commit.
func (s *GitHubService) CommitFiles(
	ctx context.Context,
	client *github.Client,
	owner, repo, branch, message string,
	files map[string]string,
) error {
	// Step 1: Resolve the current HEAD commit SHA for the branch.
	ref, _, err := client.Git.GetRef(ctx, owner, repo, "refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("CommitFiles: get branch ref: %w", err)
	}
	headSHA := ref.Object.GetSHA()

	// Step 2: Get the tree SHA at HEAD.
	headCommit, _, err := client.Git.GetCommit(ctx, owner, repo, headSHA)
	if err != nil {
		return fmt.Errorf("CommitFiles: get HEAD commit: %w", err)
	}
	baseTreeSHA := headCommit.Tree.GetSHA()

	// Step 3: Create a blob for each file and assemble tree entries.
	entries := make([]*github.TreeEntry, 0, len(files))
	for path, content := range files {
		encoded := base64.StdEncoding.EncodeToString([]byte(content))
		blob, _, err := client.Git.CreateBlob(ctx, owner, repo, &github.Blob{
			Content:  github.String(encoded),
			Encoding: github.String("base64"),
		})
		if err != nil {
			return fmt.Errorf("CommitFiles: create blob for %s: %w", path, err)
		}
		entries = append(entries, &github.TreeEntry{
			Path: github.String(path),
			Mode: github.String("100644"), // regular file
			Type: github.String("blob"),
			SHA:  blob.SHA,
		})
	}

	// Step 4: Create a new tree based on the current HEAD tree.
	newTree, _, err := client.Git.CreateTree(ctx, owner, repo, baseTreeSHA, entries)
	if err != nil {
		return fmt.Errorf("CommitFiles: create tree: %w", err)
	}

	// Step 5: Create the commit.
	newCommit, _, err := client.Git.CreateCommit(ctx, owner, repo, &github.Commit{
		Message: github.String(message),
		Tree:    &github.Tree{SHA: newTree.SHA},
		Parents: []*github.Commit{{SHA: github.String(headSHA)}},
	}, &github.CreateCommitOptions{})
	if err != nil {
		return fmt.Errorf("CommitFiles: create commit: %w", err)
	}

	// Step 6: Advance the branch ref.
	_, _, err = client.Git.UpdateRef(ctx, owner, repo, &github.Reference{
		Ref:    github.String("refs/heads/" + branch),
		Object: &github.GitObject{SHA: newCommit.SHA},
	}, false)
	if err != nil {
		return fmt.Errorf("CommitFiles: update ref: %w", err)
	}
	return nil
}

// CreatePullRequest opens a draft pull request from head into base and returns
// the created PR. head and base are branch names (not refs).
func (s *GitHubService) CreatePullRequest(
	ctx context.Context,
	client *github.Client,
	owner, repo, title, body, head, base string,
) (*github.PullRequest, error) {
	draft := true
	pr, _, err := client.PullRequests.Create(ctx, owner, repo, &github.NewPullRequest{
		Title: github.String(title),
		Body:  github.String(body),
		Head:  github.String(head),
		Base:  github.String(base),
		Draft: &draft,
	})
	if err != nil {
		return nil, fmt.Errorf("CreatePullRequest: %w", err)
	}
	return pr, nil
}

