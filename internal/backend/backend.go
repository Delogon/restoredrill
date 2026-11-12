// Package backend defines the adapter interface implemented by the restic
// and Borg backends. Adapters are strictly read-only towards repositories:
// they may only list, inspect and extract data, never modify it.
package backend

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// Snapshot is one backup snapshot (restic snapshot or Borg archive).
type Snapshot struct {
	ID    string    `json:"id"` // restic snapshot ID or Borg archive name
	Time  time.Time `json:"time"`
	Host  string    `json:"host,omitempty"`
	Tags  []string  `json:"tags,omitempty"`
	Paths []string  `json:"paths,omitempty"`
}

// SnapshotFilter narrows the snapshot list.
type SnapshotFilter struct {
	Tags        []string // restic only: snapshot must carry all listed tags
	Host        string
	Paths       []string // restic only: snapshot must cover these paths
	ArchiveGlob string   // borg only: archive name glob
}

// Progress reports restore progress.
type Progress struct {
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
	Message    string `json:"message,omitempty"`
}

// RestoreStats describes a completed restore.
type RestoreStats struct {
	Bytes    int64         `json:"bytes"`
	Files    int64         `json:"files"`
	Duration time.Duration `json:"duration"`
}

// Backend is the adapter interface. Both adapters must produce the same
// restore layout: restored files keep their original absolute path below the
// restore target directory.
type Backend interface {
	Kind() string // "restic" | "borg"
	Version(ctx context.Context) (string, error)
	ListSnapshots(ctx context.Context, f SnapshotFilter) ([]Snapshot, error)
	EstimateRestoreSize(ctx context.Context, s Snapshot, paths []string) (int64, error)
	Restore(ctx context.Context, s Snapshot, paths []string, target string, progress func(Progress)) (RestoreStats, error)
}

// SnapshotDetailer is an optional interface for backends that can enrich a
// snapshot with metadata (e.g. Borg archive hostname) after selection.
type SnapshotDetailer interface {
	SnapshotDetails(ctx context.Context, s Snapshot) (Snapshot, error)
}

// Error kinds let the runner distinguish repository problems (error) from
// data problems (fail).
const (
	KindAuth        = "auth"
	KindUnreachable = "unreachable"
	KindNotFound    = "notfound"
	KindVersion     = "version"
	KindLocked      = "locked"
	KindOther       = "other"
)

// Error is a classified backend error.
type Error struct {
	Kind    string
	Message string
	Err     error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

func errorf(kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Message: fmt.Sprintf(format, args...)}
}

// Selection parameters for SelectSnapshot.
type Selection struct {
	Select string        // latest | random | oldest
	Within time.Duration // window for random and oldest
	Seed   int64         // seed for random; stored with the run
}

// SelectSnapshot applies the selector to a snapshot list. now is injectable
// for tests. It returns an error when no snapshot matches.
func SelectSnapshot(snaps []Snapshot, sel Selection, now time.Time) (Snapshot, error) {
	if len(snaps) == 0 {
		return Snapshot{}, errorf(KindNotFound, "no snapshots found in repository — check the repository and snapshot filters (tags, host, paths, archive_glob)")
	}
	candidates := snaps
	if sel.Within > 0 && sel.Select != "latest" {
		cutoff := now.Add(-sel.Within)
		candidates = nil
		for _, s := range snaps {
			if !s.Time.Before(cutoff) {
				candidates = append(candidates, s)
			}
		}
		if len(candidates) == 0 {
			return Snapshot{}, errorf(KindNotFound, "no snapshots within the last %s — widen snapshot.within or check that backups are running", sel.Within)
		}
	}
	switch sel.Select {
	case "latest", "":
		best := candidates[0]
		for _, s := range candidates[1:] {
			if s.Time.After(best.Time) {
				best = s
			}
		}
		return best, nil
	case "oldest":
		best := candidates[0]
		for _, s := range candidates[1:] {
			if s.Time.Before(best.Time) {
				best = s
			}
		}
		return best, nil
	case "random":
		rng := rand.New(rand.NewSource(sel.Seed))
		return candidates[rng.Intn(len(candidates))], nil
	default:
		return Snapshot{}, errorf(KindOther, "unknown snapshot selector %q", sel.Select)
	}
}

// SortSnapshots sorts by time ascending (stable for equal times).
func SortSnapshots(snaps []Snapshot) {
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].Time.Before(snaps[j].Time) })
}

// pathMatches reports whether a snapshot covering snapPaths satisfies a
// filter path: either path may be a prefix of the other.
func pathMatches(snapPaths []string, filter string) bool {
	for _, p := range snapPaths {
		if p == filter || strings.HasPrefix(p, filter+"/") || strings.HasPrefix(filter, p+"/") {
			return true
		}
	}
	return false
}

// hasAllTags reports whether tags contains every wanted tag.
func hasAllTags(tags, wanted []string) bool {
	for _, w := range wanted {
		found := false
		for _, t := range tags {
			if t == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}