package resolver_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/emirhan-karaca/action-pin/v2/internal/resolver"
)

func TestResolve_RefURLCharacters(t *testing.T) {
	for _, ref := range []string{"release/v1", "release#1", "release%23v1"} {
		t.Run(ref, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.URL.Path != "/repos/actions/checkout/commits/"+ref || req.URL.RawQuery != "" {
					t.Errorf("unexpected request URL: %s", req.URL)
				}
				fmt.Fprintf(w, `{"sha": %q}`, dummySha1)
			}))
			defer server.Close()
			r := resolver.New(resolver.WithBaseURL(server.URL))
			sha, err := r.Resolve(context.Background(), "actions", "checkout", ref)
			if err != nil || sha != dummySha1 {
				t.Fatalf("got %q, %v", sha, err)
			}
		})
	}
}

func TestResolve_GitErrorsRedactToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "unavailable", http.StatusForbidden)
	}))
	defer server.Close()
	const token = "test-secret-token"
	r := resolver.New(resolver.WithBaseURL(server.URL), resolver.WithToken(token),
		resolver.WithGitExec(func(ctx context.Context, args ...string) ([]byte, error) {
			return []byte("fatal: unable to access " + args[1]), fmt.Errorf("authentication failed for %s", token)
		}))
	_, err := r.Resolve(context.Background(), "actions", "checkout", "v4")
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("expected redacted error, got %v", err)
	}
}

