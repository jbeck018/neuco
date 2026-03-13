package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	mw "github.com/neuco-ai/neuco/internal/api/middleware"
)

// userRepo is the simplified repo shape returned by ListUserRepos.
type userRepo struct {
	FullName    string    `json:"full_name"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Language    string    `json:"language,omitempty"`
	Private     bool      `json:"private"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// githubRepoResponse mirrors the subset of fields returned by the GitHub repos API.
type githubRepoResponse struct {
	FullName    string    `json:"full_name"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Language    string    `json:"language"`
	Private     bool      `json:"private"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ListUserRepos handles GET /api/v1/auth/github/repos?q=search_term.
//
// Uses the authenticated user's stored GitHub OAuth token to list their
// repositories. When the optional "q" query parameter is provided, the
// results are filtered to repos whose name contains the search term
// (case-insensitive). Up to 30 repos are returned, sorted by most recently
// updated.
func ListUserRepos(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := mw.UserIDFromCtx(r.Context())

		token, err := d.Store.GetUserGitHubToken(r.Context(), userID)
		if err != nil {
			slog.Error("failed to fetch github token", "user_id", userID, "error", err)
			respondErr(w, r, http.StatusInternalServerError, "failed to fetch github token")
			return
		}
		if token == "" {
			respondErr(w, r, http.StatusUnauthorized, "no github token stored for user; please re-authenticate")
			return
		}

		query := strings.TrimSpace(r.URL.Query().Get("q"))

		var repos []githubRepoResponse
		var fetchErr error

		if query != "" {
			repos, fetchErr = searchGitHubRepos(r.Context(), token, query)
		} else {
			repos, fetchErr = listGitHubUserRepos(r.Context(), token)
		}

		if fetchErr != nil {
			slog.Error("github repos fetch failed", "user_id", userID, "error", fetchErr)
			respondErr(w, r, http.StatusBadGateway, "failed to fetch repositories from github")
			return
		}

		result := make([]userRepo, 0, len(repos))
		for _, repo := range repos {
			result = append(result, userRepo(repo))
		}

		respondOK(w, r, result)
	}
}

// listGitHubUserRepos fetches up to 30 repos for the authenticated user via
// GET /user/repos, sorted by most recently updated.
func listGitHubUserRepos(ctx context.Context, token string) ([]githubRepoResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/user/repos?per_page=30&sort=updated&type=all", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API returned status %d", resp.StatusCode)
	}

	var repos []githubRepoResponse
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return repos, nil
}

// searchGitHubRepos searches for repositories matching the query that are
// owned by the authenticated user via GET /search/repositories.
func searchGitHubRepos(ctx context.Context, token, query string) ([]githubRepoResponse, error) {
	// Fetch the authenticated user's login first so we can scope the search.
	login, err := fetchGitHubLogin(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("fetch github login: %w", err)
	}

	searchURL := fmt.Sprintf(
		"https://api.github.com/search/repositories?q=%s+user:%s&per_page=30&sort=updated",
		query, login,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github search API returned status %d", resp.StatusCode)
	}

	var searchResult struct {
		Items []githubRepoResponse `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&searchResult); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return searchResult.Items, nil
}

// fetchGitHubLogin returns the GitHub login for the token owner.
// The result from GET /user is reused from auth.go's fetchGitHubUser; this
// separate helper avoids the import cycle while remaining a small, focused
// HTTP call.
func fetchGitHubLogin(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github API returned status %d", resp.StatusCode)
	}

	var user struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return "", err
	}
	return user.Login, nil
}
