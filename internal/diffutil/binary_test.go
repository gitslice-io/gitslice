package diffutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUnifiedFileDiffBinary(t *testing.T) {
	tests := []struct {
		name string
		old  File
		new  File
		want string
	}{
		{
			name: "new file",
			old:  File{Path: "/x.png"},
			new:  File{Path: "/x.png", Exists: true, Data: []byte{'P', 'N', 'G', 0, 1}},
			want: "diff --git a/x.png b/x.png\nnew file mode 100644\nBinary files /dev/null and b/x.png differ\n",
		},
		{
			name: "deleted file",
			old:  File{Path: "/x.png", Exists: true, Data: []byte{'P', 'N', 'G', 0, 1}},
			new:  File{Path: "/x.png"},
			want: "diff --git a/x.png b/x.png\ndeleted file mode 100644\nBinary files a/x.png and /dev/null differ\n",
		},
		{
			name: "modified file",
			old:  File{Path: "/x", Exists: true, Data: []byte{'a', 0}},
			new:  File{Path: "/x", Exists: true, Data: []byte{'b', 0}},
			want: "diff --git a/x b/x\nBinary files a/x and b/x differ\n",
		},
		{
			name: "text to binary",
			old:  File{Path: "/x", Exists: true, Data: []byte("text\n")},
			new:  File{Path: "/x", Exists: true, Data: []byte{'b', 0}},
			want: "diff --git a/x b/x\nBinary files a/x and b/x differ\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UnifiedFileDiff(tt.old, tt.new)
			if got != tt.want {
				t.Fatalf("UnifiedFileDiff() mismatch\ngot:  %q\nwant: %q", got, tt.want)
			}
			if strings.Contains(got, "--- ") || strings.Contains(got, "+++ ") || strings.Contains(got, "@@ ") {
				t.Fatalf("binary diff contains text diff lines:\n%s", got)
			}
		})
	}
}

func TestUnifiedFileDiffInvalidUTF8TextIsSanitized(t *testing.T) {
	got := UnifiedFileDiff(
		File{Path: "/latin1.txt", Exists: true, Data: []byte("plain\n")},
		File{Path: "/latin1.txt", Exists: true, Data: []byte{'c', 'a', 'f', 0xe9, '\n'}},
	)

	if !utf8.ValidString(got) {
		t.Fatalf("diff is not valid UTF-8: %q", got)
	}
	if !strings.ContainsRune(got, '�') {
		t.Fatalf("diff does not contain replacement rune: %q", got)
	}
}

func TestUnifiedFileDiffUnchangedBinaryIsEmpty(t *testing.T) {
	data := []byte{'a', 0, 'b'}
	got := UnifiedFileDiff(
		File{Path: "/x", Exists: true, Data: data},
		File{Path: "/x", Exists: true, Data: data},
	)
	if got != "" {
		t.Fatalf("unchanged binary diff = %q, want empty", got)
	}
}

func TestUnifiedFileDiffPlainTextRegression(t *testing.T) {
	got := UnifiedFileDiff(
		File{Path: "/x.txt", Exists: true, Data: []byte("a\nb\n")},
		File{Path: "/x.txt", Exists: true, Data: []byte("a\nc\n")},
	)
	want := "diff --git a/x.txt b/x.txt\n--- a/x.txt\n+++ b/x.txt\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n"
	if got != want {
		t.Fatalf("UnifiedFileDiff() mismatch\ngot:  %q\nwant: %q", got, want)
	}
}
