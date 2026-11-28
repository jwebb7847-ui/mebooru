package windowregistry

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jwebb7847-ui/mebooru/shared/acquisitionplan"
	"github.com/jwebb7847-ui/mebooru/shared/workerproto"
)

const LiveConfigName = "live-config.json"
const MaxLivePolicy = 12 << 20

// LiveConfig is written by the library while holding the profile lock.
// It identifies the operator-selected runtime. Policy comes from the
// existing Meloader config, which the contained policy process reads.
type LiveConfig struct {
	Runtime, RuntimeSHA256, PolicyConfig string
	RuntimeWheel, RuntimeWheelSHA256     string
	SleepRequest                         float64
	MaxItems                             int
}

func LoadLive(dir string) (LiveConfig, error) {
	var cfg LiveConfig
	err := ReadArtifact(dir, LiveConfigName, &cfg, 64<<10)
	if err != nil {
		return LiveConfig{}, err
	}
	if !filepath.IsAbs(cfg.Runtime) || !filepath.IsAbs(cfg.PolicyConfig) || !validHex(cfg.RuntimeSHA256, 32) ||
		cfg.SleepRequest < 0 || math.IsInf(cfg.SleepRequest, 0) || math.IsNaN(cfg.SleepRequest) || cfg.MaxItems < 1 || cfg.MaxItems > workerproto.MaxMetadataWindow {
		return LiveConfig{}, ErrWindow
	}
	if cfg.RuntimeWheel != "" && (!filepath.IsAbs(cfg.RuntimeWheel) || !validHex(cfg.RuntimeWheelSHA256, 32)) {
		return LiveConfig{}, ErrWindow
	}
	return cfg, nil
}

func (c LiveConfig) Version() string {
	if c.RuntimeWheel != "" {
		return acquisitionplan.StrictQualifiedUpdateVersion
	}
	return acquisitionplan.StrictQualifiedVersion
}

func (c LiveConfig) Identity(source string) (acquisitionplan.MetadataConfig, acquisitionplan.MetadataIdentity) {
	config := acquisitionplan.MetadataConfig{Generation: 1, Source: source}
	digest, _ := config.Identity()
	return config, acquisitionplan.MetadataIdentity{
		Config:  digest,
		Runtime: acquisitionplan.RuntimeIdentity{Generation: 1, Version: c.Version(), ArtifactSHA256: c.RuntimeSHA256, ManifestSHA256: cmp.Or(c.RuntimeWheelSHA256, c.RuntimeSHA256)},
	}
}

func (c LiveConfig) RuntimeArgs(args []string) []string {
	if c.SleepRequest <= 0 {
		return args
	}
	pacing := strconv.FormatFloat(c.SleepRequest, 'f', -1, 64)
	return append([]string{args[0], "-o", "sleep-request=" + pacing, "-o", "sleep=" + pacing}, args[1:]...)
}

func RuntimeDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 512<<20 {
		return "", ErrWindow
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return "", ErrWindow
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (512<<20)+1))
	if err != nil || n != info.Size() {
		return "", ErrWindow
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type LivePolicy struct {
	Request                   workerproto.MetadataPlanRequest
	Subcategory, NativeSHA256 string
	Listed, Blocked           []string
	Proposal                  workerproto.WindowAllocateProposal
}

type RuntimeCommand struct {
	Args                    []string
	Request                 workerproto.MetadataPlanRequest
	Generation, Subcategory string
}

type TransferGrant struct {
	Allocation   workerproto.StageAllocation
	PolicySHA256 string
}

type TransferProof struct {
	Allocation workerproto.StageAllocation
	Files      []acquisitionplan.VerifiedTransferFile
}

func ArtifactName(job int64, suffix string) string { return fmt.Sprintf("job-%d-%s.json", job, suffix) }
func LiveOnline(dir string) bool {
	record, err := ReadBytes(dir, workerproto.AcquisitionPolicyFile, 128)
	return err == nil && string(record) == workerproto.AcquisitionOnlineRecord
}

func Workspace(dir string, g workerproto.StageAllocation) (string, error) {
	if !workerproto.ValidStageAllocation(g) {
		return "", ErrWindow
	}
	p := filepath.Join(dir, "stage", g.Request.OperationID, g.TransferID)
	actual, err := filepath.EvalSymlinks(p)
	if err != nil || actual != p {
		return "", ErrWindow
	}
	info, err := os.Lstat(p)
	if err != nil || !info.IsDir() {
		return "", ErrWindow
	}
	return p, nil
}

func ReadArtifact(dir, name string, dst any, limit int64) error {
	raw, err := ReadBytes(dir, name, limit)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, dst) != nil {
		return ErrWindow
	}
	canonical, err := json.Marshal(dst)
	if err != nil || !bytes.Equal(canonical, raw) {
		return ErrWindow
	}
	return nil
}

func ReadBytes(dir, name string, limit int64) ([]byte, error) {
	if filepath.Base(name) != name {
		return nil, ErrWindow
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	i, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !i.Mode().IsRegular() || i.Size() < 1 || i.Size() > limit {
		return nil, ErrWindow
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(i, actual) {
		return nil, ErrWindow
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) != i.Size() {
		return nil, ErrWindow
	}
	return raw, nil
}

func WriteArtifact(dir, name string, src any, limit int64) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return WriteBytes(dir, name, raw, limit)
}

func WriteBytes(dir, name string, raw []byte, limit int64) error {
	if filepath.Base(name) != name || len(raw) == 0 || int64(len(raw)) > limit {
		return ErrWindow
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
