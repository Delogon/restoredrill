package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
)

// checksumSample compares N random restored files (SHA-256) to the live
// source. Only files whose live copy has the same size and an mtime at or
// before the snapshot time are compared; files changed after the backup are
// skipped, not failed. If fewer than half the sampled files qualify, the
// check is error.
type checksumSample struct{}

func init() { Register(checksumSample{}) }

func (checksumSample) Type() string  { return "checksum_sample" }
func (checksumSample) Phase() string { return "data" }

func (checksumSample) Run(ctx context.Context, c *Context, cfg config.Check) Result {
	files, err := GlobFiles(c.RestoreDir, cfg.Paths)
	if err != nil {
		return errorf("checksum_sample: cannot walk restore dir: %v", err)
	}
	if len(files) == 0 {
		return errorf("checksum_sample: no restored files match the patterns — nothing to sample")
	}
	sample := cfg.Sample
	if sample <= 0 {
		sample = 50
	}
	if sample > len(files) {
		sample = len(files)
	}
	seed := cfg.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(seed))
	// Sample without replacement via a partial Fisher-Yates shuffle.
	idx := make([]int, len(files))
	for i := range idx {
		idx[i] = i
	}
	for i := 0; i < sample; i++ {
		j := i + rng.Intn(len(idx)-i)
		idx[i], idx[j] = idx[j], idx[i]
	}

	var qualified, skipped, mismatched int
	var mismatchNames []string
	for i := 0; i < sample; i++ {
		rel := files[idx[i]]
		restoredPath := filepath.Join(c.RestoreDir, filepath.FromSlash(rel))
		livePath := filepath.Join(cfg.SourceRoot, filepath.FromSlash(rel))

		rfi, err := os.Stat(restoredPath)
		if err != nil {
			return errorf("checksum_sample: cannot stat restored file %s: %v", rel, err)
		}
		lfi, err := os.Stat(livePath)
		if err != nil {
			skipped++ // live copy gone
			continue
		}
		if lfi.Size() != rfi.Size() {
			skipped++ // size changed after backup
			continue
		}
		if lfi.ModTime().After(c.Snapshot.Time) {
			skipped++ // modified after the snapshot
			continue
		}
		qualified++
		rsum, err := sha256File(restoredPath)
		if err != nil {
			return errorf("checksum_sample: cannot hash restored file %s: %v", rel, err)
		}
		lsum, err := sha256File(livePath)
		if err != nil {
			return errorf("checksum_sample: cannot hash live file %s: %v", livePath, err)
		}
		if rsum != lsum {
			mismatched++
			if len(mismatchNames) < 5 {
				mismatchNames = append(mismatchNames, rel)
			}
		}
	}

	details := map[string]any{
		"sampled":    sample,
		"qualified":  qualified,
		"skipped":    skipped,
		"mismatched": mismatched,
	}
	if qualified*2 < sample {
		return Result{Status: Error, Message: sprintf("only %d of %d sampled files qualify for comparison (live copies changed after the backup) — cannot verify checksums reliably", qualified, sample), Details: details}
	}
	if mismatched > 0 {
		details["mismatch_examples"] = mismatchNames
		return Result{Status: Fail, Message: sprintf("%d of %d compared files differ from the live source — the backup is corrupted", mismatched, qualified), Details: details}
	}
	return Result{Status: Pass, Message: sprintf("%d files verified identical (%d skipped as changed after backup)", qualified, skipped), Details: details}
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}