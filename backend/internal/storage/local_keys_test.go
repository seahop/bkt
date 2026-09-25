package storage

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const testVID = "0b0c3b51-0000-4000-8000-0000000000aa"

func putString(t *testing.T, ls *LocalStorage, key, s string) {
	t.Helper()
	if err := ls.PutObject("b", key, bytes.NewReader([]byte(s)), int64(len(s)), "", nil); err != nil {
		t.Fatalf("PutObject(%q): %v", key, err)
	}
}

func readString(t *testing.T, ls *LocalStorage, key string) string {
	t.Helper()
	rc, err := ls.GetObject("b", key)
	if err != nil {
		t.Fatalf("GetObject(%q): %v", key, err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return string(b)
}

func readVersion(t *testing.T, ls *LocalStorage, key, vid string) string {
	t.Helper()
	rc, err := ls.GetObjectVersion("b", key, vid)
	if err != nil {
		t.Fatalf("GetObjectVersion(%q, %s): %v", key, vid, err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return string(b)
}

func listKeys(t *testing.T, ls *LocalStorage, prefix string) []string {
	t.Helper()
	objs, err := ls.ListObjects("b", prefix)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.Key)
	}
	sort.Strings(keys)
	return keys
}

func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// compatKeys are valid S3 keys that the key-as-path layout could not store
// (or aliased onto other keys).
func compatKeys() []string {
	return []string{
		"p", "p/q",
		"m/", "m",
		strings.Repeat("s", 300),
		"dir/" + strings.Repeat("L", 300) + "/leaf",
		"a..b.txt", "../x", "/leading", "a\\b", "a//b", "./x",
		"ünï/😀", "dir/.bkt-folder", ".bkt-folder", ".tmp-upload-123", "x/.key",
		strings.Repeat("k", 1024),
		strings.Repeat("é", 512), // 1024 bytes of two-byte runes
	}
}

// "p" and "p/q" (and "m/" and "m") coexist in either creation order with
// independent contents.
func TestLocalPrefixKeysCoexist(t *testing.T) {
	for _, pair := range [][2]string{{"p", "p/q"}, {"p/q", "p"}, {"m/", "m"}, {"m", "m/"}, {"d/", "d/f"}, {"d/f", "d/"}} {
		ls := newTestLocal(t)
		putString(t, ls, pair[0], "first:"+pair[0])
		putString(t, ls, pair[1], "second:"+pair[1])
		if got := readString(t, ls, pair[0]); got != "first:"+pair[0] {
			t.Errorf("%v: %q = %q", pair, pair[0], got)
		}
		if got := readString(t, ls, pair[1]); got != "second:"+pair[1] {
			t.Errorf("%v: %q = %q", pair, pair[1], got)
		}
		want := []string{pair[0], pair[1]}
		sort.Strings(want)
		if got := listKeys(t, ls, ""); !equalKeys(got, want) {
			t.Errorf("%v: listing = %q", pair, got)
		}
		// Deleting one never affects the other.
		if err := ls.DeleteObject("b", pair[0]); err != nil {
			t.Fatal(err)
		}
		if ok, _ := ls.ObjectExists("b", pair[0]); ok {
			t.Errorf("%v: %q survived delete", pair, pair[0])
		}
		if got := readString(t, ls, pair[1]); got != "second:"+pair[1] {
			t.Errorf("%v: %q after deleting %q = %q", pair, pair[1], pair[0], got)
		}
	}
}

// Every compat key round-trips through every storage operation.
func TestLocalArbitraryKeysRoundTrip(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	keys := compatKeys()
	for i, k := range keys {
		body := fmt.Sprintf("body-%02d-%s", i, k)
		putString(t, ls, k, body)

		if got := readString(t, ls, k); got != body {
			t.Errorf("GetObject(%q) = %q", k, got)
		}
		rc, err := GetObjectRange(ls, "b", k, 5, 2)
		if err != nil {
			t.Fatalf("GetObjectRange(%q): %v", k, err)
		}
		got, _ := io.ReadAll(rc)
		_ = rc.Close()
		if string(got) != body[5:7] {
			t.Errorf("GetObjectRange(%q) = %q, want %q", k, got, body[5:7])
		}
		info, err := ls.GetObjectInfo("b", k)
		if err != nil || info.Key != k || info.Size != int64(len(body)) || len(info.ETag) != 32 {
			t.Errorf("GetObjectInfo(%q) = %+v, %v", k, info, err)
		}
		if ok, err := ls.ObjectExists("b", k); !ok || err != nil {
			t.Errorf("ObjectExists(%q) = %v, %v", k, ok, err)
		}
	}
	want := append([]string(nil), keys...)
	sort.Strings(want)
	if got := listKeys(t, ls, ""); !equalKeys(got, want) {
		t.Fatalf("listing = %q\nwant %q", got, want)
	}
	if got := listKeys(t, ls, "p"); !equalKeys(got, []string{"p", "p/q"}) {
		t.Errorf("prefix listing p = %q", got)
	}
	if got := listKeys(t, ls, "../"); !equalKeys(got, []string{"../x"}) {
		t.Errorf("prefix listing ../ = %q", got)
	}

	for i, k := range keys {
		body := fmt.Sprintf("body-%02d-%s", i, k)
		// Copy onto a sibling key and onto another compat key's prefix.
		dst := k + "#copy"
		if err := ls.CopyObject("b", k, dst); err != nil {
			t.Fatalf("CopyObject(%q): %v", k, err)
		}
		if got := readString(t, ls, dst); got != body {
			t.Errorf("copy of %q = %q", k, got)
		}
		if err := ls.DeleteObject("b", dst); err != nil {
			t.Fatal(err)
		}

		// Versions: archive, read, promote, re-archive, delete.
		vid := fmt.Sprintf("0b0c3b51-0000-4000-8000-%012d", i)
		if err := ls.ArchiveObjectVersion("b", k, vid); err != nil {
			t.Fatalf("ArchiveObjectVersion(%q): %v", k, err)
		}
		if ok, _ := ls.ObjectExists("b", k); ok {
			t.Errorf("%q still current after archive", k)
		}
		if got := readVersion(t, ls, k, vid); got != body {
			t.Errorf("version of %q = %q", k, got)
		}
		if err := ls.PromoteObjectVersion("b", k, vid); err != nil {
			t.Fatalf("PromoteObjectVersion(%q): %v", k, err)
		}
		if got := readString(t, ls, k); got != body {
			t.Errorf("promoted %q = %q", k, got)
		}
		if err := ls.ArchiveObjectVersion("b", k, vid); err != nil {
			t.Fatal(err)
		}
		if err := ls.DeleteObjectVersion("b", k, vid); err != nil {
			t.Fatal(err)
		}
		if _, err := ls.GetObjectVersion("b", k, vid); err == nil {
			t.Errorf("deleted version of %q still readable", k)
		}

		// Multipart complete.
		id, err := ls.CreateMultipartUpload("b", k, "", nil)
		if err != nil {
			t.Fatalf("CreateMultipartUpload(%q): %v", k, err)
		}
		if _, err := ls.UploadPart("b", k, id, 1, strings.NewReader("mp1-"), 4); err != nil {
			t.Fatal(err)
		}
		if _, err := ls.UploadPart("b", k, id, 2, strings.NewReader(k), int64(len(k))); err != nil {
			t.Fatal(err)
		}
		if err := ls.CompleteMultipartUpload("b", k, id, []CompletedPart{{PartNumber: 1}, {PartNumber: 2}}); err != nil {
			t.Fatalf("CompleteMultipartUpload(%q): %v", k, err)
		}
		if got := readString(t, ls, k); got != "mp1-"+k {
			t.Errorf("multipart %q = %q", k, got)
		}
	}
	if got := listKeys(t, ls, ""); !equalKeys(got, want) {
		t.Errorf("listing after ops = %q", got)
	}

	for _, k := range keys {
		if err := ls.DeleteObject("b", k); err != nil {
			t.Fatal(err)
		}
	}
	if got := listKeys(t, ls, ""); len(got) != 0 {
		t.Errorf("listing after delete = %q", got)
	}
	// Deleted keys leave no files behind (only fan-out directories).
	assertNoFiles(t, filepath.Join(ls.rootPath, ".objects", "b"))
	assertNoFiles(t, filepath.Join(ls.rootPath, ".objversions", "b"))
}

func assertNoFiles(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("leftover file %s", p)
		}
		return nil
	})
}

