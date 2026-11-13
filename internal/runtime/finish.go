package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/iamnalinor/qual/internal/domain"
	"github.com/iamnalinor/qual/internal/report"
)

func (r *Runtime) finish(ctx context.Context, state *domain.RunState, p *agentProgress, data []byte) (string, *domain.AgentResult, error) {
	var args report.Finish
	if err := report.Decode(data, &args); err != nil {
		return "", nil, err
	}
	paths := []string{args.ReportPath, args.FindingsPath, args.ManifestPath}
	if p.Spec.Role == domain.RoleLeader {
		paths = append(paths, args.DecisionsPath)
	}
	if p.Spec.Role == domain.RoleSetup {
		paths = append(paths, args.ProposalsPath)
	}
	files := map[string][]byte{}
	for _, path := range paths {
		content, err := r.finishFile(p.Workspace, path)
		if err != nil {
			return "", nil, err
		}
		files[path] = content
	}
	var primary []domain.Finding
	for _, result := range state.Results {
		if result.Role == domain.RoleReviewer {
			primary = append(primary, result.Findings...)
		}
	}
	result, err := report.Validate(args, files, state.Snapshot, p.Spec, primary)
	if err != nil {
		return "", nil, err
	}
	for _, path := range paths {
		artifact, e := r.config.Store.PutArtifact(ctx, state.RunID, path, bytes.NewReader(files[path]))
		if e != nil {
			return "", nil, e
		}
		result.Artifacts = append(result.Artifacts, artifact)
	}
	result.Usage = p.Usage
	return "finish accepted; validated artifacts saved", &result, nil
}

func (r *Runtime) finishFile(root, path string) (data []byte, err error) {
	resolved, err := confined(root, path, false)
	if err != nil {
		return nil, err
	}
	f, info, err := openRegular(resolved)
	if err != nil {
		return nil, fmt.Errorf("open finish output: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if !info.Mode().IsRegular() || info.Size() > r.config.MaxArtifactBytes {
		return nil, coded(domain.ErrTool, "finish output must be a bounded regular file", nil)
	}
	data, err = io.ReadAll(io.LimitReader(f, r.config.MaxArtifactBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read finish output: %w", err)
	}
	if int64(len(data)) > r.config.MaxArtifactBytes {
		return nil, coded(domain.ErrTool, "finish output grew beyond size limit", nil)
	}
	return data, nil
}
