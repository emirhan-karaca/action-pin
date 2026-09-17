package resolver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/emirhan-karaca/action-pin/v2/internal/action"
)

// Resolver resolves a GitHub action owner/repo and ref to a 40-character commit SHA.
type Resolver interface {
	Resolve(ctx context.Context, owner, repo, ref string) (string, error)
}

// GitExecutor runs git commands. Abstracted for testing.
type GitExecutor func(ctx context.Context, args ...string) ([]byte, error)

func defaultGitExec(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo")
	return cmd.CombinedOutput()
}

// GitHubResolver resolves action refs using GitHub API with git ls-remote fallback.
type GitHubResolver struct {
	Token      string
	BaseURL    string
	HTTPClient *http.Client
	GitExec    GitExecutor
	cache      sync.Map
}

// Option configures GitHubResolver.
type Option func(*GitHubResolver)

// WithToken sets the GitHub token.
func WithToken(token string) Option {
	return func(r *GitHubResolver) {
		r.Token = token
	}
}

// WithBaseURL sets the base API URL (e.g. for GitHub Enterprise or mock test servers).
func WithBaseURL(url string) Option {
	return func(r *GitHubResolver) {
		r.BaseURL = strings.TrimRight(url, "/")
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(r *GitHubResolver) {
		r.HTTPClient = client
	}
}

// WithGitExec sets a custom git execution function.
func WithGitExec(exec GitExecutor) Option {
	return func(r *GitHubResolver) {
		r.GitExec = exec
	}
}

// New creates a new GitHubResolver.
func New(opts ...Option) *GitHubResolver {
	r := &GitHubResolver{
		BaseURL: "https://api.github.com",
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		GitExec: defaultGitExec,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

type commitResponse struct {
	SHA string `json:"sha"`
}

type gitRefResponse struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

type gitTagResponse struct {
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

// Resolve resolves owner/repo and ref to a commit SHA.
func (r *GitHubResolver) Resolve(ctx context.Context, owner, repo, ref string) (string, error) {
	// If already a full 40-character commit SHA, nothing to resolve
	if action.IsCommitSHA(ref) {
		return ref, nil
	}

	cacheKey := fmt.Sprintf("%s/%s@%s", strings.ToLower(owner), strings.ToLower(repo), ref)
	if cached, ok := r.cache.Load(cacheKey); ok {
		return cached.(string), nil
	}

	// First try GitHub API
	sha, apiErr := r.resolveViaAPI(ctx, owner, repo, ref)
	if apiErr == nil && action.IsCommitSHA(sha) {
		r.cache.Store(cacheKey, sha)
		return sha, nil
	}

	// Fallback to git ls-remote
	gitSha, gitErr := r.resolveViaGit(ctx, owner, repo, ref)
	if gitErr == nil && action.IsCommitSHA(gitSha) {
		r.cache.Store(cacheKey, gitSha)
		return gitSha, nil
	}

	// If both failed, return an informative error
	if apiErr != nil && gitErr != nil {
		return "", fmt.Errorf("failed to resolve %s/%s@%s: API error: %w; Git fallback error: %v", owner, repo, ref, apiErr, gitErr)
	}
	if apiErr != nil {
		return "", fmt.Errorf("failed to resolve %s/%s@%s: %w", owner, repo, ref, apiErr)
	}
	return "", fmt.Errorf("failed to resolve %s/%s@%s: git error: %w", owner, repo, ref, gitErr)
}

func (r *GitHubResolver) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "action-pin")
	if r.Token != "" {
		req.Header.Set("Authorization", "Bearer "+r.Token)
	}
}

func (r *GitHubResolver) resolveViaAPI(ctx context.Context, owner, repo, ref string) (string, error) {
	// 1. Try /repos/{owner}/{repo}/commits/{ref}
	commitURL := fmt.Sprintf("%s/repos/%s/%s/commits/%s", r.BaseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, commitURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create commit request: %w", err)
	}
	r.setHeaders(req)

	resp, err := r.HTTPClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var commit commitResponse
			if err := json.NewDecoder(resp.Body).Decode(&commit); err == nil && action.IsCommitSHA(commit.SHA) {
				return commit.SHA, nil
			}
		}
	}

	// 2. If /commits/{ref} failed (e.g. 422 for annotated tags or 404), try /repos/{owner}/{repo}/git/ref/tags/{ref}
	tagRefURL := fmt.Sprintf("%s/repos/%s/%s/git/ref/tags/%s", r.BaseURL, url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(ref))
	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, tagRefURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create tag ref request: %w", err)
	}
	r.setHeaders(req2)

	resp2, err := r.HTTPClient.Do(req2)
	if err == nil {
		defer resp2.Body.Close()
		if resp2.StatusCode == http.StatusOK {
			var gitRef gitRefResponse
			if err := json.NewDecoder(resp2.Body).Decode(&gitRef); err == nil {
				if gitRef.Object.Type == "commit" && action.IsCommitSHA(gitRef.Object.SHA) {
					return gitRef.Object.SHA, nil
				}
				if gitRef.Object.Type == "tag" && action.IsCommitSHA(gitRef.Object.SHA) {
					return r.peelTag(ctx, owner, repo, gitRef.Object.SHA)
				}
			}
		}
	}

	return "", fmt.Errorf("API failed to resolve %s/%s@%s", owner, repo, ref)
}

