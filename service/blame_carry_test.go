package service

import (
	"fmt"
	"math/rand"
	"testing"

	"gitslice.io/gitslice/internal/diffutil"
	corev1 "gitslice.io/gitslice/proto/core/v1"
)

// backwardBlame is blame computed the direct way: for each line of the
// newest version, walk back until a version does not have it.
func backwardBlame(versions [][]string, commits []string) []string {
	newest := len(versions) - 1
	owner := make([]string, len(versions[newest]))
	pos := make([]int, len(owner))
	for i := range pos {
		pos[i] = i
	}
	for v := newest; v > 0; v-- {
		match := diffutil.MatchLines(versions[v-1], versions[v], 0)
		for i := range owner {
			if owner[i] != "" {
				continue
			}
			if j := match[pos[i]]; j >= 0 {
				pos[i] = j
			} else {
				owner[i] = commits[v]
			}
		}
	}
	for i := range owner {
		if owner[i] == "" {
			owner[i] = commits[0]
		}
	}
	return owner
}

// steps builds the walk's steps for versions[from+1..]: commit v turns
// versions[v-1] into versions[v]; version 0 is created by commit 0.
func blameSteps(versions [][]string, commits []string, from int) []blameStep {
	var steps []blameStep
	for v := len(versions) - 1; v >= from; v-- {
		step := blameStep{commit: &corev1.Commit{Id: commits[v]}, version: blameVersion{hash: fmt.Sprint(v), lines: versions[v]}}
		if v > 0 {
			step.parent = &blameVersion{hash: fmt.Sprint(v - 1), lines: versions[v-1]}
		}
		steps = append(steps, step)
	}
	return steps
}

func carryAll(owners []string, steps []blameStep) []string {
	for i := len(steps) - 1; i >= 0; i-- {
		owners = carryBlame(owners, steps[i])
	}
	return owners
}

func TestCarriedBlameMatchesTheWalkFromAnySnapshot(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 200; round++ {
		lines := make([]string, 5+rng.Intn(30))
		for i := range lines {
			lines[i] = fmt.Sprintf("l%d", rng.Intn(8))
		}
		versions := [][]string{lines}
		commits := []string{"c0"}
		for v := 1; v < 2+rng.Intn(12); v++ {
			next := append([]string(nil), versions[v-1]...)
			for e := 0; e < 1+rng.Intn(4); e++ {
				switch k := rng.Intn(3); {
				case k == 0 && len(next) > 1:
					at := rng.Intn(len(next))
					next = append(next[:at], next[at+1:]...)
				case k == 1:
					at := rng.Intn(len(next) + 1)
					next = append(next[:at], append([]string{fmt.Sprintf("n%d.%d", v, e)}, next[at:]...)...)
				default:
					next[rng.Intn(len(next))] = fmt.Sprintf("e%d.%d", v, e)
				}
			}
			versions = append(versions, next)
			commits = append(commits, fmt.Sprintf("c%d", v))
		}
		want := backwardBlame(versions, commits)
		// From the creation, and from a snapshot of every older version.
		if got := carryAll(nil, blameSteps(versions, commits, 0)); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("round %d from creation:\n got %v\nwant %v", round, got, want)
		}
		for snap := 0; snap < len(versions)-1; snap++ {
			base := backwardBlame(versions[:snap+1], commits[:snap+1])
			if got := carryAll(base, blameSteps(versions, commits, snap+1)); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("round %d from a snapshot of version %d:\n got %v\nwant %v", round, snap, got, want)
			}
		}
	}
}
