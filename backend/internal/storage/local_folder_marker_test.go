package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"bkt/internal/validation"
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

// s3fs mkdir / "new folder" in Cyberduck or rclone / put-object --key dir/
// create a "dir/" object; it must coexist with objects inside the folder in
// either creation order.
func TestLocalFolderMarkerCoexistsWithContents(t *testing.T) {
	for _, markerFirst := range []bool{true, false} {
		ls := newTestLocal(t)
		_ = ls.CreateBucket("b", "")
		if markerFirst {
			putString(t, ls, "dir/", "")
			putString(t, ls, "dir/file.txt", "hello")
		} else {
			putString(t, ls, "dir/file.txt", "hello")
			putString(t, ls, "dir/", "")
		}
		putString(t, ls, "dir/sub/", "")

		if got := listKeys(t, ls, ""); len(got) != 3 || got[0] != "dir/" || got[1] != "dir/file.txt" || got[2] != "dir/sub/" {
			t.Fatalf("listing = %v", got)
		}
		if got := listKeys(t, ls, "dir/s"); len(got) != 1 || got[0] != "dir/sub/" {
			t.Errorf("prefix listing = %v", got)
		}
		if _, err := os.Stat(filepath.Join(ls.rootPath, "b", "dir", validation.LocalFolderMarkerName)); err != nil {
			t.Errorf("marker file not stored inside the folder: %v", err)
		}
		info, err := ls.GetObjectInfo("b", "dir/")
		if err != nil || info.Size != 0 || info.Key != "dir/" || info.ETag != "d41d8cd98f00b204e9800998ecf8427e" {
			t.Errorf("GetObjectInfo(dir/) = %+v, %v", info, err)
		}
		if ok, err := ls.ObjectExists("b", "dir/"); !ok || err != nil {
			t.Errorf("ObjectExists(dir/) = %v, %v", ok, err)
		}
		if got := readString(t, ls, "dir/"); got != "" {
			t.Errorf("marker body = %q", got)
		}

		// Deleting the marker never touches the folder's contents.
		if err := ls.DeleteObject("b", "dir/"); err != nil {
			t.Fatal(err)
		}
		if ok, _ := ls.ObjectExists("b", "dir/"); ok {
			t.Error("marker still exists after delete")
		}
		if got := readString(t, ls, "dir/file.txt"); got != "hello" {
			t.Errorf("folder content lost after marker delete: %q", got)
		}
		if ok, _ := ls.ObjectExists("b", "dir/sub/"); !ok {
			t.Error("nested marker lost after parent marker delete")
		}
	}
}

// A regular key whose path is a folder is not an object: reads report "not
// found" and deletes/archives never remove or move the folder.
func TestLocalRegularKeyNeverAddressesAFolder(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	putString(t, ls, "dir/", "")
	putString(t, ls, "dir/file.txt", "hello")

	if _, err := ls.GetObject("b", "dir"); err == nil {
		t.Error("GetObject(dir) opened the folder")
	}
	if ok, _ := ls.ObjectExists("b", "dir"); ok {
		t.Error("ObjectExists(dir) = true for a folder")
	}
	if _, err := ls.GetObjectInfo("b", "dir"); err == nil {
		t.Error("GetObjectInfo(dir) succeeded for a folder")
	}
	if err := ls.DeleteObject("b", "dir"); err != nil {
		t.Errorf("DeleteObject(dir) = %v", err)
	}
	if err := ls.ArchiveObjectVersion("b", "dir", testVID); err == nil {
		t.Error("ArchiveObjectVersion(dir) moved a folder into version storage")
	}
	if got := readString(t, ls, "dir/file.txt"); got != "hello" {
		t.Errorf("folder content affected: %q", got)
	}
}

