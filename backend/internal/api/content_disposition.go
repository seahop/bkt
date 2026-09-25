package api

import (
	"mime"
	"strings"
)

// downloadFileName derives a download file name from an object key. Keys are
// opaque S3 strings ("../../etc/passwd", "a\\b", "dir/", control characters
// ...), so only the last segment is kept (split on both '/' and '\', ignoring
// trailing slashes), control characters are dropped, and names that would be
// empty or a path dot-segment fall back to "download". Browsers apply their
// own file-name sanitization on top of this.
func downloadFileName(key string) string {
	name := strings.TrimRight(key, "/\\")
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if strings.Trim(name, ". ") == "" {
		return "download"
	}
	return name
}

// attachmentDisposition is a Content-Disposition "attachment" header value
// naming the key's file. mime.FormatMediaType quotes/escapes the value and
// uses the RFC 2231 filename*=utf-8” form for non-ASCII names, so no key can
// break out of the header parameter.
func attachmentDisposition(key string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": downloadFileName(key)}); v != "" {
		return v
	}
	return "attachment"
}
