package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/managedqa"
	"github.com/jwebb7847-ui/mebooru/shared/processguard"
	"github.com/jwebb7847-ui/mebooru/shared/windowregistry"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
	"github.com/monbooru/monbooru/internal/acquisitionpolicy"
)

func liveRuntimeGrant(dir, generation string, cfg windowregistry.LiveConfig, md workerproto.MetadataPlanRequest, current func(context.Context) bool) processguard.ManagedRuntimeGrant {
	options, identity := cfg.Identity(md.Source)
	stage := func(c context.Context) (workerproto.StageAllocation, string, error) {
		var g windowregistry.TransferGrant
		var p windowregistry.LivePolicy
		if !current(c) || windowregistry.ReadArtifact(dir, windowregistry.ArtifactName(md.JobID, "stage"), &g, 64<<10) != nil ||
			windowregistry.ReadArtifact(dir, windowregistry.ArtifactName(md.JobID, "policy"), &p, windowregistry.MaxLivePolicy) != nil ||
			g.Allocation.Request.Transfer != p.Proposal.Transfer || g.PolicySHA256 != p.Proposal.PolicySHA256 || p.Request != md {
			return workerproto.StageAllocation{}, "", windowregistry.ErrWindow
		}
		path, err := windowregistry.Workspace(dir, g.Allocation)
		return g.Allocation, path, err
	}
	start := func(c context.Context, args []string, sub string) (processguard.StreamProbeChild, error) {
		if !current(c) {
			return nil, workerproto.ErrOffline
		}
		raw, err := json.Marshal(windowregistry.RuntimeCommand{Args: args, Request: md, Generation: generation, Subcategory: sub})
		if err != nil {
			return nil, err
		}
		return startWindowRuntimeCommand(dir, raw)
	}
	return processguard.ManagedRuntimeGrant{
		Metadata: md, Runtime: identity.Runtime, Config: options, Current: current, Transfer: stage,
		Start: func(c context.Context, args []string) (processguard.StreamProbeChild, error) {
			return start(c, args, "")
		},
		StartResolve: start,
		MetadataComplete: func(c context.Context, q workerproto.MetadataPlanRequest, sub string, b []byte) error {
			var p windowregistry.LivePolicy
			if !current(c) || q != md || windowregistry.ReadArtifact(dir, windowregistry.ArtifactName(md.JobID, "policy"), &p, windowregistry.MaxLivePolicy) != nil ||
				p.Request != q || p.Subcategory != sub || p.NativeSHA256 != managedqa.Digest(b) {
				return windowregistry.ErrWindow
			}
			return windowregistry.WriteBytes(dir, windowregistry.ArtifactName(md.JobID, "native"), b, workerproto.MaxMetadataOutputBytes)
		},
		Complete: func(c context.Context, a workerproto.StageAllocation, files []acquisitionplan.VerifiedTransferFile) error {
			known, _, err := stage(c)
			if err != nil || known != a {
				return windowregistry.ErrWindow
			}
			return windowregistry.WriteArtifact(dir, windowregistry.ArtifactName(md.JobID, "transfer"), windowregistry.TransferProof{Allocation: a, Files: files}, 1<<20)
		},
	}
}

func approvedLiveCommand(dir string, cfg windowregistry.LiveConfig, cmd windowregistry.RuntimeCommand) (bool, error) {
	q := cmd.Request
	reg, err := windowregistry.Open(filepath.Join(dir, "windows"))
	if err != nil {
		return false, err
	}
	defer reg.Close()
	grant, err := reg.Observe(q, cmd.Generation)
	if err != nil || !reg.Admitting(grant) {
		return false, windowregistry.ErrWindow
	}
	options, identity := cfg.Identity(q.Source)
	if grant.Identity != identity {
		return false, windowregistry.ErrWindow
	}
	if cmd.Subcategory != "" {
		want, err := options.Compile(q, q)
		if err != nil || !slices.Equal(want, cmd.Args) || !workerproto.QualifiedMetadataSource(q.Source, cmd.Subcategory) {
			return false, windowregistry.ErrWindow
		}
		return true, nil
	}
	classify := q
	classify.Kind = workerproto.MetadataClassify
	classify.First, classify.Last = 0, 0
	if want, err := options.Compile(classify, classify); err == nil && slices.Equal(want, cmd.Args) {
		return false, nil
	}
	var a windowregistry.TransferGrant
	if err := windowregistry.ReadArtifact(dir, windowregistry.ArtifactName(q.JobID, "stage"), &a, 64<<10); err != nil || a.Allocation.Request.Transfer.Metadata != q {
		return false, windowregistry.ErrWindow
	}
	root, err := windowregistry.Workspace(dir, a.Allocation)
	if err != nil {
		return false, err
	}
	commands, err := acquisitionplan.ObservePreparedTransferCommands(a.Allocation.Request.Transfer, options, root)
	if err != nil || !(slices.Equal(commands.Version, cmd.Args) || slices.Equal(commands.Negative, cmd.Args) || slices.Equal(commands.Positive, cmd.Args) || slices.Equal(commands.Transfer, cmd.Args)) {
		return false, windowregistry.ErrWindow
	}
	return false, nil
}

