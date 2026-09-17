package pinner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/emirhan-karaca/action-pin/v2/internal/action"
	"github.com/emirhan-karaca/action-pin/v2/internal/resolver"
)

// Finding describes an unpinned action found in a workflow.
type Finding struct {
	File         string `json:"file"`
	Line         int    `json:"line"`
	Column       int    `json:"column"`
	Action       string `json:"action"`                  // original uses: string (e.g. actions/checkout@v4)
	Owner        string `json:"owner"`                   // repo owner
	Repo         string `json:"repo"`                    // repo name
	Ref          string `json:"ref"`                     // tag or branch name
	ResolvedSHA  string `json:"resolved_sha,omitempty"`  // 40-char commit SHA, when resolution is enabled
	PinnedAction string `json:"pinned_action,omitempty"` // new uses: string, when resolution is enabled
}

// Result summarizes a check or fix run across files.
type Result struct {
	FilesChecked  int       `json:"files_checked"`
	FilesModified int       `json:"files_modified"`
	UnpinnedCount int       `json:"unpinned_count"`
	Findings      []Finding `json:"findings"`
}

// Pinner coordinates reading, AST traversing, resolving, and updating workflow files.
type Pinner struct {
	resolver resolver.Resolver
	resolve  bool
}

// Option configures a Pinner.
type Option func(*Pinner)

// WithResolve enables resolving suggested SHAs in check mode. Fix mode always resolves.
func WithResolve(resolve bool) Option {
	return func(p *Pinner) {
		p.resolve = resolve
	}
}

// New creates a new Pinner. Checks are offline unless WithResolve(true) is used.
// The resolver may be nil when only performing offline checks.
func New(r resolver.Resolver, opts ...Option) *Pinner {
	p := &Pinner{resolver: r}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ProcessContent processes YAML content, identifying and optionally pinning actions.
// If fix is true and unpinned actions are found, it returns the updated YAML content.
// If fix is false or no changes are made, it returns the original content.
// Offline checks identify unpinned refs without verifying that the repository or ref exists.
func (p *Pinner) ProcessContent(ctx context.Context, filename string, content []byte, fix bool) ([]byte, []Finding, error) {
	if len(bytes.TrimSpace(content)) == 0 {
		return content, nil, nil
	}

	dec := yaml.NewDecoder(bytes.NewReader(content))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, fmt.Errorf("failed to parse YAML in %s: %w", filename, err)
		}
		docs = append(docs, &doc)
	}

	var findings []Finding
	var edits []sourceEdit
	for _, doc := range docs {
		docFindings, err := p.traverseAndPin(ctx, filename, doc, fix, &edits)
		if err != nil {
			return nil, nil, err
		}
		findings = append(findings, docFindings...)
	}

	if !fix || len(findings) == 0 {
		return content, findings, nil
	}
	if updated, ok := applySourceEdits(content, edits); ok {
		return updated, findings, nil
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for _, doc := range docs {
		if err := enc.Encode(doc); err != nil {
			return nil, nil, fmt.Errorf("failed to encode YAML for %s: %w", filename, err)
		}
	}
	if err := enc.Close(); err != nil {
		return nil, nil, fmt.Errorf("failed to close YAML encoder: %w", err)
	}

	return buf.Bytes(), findings, nil
}

