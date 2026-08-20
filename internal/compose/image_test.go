package compose

import "testing"

func TestPinnedImageDigestAcceptsDigestWithoutTag(t *testing.T) {
	t.Parallel()

	image := "example.com/team/api@" + testReferenceDigest
	if _, ok := pinnedImageDigest(image); !ok {
		t.Fatalf("pinnedImageDigest(%q) rejected a pinned image", image)
	}
}

func TestPinnedImageDigestRejectsInvalidTags(t *testing.T) {
	t.Parallel()

	images := []string{
		"example.com/team/api:@" + testReferenceDigest,
		"example.com/team/api:invalid/tag@" + testReferenceDigest,
	}
	for _, image := range images {
		_, ok := pinnedImageDigest(image)
		if ok {
			t.Fatalf("pinnedImageDigest(%q) accepted an invalid tag", image)
		}
	}
}