// A folder marker "dir/" is an ordinary key: deleting or archiving it never
// touches "dir/..." objects, and a regular key "dir" is independent of it.
func TestLocalFolderMarkerIsAnOrdinaryKey(t *testing.T) {
	ls := newTestLocal(t)
	putString(t, ls, "dir/", "")
	putString(t, ls, "dir/file.txt", "hello")
	info, err := ls.GetObjectInfo("b", "dir/")
	if err != nil || info.Size != 0 || info.ETag != "d41d8cd98f00b204e9800998ecf8427e" || info.ContentType != "application/octet-stream" {
		t.Errorf("GetObjectInfo(dir/) = %+v, %v", info, err)
	}
	if ok, _ := ls.ObjectExists("b", "dir"); ok {
		t.Error("ObjectExists(dir) = true without such an object")
	}
	if err := ls.ArchiveObjectVersion("b", "dir", testVID); err == nil {
		t.Error("archiving a missing object succeeded")
	}
	if err := ls.ArchiveObjectVersion("b", "dir/", testVID); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, ls, "dir/file.txt"); got != "hello" {
		t.Errorf("folder content affected by marker archive: %q", got)
	}
	if err := ls.PromoteObjectVersion("b", "dir/", testVID); err != nil {
		t.Fatal(err)
	}
	if err := ls.DeleteObject("b", "dir/"); err != nil {
		t.Fatal(err)
	}
	if got := listKeys(t, ls, ""); !equalKeys(got, []string{"dir/file.txt"}) {
		t.Errorf("listing = %q", got)
	}
	// Promoting a missing version fails cleanly and creates nothing.
	if err := ls.PromoteObjectVersion("b", "gone", testVID); err == nil {
		t.Error("promoting a missing version succeeded")
	}
	if ok, _ := ls.ObjectExists("b", "gone"); ok {
		t.Error("failed promote created an object")
	}
}

func TestLocalListingSkipsInternalFiles(t *testing.T) {
	ls := newTestLocal(t)
	putString(t, ls, "a", "1")
	loc, _ := ls.objectLocation("b", "a")
	// Interrupted writes, the layout marker, an orphan blob without sidecar
	// and a blob whose sidecar names another key are never listed.
	for name, content := range map[string]string{
		".tmp-upload-1":   "partial",
		".tmp-assemble-2": "partial",
		".tmp-key-3":      "a",
		keyHash("orphan"): "no sidecar",
	} {
		if err := os.WriteFile(filepath.Join(loc.dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bad := objectLocIn(filepath.Join(ls.rootPath, ".objects", "b"), "mismatch")
	_ = os.MkdirAll(bad.dir, 0o750)
	_ = os.WriteFile(bad.blob, []byte("x"), 0o600)
	_ = os.WriteFile(bad.sidecar, []byte("not-mismatch"), 0o600)
	_ = os.WriteFile(filepath.Join(ls.rootPath, ".objects", "b", layoutMarkerName), []byte("v2"), 0o600)
	if got := listKeys(t, ls, ""); !equalKeys(got, []string{"a"}) {
		t.Errorf("listing = %q", got)
	}
}