const (
	dummySha1 = "b4ffde65f46336ab88eb53be808477a3936bae11"
	dummySha2 = "50fbc622fc4ef5163becd7fab6573eac35f8462e"
	tagSha    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestResolve_AlreadySHA(t *testing.T) {
	r := resolver.New()
	sha, err := r.Resolve(context.Background(), "actions", "checkout", dummySha1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != dummySha1 {
		t.Errorf("got %s, want %s", sha, dummySha1)
	}
}

func TestResolve_ViaAPI(t *testing.T) {
	var apiCalls int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt32(&apiCalls, 1)

		if req.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %s", req.Header.Get("Authorization"))
		}
		if req.URL.Path != "/repos/actions/checkout/commits/v4" {
			t.Errorf("unexpected URL path: %s", req.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"sha": "%s"}`, dummySha1)
	}))
	defer server.Close()

	r := resolver.New(
		resolver.WithBaseURL(server.URL),
		resolver.WithToken("test-token"),
	)

	ctx := context.Background()
	sha, err := r.Resolve(ctx, "actions", "checkout", "v4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != dummySha1 {
		t.Errorf("got %s, want %s", sha, dummySha1)
	}

	// Test caching: second call should not increment apiCalls
	sha2, err := r.Resolve(ctx, "actions", "checkout", "v4")
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if sha2 != dummySha1 {
		t.Errorf("got %s, want %s", sha2, dummySha1)
	}
	if atomic.LoadInt32(&apiCalls) != 1 {
		t.Errorf("expected 1 API call due to cache, got %d", atomic.LoadInt32(&apiCalls))
	}
}

func TestResolve_FallbackToGit_PeeledTag(t *testing.T) {
	// API fails with 403 Rate Limit
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, `{"message":"API rate limit exceeded"}`, http.StatusForbidden)
	}))
	defer server.Close()

	var gitCalled bool
	mockGit := func(ctx context.Context, args ...string) ([]byte, error) {
		gitCalled = true
		// Return tag and peeled tag lines
		out := fmt.Sprintf("%s\trefs/tags/v1\n%s\trefs/tags/v1^{}\n", tagSha, dummySha2)
		return []byte(out), nil
	}

	r := resolver.New(
		resolver.WithBaseURL(server.URL),
		resolver.WithGitExec(mockGit),
	)

	sha, err := r.Resolve(context.Background(), "actions", "checkout", "v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gitCalled {
		t.Errorf("expected git fallback to be called")
	}
	// Must pick the peeled SHA (dummySha2), not the tag object SHA (tagSha)
	if sha != dummySha2 {
		t.Errorf("got %s, want peeled sha %s", sha, dummySha2)
	}
}

func TestResolve_FallbackToGit_BranchHead(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer server.Close()

	mockGit := func(ctx context.Context, args ...string) ([]byte, error) {
		out := fmt.Sprintf("%s\trefs/heads/main\n", dummySha1)
		return []byte(out), nil
	}

	r := resolver.New(
		resolver.WithBaseURL(server.URL),
		resolver.WithGitExec(mockGit),
	)

	sha, err := r.Resolve(context.Background(), "actions", "checkout", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != dummySha1 {
		t.Errorf("got %s, want %s", sha, dummySha1)
	}
}

func TestResolve_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer server.Close()

	mockGit := func(ctx context.Context, args ...string) ([]byte, error) {
		return []byte(""), nil
	}

	r := resolver.New(
		resolver.WithBaseURL(server.URL),
		resolver.WithGitExec(mockGit),
	)

	_, err := r.Resolve(context.Background(), "actions", "checkout", "non-existent-tag")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestResolve_APIAnnotatedTagPeeling(t *testing.T) {
	var tagPeeledCalled bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/repos/actions/checkout/commits/v4":
			// /commits returns 422 for annotated tag
			http.Error(w, `{"message":"No commit found for SHA: v4"}`, http.StatusUnprocessableEntity)
		case "/repos/actions/checkout/git/ref/tags/v4":
			// returns tag object
			fmt.Fprintf(w, `{
				"ref": "refs/tags/v4",
				"object": {
					"sha": "1111111111111111111111111111111111111111",
					"type": "tag",
					"url": "http://%s/repos/actions/checkout/git/tags/1111111111111111111111111111111111111111"
				}
			}`, req.Host)
		case "/repos/actions/checkout/git/tags/1111111111111111111111111111111111111111":
			tagPeeledCalled = true
			fmt.Fprintf(w, `{
				"sha": "1111111111111111111111111111111111111111",
				"object": {
					"sha": "%s",
					"type": "commit"
				}
			}`, dummySha1)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	r := resolver.New(resolver.WithBaseURL(server.URL))
	sha, err := r.Resolve(context.Background(), "actions", "checkout", "v4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != dummySha1 {
		t.Errorf("got %s, want peeled commit %s", sha, dummySha1)
	}
	if !tagPeeledCalled {
		t.Errorf("expected tag peeling endpoint to be called")
	}
}

func TestResolve_APIAnnotatedTagRejectsNonCommitObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/repos/actions/checkout/commits/v4":
			http.Error(w, `{"message":"No commit found for SHA: v4"}`, http.StatusUnprocessableEntity)
		case "/repos/actions/checkout/git/ref/tags/v4":
			fmt.Fprintf(w, `{
				"ref": "refs/tags/v4",
				"object": {
					"sha": "2222222222222222222222222222222222222222",
					"type": "tag",
					"url": "%s/repos/actions/checkout/git/tags/2222222222222222222222222222222222222222"
				}
			}`, "http://"+req.Host)
		case "/repos/actions/checkout/git/tags/2222222222222222222222222222222222222222":
			fmt.Fprintf(w, `{
				"sha": "2222222222222222222222222222222222222222",
				"object": {
					"sha": "%s",
					"type": "blob"
				}
			}`, dummySha1)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	r := resolver.New(resolver.WithBaseURL(server.URL), resolver.WithGitExec(func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("git unavailable")
	}))
	_, err := r.Resolve(context.Background(), "actions", "checkout", "v4")
	if err == nil {
		t.Fatal("expected error when tag object does not peel to a commit")
	}
}

func TestResolve_APIAnnotatedTagNestedPeeling(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/repos/actions/checkout/commits/v5":
			http.Error(w, `{"message":"No commit found for SHA: v5"}`, http.StatusUnprocessableEntity)
		case "/repos/actions/checkout/git/ref/tags/v5":
			fmt.Fprintf(w, `{
				"ref": "refs/tags/v5",
				"object": {
					"sha": "1111111111111111111111111111111111111111",
					"type": "tag"
				}
			}`)
		case "/repos/actions/checkout/git/tags/1111111111111111111111111111111111111111":
			fmt.Fprintf(w, `{
				"sha": "1111111111111111111111111111111111111111",
				"object": {
					"sha": "2222222222222222222222222222222222222222",
					"type": "tag"
				}
			}`)
		case "/repos/actions/checkout/git/tags/2222222222222222222222222222222222222222":
			fmt.Fprintf(w, `{
				"sha": "2222222222222222222222222222222222222222",
				"object": {
					"sha": "%s",
					"type": "commit"
				}
			}`, dummySha1)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	r := resolver.New(resolver.WithBaseURL(server.URL), resolver.WithGitExec(func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("git unavailable")
	}))
	sha, err := r.Resolve(context.Background(), "actions", "checkout", "v5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != dummySha1 {
		t.Errorf("got %s, want %s", sha, dummySha1)
	}
}

func TestResolve_APIAnnotatedTagIgnoresResponseObjectURL(t *testing.T) {
	var attackerCalls int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		atomic.AddInt32(&attackerCalls, 1)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"object": {"sha": "%s", "type": "commit"}}`, dummySha1)
	}))
	defer attacker.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/repos/actions/checkout/commits/v4":
			http.Error(w, `{"message":"No commit found for SHA: v4"}`, http.StatusUnprocessableEntity)
		case "/repos/actions/checkout/git/ref/tags/v4":
			fmt.Fprintf(w, `{
				"ref": "refs/tags/v4",
				"object": {
					"sha": "2222222222222222222222222222222222222222",
					"type": "tag",
					"url": "%s/attacker-endpoint"
				}
			}`, attacker.URL)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	r := resolver.New(resolver.WithBaseURL(server.URL), resolver.WithToken("secret-token"), resolver.WithGitExec(func(ctx context.Context, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("git unavailable")
	}))
	_, err := r.Resolve(context.Background(), "actions", "checkout", "v4")
	if err == nil {
		t.Error("expected error when tag cannot be peeled via canonical API base URL")
	}
	if atomic.LoadInt32(&attackerCalls) != 0 {
		t.Errorf("must not follow object URL from response, got %d attacker calls", atomic.LoadInt32(&attackerCalls))
	}
}