type boundedMetadata struct{ bytes.Buffer }

func (b *boundedMetadata) Write(p []byte) (int, error) {
	if len(p) > workerproto.MaxMetadataOutputBytes-b.Len() {
		return 0, errors.New("source metadata exceeds the window limit")
	}
	return b.Buffer.Write(p)
}

func runLiveRuntimeLeaf(dir, encoded string, cfg windowregistry.LiveConfig) error {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	var request windowregistry.RuntimeCommand
	if err != nil || len(raw) > 48<<10 || json.Unmarshal(raw, &request) != nil || len(request.Args) == 0 {
		return windowregistry.ErrWindow
	}
	resolve, err := approvedLiveCommand(dir, cfg, request)
	if err != nil {
		return err
	}
	store, err := acquisitionpolicy.Open(dir)
	if err != nil {
		return err
	}
	defer store.Close()
	if offline, err := store.Load(); err != nil || offline {
		return workerproto.ErrOffline
	}
	digest, err := windowregistry.RuntimeDigest(cfg.Runtime)
	if err != nil || digest != cfg.RuntimeSHA256 {
		return errors.New("configured gallery-dl changed, restart Mebooru")
	}
	if cfg.RuntimeWheel != "" {
		hash, err := windowregistry.RuntimeDigest(cfg.RuntimeWheel)
		if err != nil || hash != cfg.RuntimeWheelSHA256 {
			return errors.New("installed gallery-dl changed, restart Mebooru")
		}
	}
	env := append(os.Environ(), "XDG_CACHE_HOME="+dir, "XDG_CONFIG_HOME="+dir, "MEBOORU_RUNTIME_WHEEL="+cfg.RuntimeWheel)
	if slices.Contains(request.Args, "-E") {
		version := exec.Command(cfg.Runtime, "--config-ignore", "--version")
		version.Dir, version.Env = dir, env
		got, err := version.Output()
		if err != nil || strings.TrimSpace(string(got)) != cfg.Version() {
			return fmt.Errorf("managed acquisition requires gallery-dl %s", cfg.Version())
		}
	}
	args := cfg.RuntimeArgs(request.Args)
	if request.Args[len(request.Args)-1] == request.Request.URL {
		source := request.Request.Source
		if digest := cfg.SourceOptions[source]; digest != "" {
			path := sourceOptionsPath(dir, source)
			if !windowregistry.ValidLiveSource(source) || digest == "-" || !runtimeFileMatches(path, digest) {
				return errors.New("The source options are unavailable for managed acquisition.")
			}
			args = append([]string{"--config-json", path}, args...)
		}
	}
	cmd := exec.Command(cfg.Runtime, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.Stdout = os.Stdout
	var native boundedMetadata
	if resolve {
		cmd.Stdout = &native
	}
	var stderr probeOutput
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil && resolve && !stderr.overflow && len(stderr.Bytes()) == 0 {
		worker, lookupErr := processguard.ProbeSibling("monbooru", "monloader")
		if lookupErr != nil {
			return lookupErr
		}
		// This fixed policy command and all taxonomy I/O share the
		// gallery-dl leaf's OS containment and cancellation.
		policy := exec.Command(worker, "internal-managed-policy-v1", dir, encoded)
		policy.Dir, policy.Env = dir, env
		policy.Stdin = bytes.NewReader(native.Bytes())
		var diagnostics probeOutput
		policy.Stderr = &diagnostics
		err = policy.Run()
		if err == nil {
			_, err = os.Stdout.Write(native.Bytes())
		} else {
			_, _ = stderr.Write(diagnostics.Bytes())
		}
	}
	result := runtimeVersionResult{Started: cmd.Process != nil, Overflow: stderr.overflow, Stderr: stderr.Bytes()}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			result.ExitCode = exit.ExitCode()
		} else {
			result.Failed = true
		}
	}
	return json.NewEncoder(os.Stderr).Encode(result)
}
