package storage

import (
	"strconv"
	"strings"

	corev1 "github.com/gitslice-io/gitslice/proto/core/v1"
)

func ChangesetHandle(ref *corev1.SliceRef, number int64) string {
	if ref == nil || ref.Account == "" || ref.Slice == "" || number <= 0 {
		return ""
	}
	return ref.Account + "/" + ref.Slice + "@" + strconv.FormatInt(number, 10)
}

func PatchsetHandle(ref *corev1.SliceRef, changesetNumber, patchsetNumber int64) string {
	changeset := ChangesetHandle(ref, changesetNumber)
	if changeset == "" || patchsetNumber <= 0 {
		return ""
	}
	return changeset + "." + strconv.FormatInt(patchsetNumber, 10)
}

func PopulateChangesetHandles(cs *corev1.Changeset) {
	if cs == nil {
		return
	}
	cs.Handle = ChangesetHandle(cs.AuthoringSlice, cs.Number)
	for _, patchset := range cs.Patchsets {
		if patchset == nil {
			continue
		}
		patchset.Handle = PatchsetHandle(cs.AuthoringSlice, cs.Number, patchset.Number)
	}
}

func ParseChangesetHandle(selector string) (account, slice string, number int64, ok bool) {
	selector = strings.TrimSpace(selector)
	if bang := strings.LastIndex(selector, "!"); bang > 0 {
		end := len(selector)
		if at := strings.Index(selector[bang+1:], "@"); at >= 0 {
			end = bang + 1 + at
		}
		return parseChangesetHandleParts(selector[:bang], selector[bang+1:end])
	}

	at := strings.LastIndex(selector, "@")
	if at <= 0 || at == len(selector)-1 {
		return "", "", 0, false
	}
	numberPart := selector[at+1:]
	if dot := strings.Index(numberPart, "."); dot >= 0 {
		numberPart = numberPart[:dot]
	}
	return parseChangesetHandleParts(selector[:at], numberPart)
}

func parseChangesetHandleParts(ref, numberPart string) (account, slice string, number int64, ok bool) {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", 0, false
	}
	n, err := strconv.ParseInt(numberPart, 10, 64)
	if err != nil || n <= 0 {
		return "", "", 0, false
	}
	return parts[0], parts[1], n, true
}

func ParsePatchsetHandle(selector string) (account, slice string, changesetNumber, patchsetNumber int64, ok bool) {
	selector = strings.TrimSpace(selector)
	if bang := strings.LastIndex(selector, "!"); bang > 0 {
		if at := strings.LastIndex(selector, "@"); at > bang && at < len(selector)-1 {
			account, slice, changesetNumber, ok = ParseChangesetHandle(selector[:at])
			if !ok {
				return "", "", 0, 0, false
			}
			n, err := strconv.ParseInt(selector[at+1:], 10, 64)
			if err != nil || n <= 0 {
				return "", "", 0, 0, false
			}
			return account, slice, changesetNumber, n, true
		}
	}

	at := strings.LastIndex(selector, "@")
	if at <= 0 || at == len(selector)-1 {
		return "", "", 0, 0, false
	}
	version := selector[at+1:]
	dot := strings.LastIndex(version, ".")
	if dot <= 0 || dot == len(version)-1 {
		return "", "", 0, 0, false
	}
	account, slice, changesetNumber, ok = parseChangesetHandleParts(selector[:at], version[:dot])
	if !ok {
		return "", "", 0, 0, false
	}
	n, err := strconv.ParseInt(version[dot+1:], 10, 64)
	if err != nil || n <= 0 {
		return "", "", 0, 0, false
	}
	return account, slice, changesetNumber, n, true
}
