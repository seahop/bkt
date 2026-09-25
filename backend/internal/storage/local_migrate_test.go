package storage

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	mvid1 = "11111111-0000-4000-8000-000000000001"
	mvid2 = "11111111-0000-4000-8000-000000000002"
	mvid3 = "11111111-0000-4000-8000-000000000003"
	mvid4 = "11111111-0000-4000-8000-000000000004"
	mvid5 = "11111111-0000-4000-8000-000000000005"
)

// writeTree creates files (path relative to root -> content).
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func bucketKeys(t *testing.T, ls *LocalStorage, bucket string) []string {
	t.Helper()
	objs, err := ls.ListObjects(bucket, "")
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(objs))
	for _, o := range objs {
		keys = append(keys, o.Key)
	}
	return keys
}

func wantObject(t *testing.T, ls *LocalStorage, bucket, key, want string) {
	t.Helper()
	rc, err := ls.GetObject(bucket, key)
	if err != nil {
		t.Errorf("GetObject(%s, %q): %v", bucket, key, err)
		return
	}
	defer rc.Close()
	got := make([]byte, len(want)+16)
	n, _ := rc.Read(got)
	if string(got[:n]) != want {
		t.Errorf("GetObject(%s, %q) = %q, want %q", bucket, key, got[:n], want)
	}
}

func wantVersion(t *testing.T, ls *LocalStorage, bucket, key, vid, want string) {
	t.Helper()
	rc, err := ls.GetObjectVersion(bucket, key, vid)
	if err != nil {
		t.Errorf("GetObjectVersion(%s, %q, %s): %v", bucket, key, vid, err)
		return
	}
	defer rc.Close()
	got := make([]byte, len(want)+16)
	n, _ := rc.Read(got)
	if string(got[:n]) != want {
		t.Errorf("GetObjectVersion(%s, %q, %s) = %q, want %q", bucket, key, vid, got[:n], want)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// legacyFixture is a legacy (key-as-path) storage root with every shape the
// previous releases wrote.
func legacyFixture(t *testing.T) string {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		// bucket "bkt-a": objects incl. nested, markers, temp files
		"bkt-a/top.txt":                 "top",
		"bkt-a/a/b/c.txt":               "abc",
		"bkt-a/a/x.txt":                 "ax",
		"bkt-a/dir/.bkt-folder":         "",
		"bkt-a/dir/file":                "f",
		"bkt-a/dir/sub/.bkt-folder":     "",
		"bkt-a/emptymarker":             "", // older-release marker "emptymarker/" == object "emptymarker"
		"bkt-a/.bkt-folder":             "root-level object .bkt-folder",
		"bkt-a/ünï/😀 x.txt":             "unicode",
		"bkt-a/a/.tmp-upload-123":       "partial upload",
		"bkt-a/deep/er/.tmp-assemble-9": "partial assembly",
		// versions of "bkt-a", all shapes
		".versions/bkt-a/a/b/c.txt/" + mvid1:       "abc-v1",
		".versions/bkt-a/top.txt/" + mvid2:         "top-v1",
		".versions/bkt-a/dir/.bkt-folder/" + mvid3: "",
		".versions/bkt-a/old/" + mvid4:             "", // older-release marker version == versions of "old"
		".versions/bkt-a/bad/not-a-uuid":           "?",
		// bucket "clean": nothing unmappable, legacy dirs must disappear
		"clean/k1":                      "one",
		"clean/d/k2":                    "two",
		"clean/d/.bkt-folder":           "",
		".versions/clean/d/k2/" + mvid1: "two-v1",
		// a bucket that only has versions left
		".versions/vonly/gone/" + mvid5: "gone-v1",
		// not buckets: left alone
		"lost+found/x":                        "fsck",
		"Not_A_Bucket/y":                      "operator file",
		".multipart/" + mvid1 + "/part.00001": "staged",
		".multipart/" + mvid1 + "/meta.json":  "{}",
	})
	return root
}

