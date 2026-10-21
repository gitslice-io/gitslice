package storage

import (
	"strings"
	"unicode/utf8"
)

// MaxChangesetTitleRunes is the longest title a changeset keeps. A title is a
// subject line, like a commit's: what a list shows. Anything longer is the
// description's.
const MaxChangesetTitleRunes = 200

// NormalizeChangesetText turns what a client gave for a changeset's title and
// description into a one-line title and a description, so every client gets the
// same result however it sends a message:
//
//   - line endings are normalized to \n and surrounding blank space trimmed;
//   - a title of several lines is a subject and a body, as in a commit message:
//     the first line is the title, and the rest comes before the description;
//   - a first line longer than MaxChangesetTitleRunes is cut at a word, with
//     an ellipsis, and the whole line starts the description.
func NormalizeChangesetText(title, description string) (string, string) {
	title = strings.TrimSpace(strings.ReplaceAll(title, "\r\n", "\n"))
	description = strings.TrimSpace(strings.ReplaceAll(description, "\r\n", "\n"))
	subject, body, _ := strings.Cut(title, "\n")
	subject, body = strings.TrimSpace(subject), strings.TrimSpace(body)
	if utf8.RuneCountInString(subject) > MaxChangesetTitleRunes {
		runes := []rune(subject)
		cut := string(runes[:MaxChangesetTitleRunes-1])
		if space := strings.LastIndexAny(cut, " \t"); space > MaxChangesetTitleRunes/2 {
			cut = cut[:space]
		}
		body = strings.TrimSpace(subject + "\n\n" + body)
		subject = strings.TrimRight(cut, " \t.,;:") + "…"
	}
	return subject, joinParagraphs(body, description)
}

// CommitMessage is the message of the commit a changeset lands as: the title,
// and the description after a blank line, as Git reads a subject and a body.
func CommitMessage(title, description string) string {
	return joinParagraphs(strings.TrimSpace(title), strings.TrimSpace(description))
}

func joinParagraphs(first, second string) string {
	switch {
	case first == "":
		return second
	case second == "":
		return first
	}
	return first + "\n\n" + second
}