func TestResolve_APIAnnotatedTagInvalidResponsesFallBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"tree", fmt.Sprintf(`{"object":{"type":"tree","sha":%q}}`, dummySha1)},
		{"missing type", fmt.Sprintf(`{"object":{"sha":%q}}`, dummySha1)},
		{"invalid commit SHA", `{"object":{"type":"commit","sha":"invalid"}}`},
		{"invalid tag SHA", `{"object":{"type":"tag","sha":"invalid"}}`},
		{"cycle", fmt.Sprintf(`{"object":{"type":"tag","sha":%q}}`, tagSha)},
		{"malformed JSON", `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tagCalls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/repos/actions/checkout/git/ref/tags/v4":
					fmt.Fprintf(w, `{"object":{"type":"tag","sha":%q}}`, tagSha)
				case "/repos/actions/checkout/git/tags/" + tagSha:
					if atomic.AddInt32(&tagCalls, 1) > 10 {
						t.Error("tag peeling exceeded request limit")
						http.NotFound(w, req)
						return
					}
					fmt.Fprint(w, tc.body)
				default:
					http.NotFound(w, req)
				}
			}))
			defer server.Close()
			gitCalled := false
			r := resolver.New(resolver.WithBaseURL(server.URL), resolver.WithGitExec(func(ctx context.Context, args ...string) ([]byte, error) {
				gitCalled = true
				return []byte(fmt.Sprintf("%s\trefs/tags/v4^{}\n", dummySha2)), nil
			}))
			sha, err := r.Resolve(context.Background(), "actions", "checkout", "v4")
			if err != nil || sha != dummySha2 || !gitCalled {
				t.Fatalf("got %q, %v, git called %v", sha, err, gitCalled)
			}
		})
	}
}

func TestResolve_CaseInsensitiveCache(t *testing.T) {
	var apiCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		apiCalls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"sha": "%s"}`, dummySha1)
	}))
	defer server.Close()

	r := resolver.New(resolver.WithBaseURL(server.URL))
	ctx := context.Background()

	sha1, err := r.Resolve(ctx, "Actions", "Checkout", "v4")
	if err != nil || sha1 != dummySha1 {
		t.Fatalf("first call failed: %v", err)
	}

	sha2, err := r.Resolve(ctx, "actions", "checkout", "v4")
	if err != nil || sha2 != dummySha1 {
		t.Fatalf("second call failed: %v", err)
	}

	if apiCalls != 1 {
		t.Errorf("expected 1 API call due to case-insensitive cache, got %d", apiCalls)
	}
}

func TestResolve_EnterpriseGitURL(t *testing.T) {
	var capturedURL string
	mockGit := func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 1 {
			capturedURL = args[1]
		}
		return []byte(fmt.Sprintf("%s\trefs/heads/main\n", dummySha1)), nil
	}

	r := resolver.New(
		resolver.WithBaseURL("https://ghe.mycompany.internal/api/v3"),
		resolver.WithToken("ent-token"),
		resolver.WithGitExec(mockGit),
	)

	sha, err := r.Resolve(context.Background(), "corp", "action", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sha != dummySha1 {
		t.Errorf("got %s, want %s", sha, dummySha1)
	}

	expectedPrefix := "https://x-access-token:ent-token@ghe.mycompany.internal/corp/action.git"
	if capturedURL != expectedPrefix {
		t.Errorf("got git URL %q, want %q", capturedURL, expectedPrefix)
	}
}