func TestLocalFolderMarkerCopyAndVersions(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	putString(t, ls, "src/", "")
	putString(t, ls, "src/a", "A")

	// Copy (the first half of a move/rename) of a marker creates the new folder.
	if err := ls.CopyObject("b", "src/", "dst/"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := ls.ObjectExists("b", "dst/"); !ok {
		t.Error("copied marker missing")
	}
	if err := ls.CopyObject("b", "src/", "src/"); err != nil {
		t.Errorf("self-copy of marker: %v", err)
	}

	// Versioned overwrite/delete: archive, read, promote, delete version.
	if err := ls.ArchiveObjectVersion("b", "src/", testVID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := ls.ObjectExists("b", "src/"); ok {
		t.Error("marker still current after archive")
	}
	if got := readString(t, ls, "src/a"); got != "A" {
		t.Errorf("archive of marker touched folder contents: %q", got)
	}
	if _, err := os.Stat(filepath.Join(ls.rootPath, ".versions", "b", "src", validation.LocalFolderMarkerName, testVID)); err != nil {
		t.Errorf("marker version not stored under the marker name: %v", err)
	}
	rc, err := ls.GetObjectVersion("b", "src/", testVID)
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if err := ls.PromoteObjectVersion("b", "src/", testVID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := ls.ObjectExists("b", "src/"); !ok {
		t.Error("promoted marker missing")
	}
	if err := ls.ArchiveObjectVersion("b", "src/", testVID); err != nil {
		t.Fatal(err)
	}
	if err := ls.DeleteObjectVersion("b", "src/", testVID); err != nil {
		t.Fatal(err)
	}
	if _, err := ls.GetObjectVersion("b", "src/", testVID); err == nil {
		t.Error("deleted version still readable")
	}
}

// Before folder-marker support a "dir/" key was stored as the plain file
// "dir" (filepath.Join cleaned the trailing slash). Such legacy markers must
// stay readable and deletable, and a re-put upgrades them.
func TestLocalLegacyFolderMarker(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	legacy := filepath.Join(ls.rootPath, "b", "dir")
	if err := os.WriteFile(legacy, nil, 0600); err != nil {
		t.Fatal(err)
	}

	if ok, err := ls.ObjectExists("b", "dir/"); !ok || err != nil {
		t.Fatalf("legacy marker not found: %v %v", ok, err)
	}
	if info, err := ls.GetObjectInfo("b", "dir/"); err != nil || info.Size != 0 {
		t.Fatalf("legacy marker info = %+v, %v", info, err)
	}
	if got := readString(t, ls, "dir/"); got != "" {
		t.Errorf("legacy marker body = %q", got)
	}
	if err := ls.DeleteObject("b", "dir/"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("legacy marker file not removed by delete")
	}
	// The folder is usable now.
	putString(t, ls, "dir/file", "x")

	// A non-empty file at the folder path is a real object, never a marker.
	putString(t, ls, "data", "payload")
	if ok, _ := ls.ObjectExists("b", "data/"); ok {
		t.Error("non-empty file treated as a legacy marker")
	}
	if err := ls.DeleteObject("b", "data/"); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, ls, "data"); got != "payload" {
		t.Errorf("DeleteObject(data/) touched the object data: %q", got)
	}

	// Re-putting a legacy marker upgrades it to an in-folder marker.
	if err := os.WriteFile(filepath.Join(ls.rootPath, "b", "old"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	putString(t, ls, "old/", "")
	putString(t, ls, "old/child", "c")
	if got := listKeys(t, ls, "old"); len(got) != 2 || got[0] != "old/" || got[1] != "old/child" {
		t.Errorf("after upgrade listing = %v", got)
	}

	// Versioned delete of a legacy marker archives the legacy file.
	if err := os.WriteFile(filepath.Join(ls.rootPath, "b", "ver"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := ls.ArchiveObjectVersion("b", "ver/", testVID); err != nil {
		t.Fatalf("archive legacy marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ls.rootPath, "b", "ver")); !os.IsNotExist(err) {
		t.Error("legacy marker still current after archive")
	}
	if err := ls.PromoteObjectVersion("b", "ver/", testVID); err != nil {
		t.Fatalf("promote legacy marker version: %v", err)
	}
	if ok, _ := ls.ObjectExists("b", "ver/"); !ok {
		t.Error("promoted marker missing")
	}

	// Legacy archived marker versions (".versions/b/<dir>/<vid>") stay readable.
	const oldVID = "0b0c3b51-0000-4000-8000-0000000000bb"
	oldVer := filepath.Join(ls.rootPath, ".versions", "b", "gone", oldVID)
	_ = os.MkdirAll(filepath.Dir(oldVer), 0750)
	if err := os.WriteFile(oldVer, nil, 0600); err != nil {
		t.Fatal(err)
	}
	rc, err := ls.GetObjectVersion("b", "gone/", oldVID)
	if err != nil {
		t.Fatalf("legacy marker version unreadable: %v", err)
	}
	_ = rc.Close()
	if err := ls.DeleteObjectVersion("b", "gone/", oldVID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldVer); !os.IsNotExist(err) {
		t.Error("legacy marker version not deleted")
	}
}

func TestLocalFolderMarkerNameReserved(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	for _, k := range []string{validation.LocalFolderMarkerName, "dir/" + validation.LocalFolderMarkerName, validation.LocalFolderMarkerName + "/x"} {
		if err := ls.PutObject("b", k, bytes.NewReader(nil), 0, "", nil); err == nil {
			t.Errorf("PutObject(%q) accepted a reserved key", k)
		}
	}
	for _, k := range []string{"dir/", "a/b/", "dir/file", "x.bkt-folder"} {
		if err := checkLocalObjectKey("b", k); err != nil {
			t.Errorf("checkLocalObjectKey(%q) = %v", k, err)
		}
	}
	for _, k := range []string{".", "./", "a/.", "..", "a//", "a/b//", "/a/"} {
		if err := checkLocalObjectKey("b", k); err == nil {
			t.Errorf("checkLocalObjectKey(%q) accepted", k)
		}
	}
}

func TestLocalFolderMarkerMultipart(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	putString(t, ls, "mp/child", "c")
	id, err := ls.CreateMultipartUpload("b", "mp/", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	etag, err := ls.UploadPart("b", "mp/", id, 1, bytes.NewReader(nil), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := ls.CompleteMultipartUpload("b", "mp/", id, []CompletedPart{{PartNumber: 1, ETag: etag}}); err != nil {
		t.Fatal(err)
	}
	if got := listKeys(t, ls, "mp/"); len(got) != 2 || got[0] != "mp/" {
		t.Errorf("listing = %v", got)
	}
}