func (r *GitHubResolver) peelTag(ctx context.Context, owner, repo, sha string) (string, error) {
	for depth := 0; depth < 10; depth++ {
		tagURL := fmt.Sprintf("%s/repos/%s/%s/git/tags/%s", r.BaseURL, url.PathEscape(owner), url.PathEscape(repo), sha)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, tagURL, nil)
		if err != nil {
			return "", fmt.Errorf("failed to create tag request: %w", err)
		}
		r.setHeaders(req)

		resp, err := r.HTTPClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("failed to fetch tag: %w", err)
		}
		var gitTag gitTagResponse
		err = json.NewDecoder(resp.Body).Decode(&gitTag)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil || !action.IsCommitSHA(gitTag.Object.SHA) {
			return "", fmt.Errorf("invalid tag response for %s", sha)
		}
		switch gitTag.Object.Type {
		case "commit":
			return gitTag.Object.SHA, nil
		case "tag":
			sha = gitTag.Object.SHA
		default:
			return "", fmt.Errorf("tag resolves to non-commit object type %q", gitTag.Object.Type)
		}
	}
	return "", fmt.Errorf("tag nesting exceeds limit")
}

func (r *GitHubResolver) resolveViaGit(ctx context.Context, owner, repo, ref string) (string, error) {
	host := "github.com"
	if r.BaseURL != "" && r.BaseURL != "https://api.github.com" {
		if parsed, err := url.Parse(r.BaseURL); err == nil && parsed.Host != "" {
			host = parsed.Host
		}
	}

	gitURL := fmt.Sprintf("https://%s/%s/%s.git", host, owner, repo)
	if r.Token != "" {
		gitURL = fmt.Sprintf("https://x-access-token:%s@%s/%s/%s.git", r.Token, host, owner, repo)
	}

	// Query both tags (and peeled tags) and heads
	args := []string{
		"ls-remote",
		gitURL,
		fmt.Sprintf("refs/tags/%s", ref),
		fmt.Sprintf("refs/tags/%s^{}", ref),
		fmt.Sprintf("refs/heads/%s", ref),
	}

	out, err := r.GitExec(ctx, args...)
	if err != nil {
		message := fmt.Sprintf("git ls-remote failed: %v (output: %s)", err, strings.TrimSpace(string(out)))
		if r.Token != "" {
			message = strings.ReplaceAll(message, r.Token, "[REDACTED]")
		}
		return "", fmt.Errorf("%s", message)
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	var tagSha string
	var headSha string
	var peeledSha string

	peeledSuffix := fmt.Sprintf("refs/tags/%s^{}", ref)
	tagSuffix := fmt.Sprintf("refs/tags/%s", ref)
	headSuffix := fmt.Sprintf("refs/heads/%s", ref)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		sha := parts[0]
		refName := parts[1]

		if refName == peeledSuffix {
			peeledSha = sha
		} else if refName == tagSuffix {
			tagSha = sha
		} else if refName == headSuffix {
			headSha = sha
		}
	}

	if peeledSha != "" && action.IsCommitSHA(peeledSha) {
		return peeledSha, nil
	}
	if tagSha != "" && action.IsCommitSHA(tagSha) {
		return tagSha, nil
	}
	if headSha != "" && action.IsCommitSHA(headSha) {
		return headSha, nil
	}

	return "", fmt.Errorf("ref %s not found in repository %s/%s", ref, owner, repo)
}
