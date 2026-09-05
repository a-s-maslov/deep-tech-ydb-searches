package ydbstore

import (
	"bytes"
	"errors"
	"testing"
)

func TestFloat32Vector(t *testing.T) {
	actual := float32Vector([]float32{1, 0})
	expected := []byte{0, 0, 128, 63, 0, 0, 0, 0, 1}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("unexpected FloatVector encoding: %v", actual)
	}
}

func TestSameIDsIgnoresOrder(t *testing.T) {
	if !sameIDs([]uint64{2, 1}, []uint64{1, 2}) {
		t.Fatal("sameIDs should ignore order")
	}
	if sameIDs([]uint64{1, 3}, []uint64{1, 2}) {
		t.Fatal("sameIDs accepted different ids")
	}
}

func TestIsUnsupportedFulltextPrefixError(t *testing.T) {
	known := errors.New("GENERIC_ERROR: Unsupported predicate is used to access index")
	if !isUnsupportedFulltextPrefixError(known) {
		t.Fatal("known relevance prefix limitation was not recognized")
	}
	if isUnsupportedFulltextPrefixError(errors.New("index is unavailable")) {
		t.Fatal("unexpected index error was classified as the known limitation")
	}
	if isUnsupportedFulltextPrefixError(nil) {
		t.Fatal("nil error was classified as the known limitation")
	}
}

func TestFulltextPrefixCapabilityRequiresBothQueryForms(t *testing.T) {
	result := FilteredProbeResult{
		FulltextMatchIDs:       []uint64{1},
		FulltextScoreIDs:       []uint64{1},
		FulltextScoreSupported: true,
	}
	result.FulltextPrefixSupported = sameIDs(result.FulltextMatchIDs, []uint64{1}) &&
		result.FulltextScoreSupported && sameIDs(result.FulltextScoreIDs, []uint64{1})
	if !result.FulltextPrefixSupported {
		t.Fatal("complete filtered relevance capability was not recognized")
	}

	result.FulltextMatchIDs = nil
	result.FulltextPrefixSupported = sameIDs(result.FulltextMatchIDs, []uint64{1}) &&
		result.FulltextScoreSupported && sameIDs(result.FulltextScoreIDs, []uint64{1})
	if result.FulltextPrefixSupported {
		t.Fatal("empty FulltextMatch result was accepted as supported")
	}
}