// traverseAndPin searches a YAML node tree for `uses:` keys and pins them.
func (p *Pinner) traverseAndPin(ctx context.Context, filename string, node *yaml.Node, fix bool, edits *[]sourceEdit) ([]Finding, error) {
	var findings []Finding

	var walk func(n *yaml.Node) error
	walk = func(n *yaml.Node) error {
		if n == nil {
			return nil
		}

		if n.Kind == yaml.MappingNode {
			// In MappingNode, Content holds key/value pairs: [k0, v0, k1, v1, ...]
			for i := 0; i < len(n.Content); i += 2 {
				keyNode := n.Content[i]
				valNode := n.Content[i+1]

				refNode := valNode
				if refNode.Kind == yaml.AliasNode {
					refNode = refNode.Alias
				}
				if keyNode.Kind == yaml.ScalarNode && keyNode.Value == "uses" && refNode != nil && refNode.Kind == yaml.ScalarNode {
					actRef, err := action.Parse(refNode.Value)
					if err == nil && !actRef.IsLocal && !actRef.IsDocker && !actRef.IsDynamic && !actRef.IsPinned {
						finding := Finding{
							File:   filepath.ToSlash(filename),
							Line:   valNode.Line,
							Column: valNode.Column,
							Action: refNode.Value,
							Owner:  actRef.Owner,
							Repo:   actRef.Repo,
							Ref:    actRef.Ref,
						}
						if fix || p.resolve {
							if p.resolver == nil {
								return fmt.Errorf("resolving %s on line %d in %s: no resolver configured", refNode.Value, valNode.Line, filename)
							}
							sha, err := p.resolver.Resolve(ctx, actRef.Owner, actRef.Repo, actRef.Ref)
							if err != nil {
								return fmt.Errorf("resolving %s on line %d in %s: %w", refNode.Value, valNode.Line, filename, err)
							}
							finding.ResolvedSHA = sha
							finding.PinnedAction = actRef.PinnedString(sha)
						}
						findings = append(findings, finding)

						if fix {
							*edits = append(*edits, sourceEdit{node: *valNode, value: finding.PinnedAction, comment: actRef.Comment()})
							if valNode.Kind == yaml.AliasNode {
								valNode.Kind = yaml.ScalarNode
								valNode.Tag = "!!str"
								valNode.Style = refNode.Style
								valNode.Alias = nil
							}
							valNode.Value = finding.PinnedAction
							if valNode.LineComment == "" {
								valNode.LineComment = actRef.Comment()
							} else {
								valNode.LineComment += "; " + strings.TrimPrefix(actRef.Comment(), "# ")
							}
						}
					}
				}

				// Recurse into value node
				if err := walk(valNode); err != nil {
					return err
				}
			}
			return nil
		}

		for _, child := range n.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(node); err != nil {
		return nil, err
	}
	return findings, nil
}

// ProcessFile processes a single workflow file.
func (p *Pinner) ProcessFile(ctx context.Context, filePath string, fix bool) ([]Finding, bool, error) {
	if fix {
		plan, err := p.PlanFile(ctx, filePath)
		if err != nil {
			if plan == nil {
				return nil, false, err
			}
			return plan.Result.Findings, false, err
		}
		if err := plan.Apply(ctx); err != nil {
			return plan.Result.Findings, plan.Result.FilesModified > 0, err
		}
		return plan.Result.Findings, plan.Result.FilesModified > 0, nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false, fmt.Errorf("failed to read file %s: %w", filePath, err)
	}

	_, findings, err := p.ProcessContent(ctx, filePath, content, false)
	if err != nil {
		return nil, false, err
	}

	return findings, false, nil
}

// ProcessDirectory scans a directory (and subdirectories) for .yml and .yaml workflow files.
func (p *Pinner) ProcessDirectory(ctx context.Context, dirPath string, fix bool) (*Result, error) {
	if fix {
		plan, err := p.PlanDirectory(ctx, dirPath)
		if err != nil {
			if plan == nil {
				return nil, err
			}
			return &plan.Result, err
		}
		if err := plan.Apply(ctx); err != nil {
			return &plan.Result, err
		}
		return &plan.Result, nil
	}

	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("workflow directory not found: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dirPath)
	}

	result := &Result{}

	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yml" && ext != ".yaml" {
			return nil
		}

		result.FilesChecked++
		findings, modified, err := p.ProcessFile(ctx, path, false)
		if err != nil {
			return err
		}

		if modified {
			result.FilesModified++
		}
		result.UnpinnedCount += len(findings)
		result.Findings = append(result.Findings, findings...)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}
