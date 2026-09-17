package pinner_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/emirhan-karaca/action-pin/v2/internal/pinner"
)

type mockResolver struct {
	mapping map[string]string
	calls   []string
}

func TestPinner_ExactSourcePreservation(t *testing.T) {
	const sha = "b4ffde65f46336ab88eb53be808477a3936bae11"
	p := pinner.New(&mockResolver{mapping: map[string]string{"actions/checkout@v4": sha}})
	cases := []struct{ name, input, want string }{
		{"indent-and-comments", "# header\njobs:\n    build:\n        steps:\n        - uses: actions/checkout@v4  # needed for history\n\n        - run: |\n            echo hello\n", "# header\njobs:\n    build:\n        steps:\n        - uses: actions/checkout@" + sha + "  # needed for history; v4 [pinned by action-pin]\n\n        - run: |\n            echo hello\n"},
		{"crlf", "uses: actions/checkout@v4\r\n", "uses: actions/checkout@" + sha + " # v4 [pinned by action-pin]\r\n"},
		{"no-final-newline", "uses: 'actions/checkout@v4'", "uses: 'actions/checkout@" + sha + "' # v4 [pinned by action-pin]"},
		{"flow-unicode", "{name: Türkçe, uses: \"actions/checkout@v4\", with: {fetch-depth: 0}}\n", "{name: Türkçe, uses: \"actions/checkout@" + sha + "\", with: {fetch-depth: 0}} # v4 [pinned by action-pin]\n"},
		{"documents", "---\nuses: actions/checkout@v4\n...\n---\nuses: ./local\n", "---\nuses: actions/checkout@" + sha + " # v4 [pinned by action-pin]\n...\n---\nuses: ./local\n"},
		{"bom", "\ufeffuses: actions/checkout@v4\n", "\ufeffuses: actions/checkout@" + sha + " # v4 [pinned by action-pin]\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, findings, err := p.ProcessContent(context.Background(), "ci.yml", []byte(tc.input), true)
			if err != nil || len(findings) != 1 {
				t.Fatalf("fix: findings=%v err=%v", findings, err)
			}
			if string(out) != tc.want {
				t.Fatalf("got %q\nwant %q", out, tc.want)
			}
			again, findings, err := p.ProcessContent(context.Background(), "ci.yml", out, true)
			if err != nil || len(findings) != 0 || string(again) != string(out) {
				t.Fatalf("not idempotent: %q %v %v", again, findings, err)
			}
		})
	}
}