func TestMigrateLocalLayout(t *testing.T) {
	root := legacyFixture(t)
	st, err := migrateLocalLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Buckets != 3 || st.Objects != 12 || st.Versions != 6 || st.SkippedTemp != 2 || st.Unmappable != 1 ||
		st.Conflicts != 0 || st.Duplicates != 0 {
		t.Errorf("stats = %+v", st)
	}
	ls := NewLocalStorage(root)

	// "emptymarker" is a zero-length legacy file (possibly an old-style marker
	// "emptymarker/"), so both keys exist after migration.
	wantBK := []string{".bkt-folder", "a/b/c.txt", "a/x.txt", "dir/", "dir/file", "dir/sub/", "emptymarker", "emptymarker/", "top.txt", "ünï/😀 x.txt"}
	if got := bucketKeys(t, ls, "bkt-a"); !equalKeys(got, wantBK) {
		t.Errorf("bkt-a keys = %q\nwant %q", got, wantBK)
	}
	for key, want := range map[string]string{
		"top.txt": "top", "a/b/c.txt": "abc", "a/x.txt": "ax", "dir/": "", "dir/file": "f", "dir/sub/": "",
		"emptymarker": "", ".bkt-folder": "root-level object .bkt-folder", "ünï/😀 x.txt": "unicode",
	} {
		wantObject(t, ls, "bkt-a", key, want)
	}
	wantVersion(t, ls, "bkt-a", "a/b/c.txt", mvid1, "abc-v1")
	wantVersion(t, ls, "bkt-a", "top.txt", mvid2, "top-v1")
	wantVersion(t, ls, "bkt-a", "dir/", mvid3, "")
	wantVersion(t, ls, "bkt-a", "old", mvid4, "")
	wantVersion(t, ls, "clean", "d/k2", mvid1, "two-v1")
	wantVersion(t, ls, "vonly", "gone", mvid5, "gone-v1")
	if got := bucketKeys(t, ls, "clean"); !equalKeys(got, []string{"d/", "d/k2", "k1"}) {
		t.Errorf("clean keys = %q", got)
	}
	wantObject(t, ls, "clean", "d/k2", "two")

	// Migrated objects behave like native ones (versions promote, delete).
	if err := ls.PromoteObjectVersion("bkt-a", "old", mvid4); err != nil {
		t.Errorf("promote migrated version: %v", err)
	}
	if err := ls.DeleteObjectVersion("bkt-a", "a/b/c.txt", mvid1); err != nil {
		t.Error(err)
	}

	// Legacy dirs: fully migrated ones are gone; leftovers (temp files, the
	// unmappable version file) keep only their own directories.
	for _, p := range []string{"clean", ".versions/clean", ".versions/vonly", "bkt-a/dir", "bkt-a/a/b", ".versions/bkt-a/a"} {
		if exists(filepath.Join(root, p)) {
			t.Errorf("legacy %s not removed", p)
		}
	}
	for _, p := range []string{"bkt-a/a/.tmp-upload-123", "bkt-a/deep/er/.tmp-assemble-9", ".versions/bkt-a/bad/not-a-uuid"} {
		if !exists(filepath.Join(root, p)) {
			t.Errorf("leftover %s was removed", p)
		}
	}
	for _, p := range []string{"lost+found/x", "Not_A_Bucket/y", ".multipart/" + mvid1 + "/part.00001"} {
		if !exists(filepath.Join(root, p)) {
			t.Errorf("non-bucket data %s was touched", p)
		}
	}
	for _, b := range []string{"bkt-a", "clean", "vonly"} {
		if !exists(filepath.Join(root, ".objects", b, layoutMarkerName)) {
			t.Errorf("no completion marker for %s", b)
		}
	}
	if exists(filepath.Join(root, ".objects", "lost+found")) || exists(filepath.Join(root, ".objects", "Not_A_Bucket")) {
		t.Error("non-bucket directory was migrated")
	}

	// Idempotent: a second run changes nothing.
	before := bucketKeys(t, ls, "bkt-a")
	putString(t, ls, "after", "new object") // bucket "b", native
	st, err = migrateLocalLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if st != (layoutMigrationStats{}) {
		t.Errorf("second run stats = %+v, want zero", st)
	}
	if got := bucketKeys(t, ls, "bkt-a"); !equalKeys(got, before) {
		t.Errorf("bkt-a keys after second run = %q, want %q", got, before)
	}
	wantObject(t, ls, "b", "after", "new object")
	if !exists(filepath.Join(root, "bkt-a/a/.tmp-upload-123")) {
		t.Error("second run touched leftovers")
	}
}

