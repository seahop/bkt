package api

import (
	"mime"
	"strings"
	"testing"
)

func TestDownloadFileName(t *testing.T) {
	cases := map[string]string{
		"a/b/report.pdf":   "report.pdf",
		"report.pdf":       "report.pdf",
		"dir/":             "dir",
		"../../etc/passwd": "passwd",
		"/leading":         "leading",
		"a\\b":             "b",
		"..":               "download",
		"a/..":             "download",
		"./":               "download",
		"//":               "download",
		"x/\r\nSet-Cookie": "Set-Cookie",
		"ünï/😀.txt":        "😀.txt",
		"a\"b;c.txt":       "a\"b;c.txt",
	}
	for key, want := range cases {
		if got := downloadFileName(key); got != want {
			t.Errorf("downloadFileName(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestAttachmentDispositionRoundTrips(t *testing.T) {
	for _, key := range []string{"a/report.pdf", "a\"b;c.txt", "ünï/😀.txt", "x/\r\nevil", "../x y.txt", "dir/"} {
		v := attachmentDisposition(key)
		if strings.ContainsAny(v, "\r\n") {
			t.Fatalf("header value for %q contains CR/LF: %q", key, v)
		}
		typ, params, err := mime.ParseMediaType(v)
		if err != nil || typ != "attachment" {
			t.Fatalf("attachmentDisposition(%q) = %q: unparsable (%v)", key, v, err)
		}
		if params["filename"] != downloadFileName(key) {
			t.Errorf("attachmentDisposition(%q) filename = %q, want %q", key, params["filename"], downloadFileName(key))
		}
	}
}