func TestPinner_AliasUses(t *testing.T) {
	const sha = "b4ffde65f46336ab88eb53be808477a3936bae11"
	const input = "env:\n  ACTION_REF: &checkout actions/checkout@v4\n  OTHER: *checkout\njobs:\n  test:\n    steps:\n      - uses: *checkout # keep first\n      - uses: *checkout # keep second\n"
	for _, mode := range []string{"check", "resolve", "fix"} {
		t.Run(mode, func(t *testing.T) {
			res := &mockResolver{mapping: map[string]string{"actions/checkout@v4": sha}}
			p := pinner.New(res, pinner.WithResolve(mode == "resolve"))
			out, findings, err := p.ProcessContent(context.Background(), "ci.yml", []byte(input), mode == "fix")
			if err != nil || len(findings) != 2 {
				t.Fatalf("findings=%+v err=%v", findings, err)
			}
			for i, finding := range findings {
				if finding.Line != 7+i || finding.Column != 15 || finding.Action != "actions/checkout@v4" {
					t.Fatalf("unexpected finding: %+v", finding)
				}
				if mode != "check" && finding.ResolvedSHA != sha {
					t.Fatalf("missing resolved SHA: %+v", finding)
				}
			}
			if mode == "check" && len(res.calls) != 0 {
				t.Fatalf("offline check resolved aliases: %v", res.calls)
			}
			if mode != "fix" {
				if string(out) != input {
					t.Fatalf("check changed content: %s", out)
				}
				return
			}
			var decoded struct {
				Env  map[string]string `yaml:"env"`
				Jobs map[string]struct {
					Steps []struct {
						Uses string `yaml:"uses"`
					} `yaml:"steps"`
				} `yaml:"jobs"`
			}
			if err := yaml.Unmarshal(out, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Env["ACTION_REF"] != "actions/checkout@v4" || decoded.Env["OTHER"] != "actions/checkout@v4" {
				t.Fatalf("changed shared anchor: %s", out)
			}
			for _, step := range decoded.Jobs["test"].Steps {
				if step.Uses != "actions/checkout@"+sha {
					t.Fatalf("alias not pinned: %s", out)
				}
			}
			for _, comment := range []string{"keep first", "keep second"} {
				if !strings.Contains(string(out), comment) {
					t.Fatalf("lost comment: %s", out)
				}
			}
			again, findings, err := p.ProcessContent(context.Background(), "ci.yml", out, true)
			if err != nil || len(findings) != 0 || string(again) != string(out) {
				t.Fatalf("not idempotent: %s %+v %v", again, findings, err)
			}
		})
	}
}

func TestPinner_ComplexScalarFallbackPreservesComments(t *testing.T) {
	const sha = "b4ffde65f46336ab88eb53be808477a3936bae11"
	p := pinner.New(&mockResolver{mapping: map[string]string{"actions/checkout@v4": sha}})
	for _, input := range []string{
		"uses: >- # keep this\n  actions/checkout@v4\n",
		"uses: &checkout actions/checkout@v4 # keep this\nother: *checkout\n",
		"uses: !!str actions/checkout@v4 # keep this\n",
		"uses: \"actions/checkout@v\\x34\" # keep this\n",
	} {
		out, _, err := p.ProcessContent(context.Background(), "ci.yml", []byte(input), true)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]interface{}
		if err := yaml.Unmarshal(out, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded["uses"] != "actions/checkout@"+sha || !strings.Contains(string(out), "keep this") {
			t.Fatalf("lost action or comment: %s", out)
		}
	}
}

func (m *mockResolver) Resolve(ctx context.Context, owner, repo, ref string) (string, error) {
	key := fmt.Sprintf("%s/%s@%s", owner, repo, ref)
	m.calls = append(m.calls, key)
	if sha, ok := m.mapping[key]; ok {
		return sha, nil
	}
	return "", fmt.Errorf("mock: ref not found for %s", key)
}

func TestPinner_CommentAndFormatPreservation(t *testing.T) {
	resolver := &mockResolver{
		mapping: map[string]string{
			"actions/checkout@v4": "b4ffde65f46336ab88eb53be808477a3936bae11",
			"actions/setup-go@v5": "0a12ed9d6a96ab950c8f5ff42f17e3e293d2eab0",
			"actions/cache@v3":    "dacf3200ff73ea515d96a79eefffa1cc3a0bfa99",
		},
	}

	p := pinner.New(resolver)

	originalYAML := `# Workflow header comment
name: CI Pipeline

on:
  push:
    branches: [main]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      # Step 1: Check out code
      - name: Checkout
        uses: actions/checkout@v4

      # Step 2: Setup Go
      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.22'

      # Step 3: Cache restore with subpath
      - name: Cache
        uses: actions/cache/restore@v3

      # Local action (must be ignored)
      - name: Local Step
        uses: ./.github/actions/custom

      # Docker action (must be ignored)
      - name: Container Step
        uses: docker://alpine:3.18

      # Already pinned (must be untouched)
      - name: Already Pinned
        uses: actions/upload-artifact@a8a3f3ad30e3422c9c7b888a15615d19a852e5bf # v4 [pinned by action-pin]
`

	ctx := context.Background()

	// 1. Check mode
	_, findings, err := p.ProcessContent(ctx, "ci.yml", []byte(originalYAML), false)
	if err != nil {
		t.Fatalf("check mode failed: %v", err)
	}

	if len(findings) != 3 {
		t.Fatalf("expected 3 findings, got %d", len(findings))
	}

	if len(resolver.calls) != 0 {
		t.Fatalf("offline check called resolver: %v", resolver.calls)
	}
	if findings[0].Action != "actions/checkout@v4" || findings[0].ResolvedSHA != "" {
		t.Errorf("unexpected finding[0]: %+v", findings[0])
	}
	if findings[1].Action != "actions/setup-go@v5" || findings[1].ResolvedSHA != "" {
		t.Errorf("unexpected finding[1]: %+v", findings[1])
	}
	if findings[2].Action != "actions/cache/restore@v3" || findings[2].ResolvedSHA != "" {
		t.Errorf("unexpected finding[2]: %+v", findings[2])
	}

	// 2. Fix mode
	fixedBytes, fixFindings, err := p.ProcessContent(ctx, "ci.yml", []byte(originalYAML), true)
	if err != nil {
		t.Fatalf("fix mode failed: %v", err)
	}

	if len(fixFindings) != 3 {
		t.Fatalf("expected 3 findings during fix, got %d", len(fixFindings))
	}

	fixedYAML := string(fixedBytes)

	// Verify pinned SHAs and comments
	expectedSubstrings := []string{
		"# Workflow header comment",
		"# Step 1: Check out code",
		"uses: actions/checkout@b4ffde65f46336ab88eb53be808477a3936bae11 # v4 [pinned by action-pin]",
		"# Step 2: Setup Go",
		"uses: actions/setup-go@0a12ed9d6a96ab950c8f5ff42f17e3e293d2eab0 # v5 [pinned by action-pin]",
		"# Step 3: Cache restore with subpath",
		"uses: actions/cache/restore@dacf3200ff73ea515d96a79eefffa1cc3a0bfa99 # v3 [pinned by action-pin]",
		"uses: ./.github/actions/custom",
		"uses: docker://alpine:3.18",
		"uses: actions/upload-artifact@a8a3f3ad30e3422c9c7b888a15615d19a852e5bf # v4 [pinned by action-pin]",
	}

	for _, sub := range expectedSubstrings {
		if !strings.Contains(fixedYAML, sub) {
			t.Errorf("fixed YAML missing expected content: %q\n\nFull fixed YAML:\n%s", sub, fixedYAML)
		}
	}

	// 3. Idempotency test: processing fixed content again should report 0 findings and leave it unchanged
	secondBytes, secondFindings, err := p.ProcessContent(ctx, "ci.yml", fixedBytes, true)
	if err != nil {
		t.Fatalf("idempotency fix run failed: %v", err)
	}
	if len(secondFindings) != 0 {
		t.Fatalf("expected 0 findings on second run, got %d", len(secondFindings))
	}
	if string(secondBytes) != fixedYAML {
		t.Errorf("idempotency failed: second run changed the content")
	}
}

func TestPinner_ProcessDirectory(t *testing.T) {
	tempDir := t.TempDir()

	wf1 := filepath.Join(tempDir, "ci.yml")
	wf2 := filepath.Join(tempDir, "release.yaml")
	nonWf := filepath.Join(tempDir, "notes.txt")

	err := os.WriteFile(wf1, []byte("jobs:\n  j1:\n    steps:\n      - uses: actions/checkout@v4\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(wf2, []byte("jobs:\n  j2:\n    steps:\n      - uses: actions/setup-go@v5\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(nonWf, []byte("some text"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	resolver := &mockResolver{
		mapping: map[string]string{
			"actions/checkout@v4": "b4ffde65f46336ab88eb53be808477a3936bae11",
			"actions/setup-go@v5": "0a12ed9d6a96ab950c8f5ff42f17e3e293d2eab0",
		},
	}

	p := pinner.New(resolver)

	// Test check mode on directory
	res, err := p.ProcessDirectory(context.Background(), tempDir, false)
	if err != nil {
		t.Fatalf("ProcessDirectory check failed: %v", err)
	}

	if res.FilesChecked != 2 {
		t.Errorf("FilesChecked = %d, want 2", res.FilesChecked)
	}
	if res.UnpinnedCount != 2 {
		t.Errorf("UnpinnedCount = %d, want 2", res.UnpinnedCount)
	}
	if res.FilesModified != 0 {
		t.Errorf("FilesModified = %d, want 0", res.FilesModified)
	}

	// Test fix mode on directory
	resFix, err := p.ProcessDirectory(context.Background(), tempDir, true)
	if err != nil {
		t.Fatalf("ProcessDirectory fix failed: %v", err)
	}

	if resFix.FilesModified != 2 {
		t.Errorf("FilesModified = %d, want 2", resFix.FilesModified)
	}

	// Test subsequent check: should find 0 unpinned
	resCheckAfter, err := p.ProcessDirectory(context.Background(), tempDir, false)
	if err != nil {
		t.Fatalf("ProcessDirectory post-check failed: %v", err)
	}
	if resCheckAfter.UnpinnedCount != 0 {
		t.Errorf("UnpinnedCount after fix = %d, want 0", resCheckAfter.UnpinnedCount)
	}
}

func TestPinner_EmptyAndMultiDoc(t *testing.T) {
	resolver := &mockResolver{
		mapping: map[string]string{
			"actions/checkout@v4": "b4ffde65f46336ab88eb53be808477a3936bae11",
		},
	}
	p := pinner.New(resolver)

	// Empty content
	out, findings, err := p.ProcessContent(context.Background(), "empty.yml", []byte("   \n"), true)
	if err != nil {
		t.Fatalf("empty failed: %v", err)
	}
	if len(findings) != 0 || len(strings.TrimSpace(string(out))) != 0 {
		t.Errorf("unexpected result for empty content")
	}

	// Multi-document YAML
	multiDoc := `---
name: Doc1
jobs:
  j1:
    steps:
      - uses: actions/checkout@v4
---
name: Doc2
jobs:
  j2:
    steps:
      - uses: ./.github/actions/local
`
	fixed, findings, err := p.ProcessContent(context.Background(), "multi.yml", []byte(multiDoc), true)
	if err != nil {
		t.Fatalf("multi-doc failed: %v", err)
	}
	if len(findings) != 1 {
		t.Errorf("expected 1 finding, got %d", len(findings))
	}
	if !strings.Contains(string(fixed), "actions/checkout@b4ffde65f46336ab88eb53be808477a3936bae11 # v4 [pinned by action-pin]") {
		t.Errorf("multi-doc fix failed:\n%s", string(fixed))
	}
}

func TestPinner_ReusableWorkflow(t *testing.T) {
	resolver := &mockResolver{
		mapping: map[string]string{
			"octocat/shared-workflows@v2.1": "abcdef1234567890abcdef1234567890abcdef12",
		},
	}
	p := pinner.New(resolver)

	input := `name: Caller
jobs:
  call-workflow:
    uses: octocat/shared-workflows/.github/workflows/reusable.yml@v2.1
    with:
      username: mona
`
	fixed, findings, err := p.ProcessContent(context.Background(), "caller.yml", []byte(input), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}

	expectedUses := "uses: octocat/shared-workflows/.github/workflows/reusable.yml@abcdef1234567890abcdef1234567890abcdef12 # v2.1 [pinned by action-pin]"
	if !strings.Contains(string(fixed), expectedUses) {
		t.Errorf("expected pinned reusable workflow, got:\n%s", string(fixed))
	}
}

func TestPinner_InvalidYAML(t *testing.T) {
	resolver := &mockResolver{mapping: map[string]string{}}
	p := pinner.New(resolver)

	invalidYAML := "jobs: [unclosed list"
	_, _, err := p.ProcessContent(context.Background(), "invalid.yml", []byte(invalidYAML), false)
	if err == nil {
		t.Fatal("expected error on invalid YAML, got nil")
	}
}

func TestPinner_MultilineScriptAndExpressions(t *testing.T) {
	resolver := &mockResolver{
		mapping: map[string]string{
			"actions/checkout@v4": "b4ffde65f46336ab88eb53be808477a3936bae11",
		},
	}
	p := pinner.New(resolver)

	input := `name: Complex Workflow
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v4
      - name: Dynamic Step
        uses: actions/checkout@${{ matrix.version }}
      - name: Inline Script
        run: |
          # A comment inside bash script
          echo "Processing # not a comment"
          if [ -d ".git" ]; then
            # Inside conditional
            echo "Git exists"
          fi
`

	fixed, findings, err := p.ProcessContent(context.Background(), "workflow.yml", []byte(input), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(findings) != 1 {
		t.Fatalf("expected 1 finding (static checkout only), got %d findings", len(findings))
	}

	fixedStr := string(fixed)
	if !strings.Contains(fixedStr, "actions/checkout@b4ffde65f46336ab88eb53be808477a3936bae11 # v4 [pinned by action-pin]") {
		t.Errorf("missing pinned action, got:\n%s", fixedStr)
	}
	if !strings.Contains(fixedStr, "# A comment inside bash script") {
		t.Errorf("multiline comment was mangled, got:\n%s", fixedStr)
	}
	if !strings.Contains(fixedStr, "echo \"Processing # not a comment\"") {
		t.Errorf("script line was mangled, got:\n%s", fixedStr)
	}
}

func TestPinner_QuotedAction(t *testing.T) {
	resolver := &mockResolver{
		mapping: map[string]string{
			"actions/checkout@v4": "b4ffde65f46336ab88eb53be808477a3936bae11",
			"actions/setup-go@v5": "0a12ed9d6a96ab950c8f5ff42f17e3e293d2eab0",
		},
	}
	p := pinner.New(resolver)

	input := `name: Quoted Actions
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - name: Double Quoted
        uses: "actions/checkout@v4"
      - name: Single Quoted
        uses: 'actions/setup-go@v5'
`

	fixed, findings, err := p.ProcessContent(context.Background(), "test.yml", []byte(input), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findings))
	}

	fixedStr := string(fixed)
	if !strings.Contains(fixedStr, `uses: "actions/checkout@b4ffde65f46336ab88eb53be808477a3936bae11" # v4 [pinned by action-pin]`) {
		t.Errorf("double quote style not preserved:\n%s", fixedStr)
	}
	if !strings.Contains(fixedStr, `uses: 'actions/setup-go@0a12ed9d6a96ab950c8f5ff42f17e3e293d2eab0' # v5 [pinned by action-pin]`) {
		t.Errorf("single quote style not preserved:\n%s", fixedStr)
	}
}

func TestPinner_OfflineCheckReportsUnresolvableRefs(t *testing.T) {
	const input = "jobs:\n  test:\n    steps:\n      - uses: nonexistent-owner/nonexistent-repo@missing-tag\n      - uses: actions/cache/restore@not-a-real-branch\n      - uses: actions/checkout@b4ffde65f46336ab88eb53be808477a3936bae11\n      - uses: ./local\n      - uses: docker://alpine:3\n      - uses: actions/checkout@${{ matrix.ref }}\n"
	res := &mockResolver{} // Every resolution would fail.
	p := pinner.New(res)
	output, findings, err := p.ProcessContent(context.Background(), "ci.yml", []byte(input), false)
	if err != nil {
		t.Fatalf("offline check failed: %v", err)
	}
	if len(res.calls) != 0 {
		t.Fatalf("offline check attempted resolution: %v", res.calls)
	}
	if string(output) != input {
		t.Fatal("check changed source content")
	}
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want 2", len(findings))
	}
	if findings[0].Line != 4 || findings[0].Ref != "missing-tag" || findings[1].Action != "actions/cache/restore@not-a-real-branch" {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	for _, finding := range findings {
		if finding.ResolvedSHA != "" || finding.PinnedAction != "" {
			t.Errorf("offline finding has a suggested pin: %+v", finding)
		}
	}

	// No resolver is needed at all for this mode.
	if _, _, err := pinner.New(nil).ProcessContent(context.Background(), "ci.yml", []byte(input), false); err != nil {
		t.Fatalf("check without a resolver failed: %v", err)
	}
}

func TestPinner_ResolveOption(t *testing.T) {
	const sha = "b4ffde65f46336ab88eb53be808477a3936bae11"
	const input = "uses: actions/cache/restore@v4 # retain me\n"
	for _, tc := range []struct {
		name    string
		resolve bool
		fix     bool
	}{
		{name: "check-with-resolution", resolve: true},
		{name: "fix-always-resolves", fix: true},
		{name: "fix-with-redundant-resolution", resolve: true, fix: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &mockResolver{mapping: map[string]string{"actions/cache@v4": sha}}
			p := pinner.New(res, pinner.WithResolve(tc.resolve))
			output, findings, err := p.ProcessContent(context.Background(), "ci.yml", []byte(input), tc.fix)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.calls) != 1 || res.calls[0] != "actions/cache@v4" {
				t.Fatalf("unexpected resolution calls: %v", res.calls)
			}
			if len(findings) != 1 || findings[0].ResolvedSHA != sha || findings[0].PinnedAction != "actions/cache/restore@"+sha {
				t.Fatalf("unexpected resolved findings: %+v", findings)
			}
			if tc.fix {
				if !strings.Contains(string(output), "uses: actions/cache/restore@"+sha+" # retain me; v4 [pinned by action-pin]") {
					t.Fatalf("unexpected pinned output: %s", output)
				}
			} else if string(output) != input {
				t.Fatal("resolved check changed source content")
			}
		})
	}
}

func TestPinner_ResolutionFailureDoesNotWriteFile(t *testing.T) {
	const input = "uses: nonexistent-owner/nonexistent-repo@missing-tag\n"
	for _, fix := range []bool{false, true} {
		t.Run(fmt.Sprintf("fix=%t", fix), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ci.yml")
			if err := os.WriteFile(path, []byte(input), 0644); err != nil {
				t.Fatal(err)
			}
			p := pinner.New(&mockResolver{}, pinner.WithResolve(true))
			_, modified, err := p.ProcessFile(context.Background(), path, fix)
			if err == nil || !strings.Contains(err.Error(), "ref not found") {
				t.Fatalf("expected resolution failure, got %v", err)
			}
			output, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if modified || string(output) != input {
				t.Fatal("failed resolution changed file")
			}
		})
	}
}