func TestMigrateLocalLayoutNothingToDo(t *testing.T) {
	root := t.TempDir()
	if st, err := migrateLocalLayout(root); err != nil || st != (layoutMigrationStats{}) {
		t.Fatalf("empty root: %+v, %v", st, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("migration of an empty root created %v", entries)
	}
	// A root already in the blob layout is left alone too.
	ls := NewLocalStorage(root)
	putString(t, ls, "k", "v")
	if st, err := migrateLocalLayout(root); err != nil || st != (layoutMigrationStats{}) {
		t.Fatalf("blob-layout root: %+v, %v", st, err)
	}
	if err := MigrateLocalLayout(filepath.Join(root, "missing")); err != nil {
		t.Errorf("missing root: %v", err)
	}
}

// A crash part-way through: some files already moved, one moved as a hard
// link whose legacy name was not yet unlinked, one with only its sidecar
// written. Re-running finishes the job without loss or duplication.
func TestMigrateLocalLayoutResumesAfterCrash(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{}
	for i, k := range []string{"k0", "k1", "k2", "d/k3", "d/k4", "d/e/k5", "k6", "k7"} {
		files["crash/"+k] = "content-" + k + string(rune('a'+i))
	}
	files[".versions/crash/k0/"+mvid1] = "k0-v1"
	files[".versions/crash/k1/"+mvid2] = "k1-v1"
	writeTree(t, root, files)
	ls := NewLocalStorage(root)
	objRoot := filepath.Join(root, ".objects", "crash")

	// k0: fully moved by the interrupted run.
	loc0 := objectLocIn(objRoot, "k0")
	_ = os.MkdirAll(loc0.dir, 0o750)
	_ = writeSmallFileAtomic(loc0.dir, loc0.sidecar, "k0")
	if err := os.Rename(filepath.Join(root, "crash/k0"), loc0.blob); err != nil {
		t.Fatal(err)
	}
	// k1: linked, legacy name not yet removed (crash inside renameNoReplace).
	loc1 := objectLocIn(objRoot, "k1")
	_ = os.MkdirAll(loc1.dir, 0o750)
	_ = writeSmallFileAtomic(loc1.dir, loc1.sidecar, "k1")
	if err := os.Link(filepath.Join(root, "crash/k1"), loc1.blob); err != nil {
		t.Fatal(err)
	}
	// d/k3: sidecar written, blob not moved yet.
	loc3 := objectLocIn(objRoot, "d/k3")
	_ = os.MkdirAll(loc3.dir, 0o750)
	_ = writeSmallFileAtomic(loc3.dir, loc3.sidecar, "d/k3")
	// k0's version: already moved.
	v0 := filepath.Join(root, ".objversions", "crash", keyHash("k0"))
	_ = os.MkdirAll(v0, 0o750)
	_ = writeSmallFileAtomic(v0, filepath.Join(v0, ".key"), "k0")
	if err := os.Rename(filepath.Join(root, ".versions/crash/k0/"+mvid1), filepath.Join(v0, mvid1)); err != nil {
		t.Fatal(err)
	}

	st, err := migrateLocalLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Objects != 6 || st.Duplicates != 1 || st.Versions != 1 || st.leftovers() != 0 {
		t.Errorf("stats = %+v", st)
	}
	var want []string
	for rel, content := range files {
		if key, ok := strings.CutPrefix(rel, "crash/"); ok {
			want = append(want, key)
			wantObject(t, ls, "crash", key, content)
		}
	}
	sort.Strings(want)
	if got := bucketKeys(t, ls, "crash"); !equalKeys(got, want) {
		t.Errorf("keys = %q, want %q", got, want)
	}
	wantVersion(t, ls, "crash", "k0", mvid1, "k0-v1")
	wantVersion(t, ls, "crash", "k1", mvid2, "k1-v1")
	for _, p := range []string{"crash", ".versions"} {
		if exists(filepath.Join(root, p)) {
			t.Errorf("legacy %s not removed", p)
		}
	}
}

// A key present in both layouts: identical bytes drop the legacy copy,
// different bytes keep both (the blob layout wins, nothing is lost).
func TestMigrateLocalLayoutDuplicates(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"dup/same":                    "same bytes",
		"dup/diff":                    "OLD bytes",
		"dup/only-legacy":             "legacy",
		".versions/dup/same/" + mvid1: "v-same",
		".versions/dup/diff/" + mvid2: "v-OLD",
	})
	ls := NewLocalStorage(root)
	for key, content := range map[string]string{"same": "same bytes", "diff": "NEW bytes"} {
		if err := ls.PutObject("dup", key, strings.NewReader(content), int64(len(content)), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	// Versions in the blob layout under the same ids.
	for key, pair := range map[string][2]string{"same": {mvid1, "v-same"}, "diff": {mvid2, "v-NEW"}} {
		vdir, vfile, _ := ls.versionLocation("dup", key, pair[0])
		_ = os.MkdirAll(vdir, 0o750)
		_ = writeSmallFileAtomic(vdir, filepath.Join(vdir, ".key"), key)
		if err := os.WriteFile(vfile, []byte(pair[1]), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	st, err := migrateLocalLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if st.Duplicates != 2 || st.Conflicts != 2 || st.Objects != 1 || st.Versions != 0 {
		t.Errorf("stats = %+v", st)
	}
	wantObject(t, ls, "dup", "same", "same bytes")
	wantObject(t, ls, "dup", "diff", "NEW bytes")
	wantObject(t, ls, "dup", "only-legacy", "legacy")
	wantVersion(t, ls, "dup", "same", mvid1, "v-same")
	wantVersion(t, ls, "dup", "diff", mvid2, "v-NEW")
	if exists(filepath.Join(root, "dup/same")) || exists(filepath.Join(root, ".versions/dup/same")) {
		t.Error("identical legacy copies not removed")
	}
	for _, p := range []string{"dup/diff", ".versions/dup/diff/" + mvid2} {
		if !exists(filepath.Join(root, p)) {
			t.Errorf("conflicting legacy copy %s was removed", p)
		}
	}
	if !exists(filepath.Join(root, ".objects", "dup", layoutMarkerName)) {
		t.Error("marker not written")
	}
	// Later runs leave the conflicting leftovers alone.
	if st, err := migrateLocalLayout(root); err != nil || st != (layoutMigrationStats{}) {
		t.Errorf("second run: %+v, %v", st, err)
	}
	if !exists(filepath.Join(root, "dup/diff")) {
		t.Error("second run removed a conflicting legacy copy")
	}
}

func TestSameFileContents(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	big := make([]byte, 200<<10)
	for i := range big {
		big[i] = byte(i)
	}
	a := write("a", string(big))
	b := write("b", string(big))
	big[len(big)-1]++
	c := write("c", string(big))
	d := write("d", "short")
	e := write("e", "")
	f := write("f", "")
	for _, tc := range []struct {
		x, y string
		want bool
	}{{a, b, true}, {a, c, false}, {a, d, false}, {e, f, true}, {a, a, true}} {
		if got, err := sameFileContents(tc.x, tc.y); err != nil || got != tc.want {
			t.Errorf("sameFileContents(%s, %s) = %v, %v; want %v", filepath.Base(tc.x), filepath.Base(tc.y), got, err, tc.want)
		}
	}
}

// Releases before folder-marker support stored the marker "dir/" as a plain
// zero-length file "dir" (and its versions under .versions/<b>/dir/<vid>),
// indistinguishable from an empty object "dir". Both keys must stay readable
// after migration so whichever one the database references is served.
func TestMigrateLocalLayoutAliasesEmptyLegacyMarkers(t *testing.T) {
	root := t.TempDir()
	vid := "0b0e5a5e-7c1c-4a53-9d0e-0f7f2b0c6a11"
	writeTree(t, root, map[string]string{
		"bkt1/emptydir":                  "",
		"bkt1/data":                      "not empty",
		".versions/bkt1/emptydir/" + vid: "",
		".versions/bkt1/data/" + vid:     "old bytes",
	})
	st, err := migrateLocalLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if st.MarkerAliases != 2 {
		t.Errorf("MarkerAliases = %d, want 2 (object + version of the empty file)", st.MarkerAliases)
	}
	ls := NewLocalStorage(root)
	wantObject(t, ls, "bkt1", "emptydir", "")
	wantObject(t, ls, "bkt1", "emptydir/", "")
	wantObject(t, ls, "bkt1", "data", "not empty")
	if ok, _ := ls.ObjectExists("bkt1", "data/"); ok {
		t.Error("non-empty legacy file must not get a marker alias")
	}
	wantVersion(t, ls, "bkt1", "emptydir", vid, "")
	wantVersion(t, ls, "bkt1", "emptydir/", vid, "")
	wantVersion(t, ls, "bkt1", "data", vid, "old bytes")

	// Idempotent: a second run adds nothing.
	st2, err := migrateLocalLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	if st2.MarkerAliases != 0 || st2.Objects != 0 {
		t.Errorf("second run stats = %+v", st2)
	}
}
