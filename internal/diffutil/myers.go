package diffutil

import "math"

func myersMaxD(maxCells int) int {
	if maxCells <= 0 {
		return 0
	}
	return int(math.Sqrt(float64(maxCells)))
}

// myersEditDistance computes the exact minimum insert/delete edit distance
// between a and b in O((len(a)+len(b))*D) time and O(maxD) memory. It returns
// (0, false) if the edit distance exceeds maxD.
func myersEditDistance(a, b []string, maxD int) (int, bool) {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return 0, true
	}
	if maxD <= 0 {
		return 0, false
	}
	if diff := n - m; diff > maxD || -diff > maxD {
		return 0, false
	}
	if limit := n + m; maxD > limit {
		maxD = limit
	}
	offset := maxD
	v := make([]int, 2*maxD+1)
	for d := 0; d <= maxD; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				return d, true
			}
		}
	}
	return 0, false
}

// myersDiff computes an exact minimal line diff between a and b in
// O((len(a)+len(b))*D) time and O(D^2) memory. It returns (nil, false) when
// the edit distance exceeds maxD.
func myersDiff(a, b []string, maxD int) ([]string, bool) {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil, true
	}
	if maxD <= 0 {
		return nil, false
	}
	if diff := n - m; diff > maxD || -diff > maxD {
		return nil, false
	}
	if limit := n + m; maxD > limit {
		maxD = limit
	}
	offset := maxD
	v := make([]int, 2*maxD+1)
	trace := make([][]int, 0, min(maxD+1, 64))
	foundD := -1
	for d := 0; d <= maxD; d++ {
		// Snapshot only the active [-d, d] slice of v (2*d+1 ints) before
		// mutating v for step d.
		snap := make([]int, 2*d+1)
		copy(snap, v[offset-d:offset+d+1])
		trace = append(trace, snap)

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				foundD = d
				break
			}
		}
		if foundD >= 0 {
			break
		}
	}
	if foundD < 0 {
		return nil, false
	}

	// Backtrack from (n, m) to (0, 0).
	rev := make([]string, 0, n+m)
	x, y := n, m
	for d := foundD; d > 0; d-- {
		prev := trace[d]
		k := x - y
		// In trace[d], index i corresponds to diagonal i - d.
		var prevK int
		if k == -d || (k != d && prev[d+k-1] < prev[d+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := prev[d+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, " "+a[x-1])
			x--
			y--
		}
		if x == prevX {
			rev = append(rev, "+"+b[y-1])
			y--
		} else {
			rev = append(rev, "-"+a[x-1])
			x--
		}
	}
	for x > 0 && y > 0 {
		rev = append(rev, " "+a[x-1])
		x--
		y--
	}
	// Reverse in place.
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, true
}
