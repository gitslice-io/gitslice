// Package checks parses committed check definition files and resolves the checks
// that apply to a patchset.
//
// This package operates only in the logical repository-root path space used by
// checks.yaml files and check ids. Root is represented as "/", while other paths
// are slash-separated repository-relative paths such as "backend" or
// "backend/test". It intentionally does not map those logical paths to
// account-rooted canonical storage paths; that adapter belongs to the server
// integration layer.
package checks
