package api

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"bkt/internal/models"
)

func TestMergeVersionItemsIsLatestPerKey(t *testing.T) {
	now := time.Now()
	keys := []string{"a", "b", "c"}
	currents := map[string]*models.Object{
		"a": {Key: "a", VersionID: "a3", UpdatedAt: now},
	}
	versions := map[string][]models.ObjectVersion{
		"a": {{Key: "a", VersionID: "a2"}, {Key: "a", VersionID: "a1"}},
		// b: deleted — newest entry is a delete marker.
		"b": {{Key: "b", VersionID: "bm", IsDeleteMarker: true}, {Key: "b", VersionID: "b1"}},
		// c: no current row and no marker — the newest content version is latest.
		"c": {{Key: "c", VersionID: "c2"}, {Key: "c", VersionID: "c1"}},
	}
	items := mergeVersionItems(keys, currents, versions)
	want := []struct {
		vid    string
		latest bool
		marker bool
	}{
		{"a3", true, false}, {"a2", false, false}, {"a1", false, false},
		{"bm", true, true}, {"b1", false, false},
		{"c2", true, false}, {"c1", false, false},
	}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d", len(items), len(want))
	}
	for i, w := range want {
		if items[i].versionID != w.vid || items[i].isLatest != w.latest || items[i].isMarker != w.marker {
			t.Errorf("item %d = %+v, want %+v", i, items[i], w)
		}
	}

	// The null version of a current object written while unversioned.
	items = mergeVersionItems([]string{"n"}, map[string]*models.Object{"n": {Key: "n"}}, nil)
	if len(items) != 1 || items[0].versionID != "null" || !items[0].isLatest {
		t.Errorf("null current version: %+v", items)
	}
}

func TestPaginateVersionItems(t *testing.T) {
	items := make([]versionListItem, 5)
	page, trunc := paginateVersionItems(items, 3)
	if len(page) != 3 || !trunc {
		t.Errorf("5 items / max 3: len=%d truncated=%v", len(page), trunc)
	}
	page, trunc = paginateVersionItems(items[:3], 3)
	if len(page) != 3 || trunc {
		t.Errorf("3 items / max 3: len=%d truncated=%v", len(page), trunc)
	}
}

func TestListVersionsResultXML(t *testing.T) {
	out := listVersionsResult{
		Name: "b", KeyMarker: "k", VersionIdMarker: "v", MaxKeys: 2, IsTruncated: true,
		NextKeyMarker: "k2", NextVersionIdMarker: "v2",
	}
	raw, err := xml.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"<KeyMarker>k</KeyMarker>", "<VersionIdMarker>v</VersionIdMarker>",
		"<NextKeyMarker>k2</NextKeyMarker>", "<NextVersionIdMarker>v2</NextVersionIdMarker>", "<IsTruncated>true</IsTruncated>"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestDeleteObjectsXMLCarriesVersionId(t *testing.T) {
	var req DeleteRequest
	body := `<Delete><Quiet>false</Quiet><Object><Key>a</Key><VersionId>v1</VersionId></Object><Object><Key>b</Key></Object></Delete>`
	if err := xml.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Objects) != 2 || req.Objects[0].VersionId != "v1" || req.Objects[1].VersionId != "" {
		t.Fatalf("parsed %+v", req.Objects)
	}
	raw, _ := xml.Marshal(DeleteResult{
		Deleted: []DeletedObject{{Key: "a", VersionId: "m1", DeleteMarker: true, DeleteMarkerVersionId: "m1"}},
		Errors:  []DeleteError{{Key: "b", VersionId: "v2", Code: "AccessDenied", Message: "x"}},
	})
	s := string(raw)
	for _, want := range []string{"<VersionId>m1</VersionId>", "<DeleteMarker>true</DeleteMarker>",
		"<DeleteMarkerVersionId>m1</DeleteMarkerVersionId>", "<VersionId>v2</VersionId>"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}
