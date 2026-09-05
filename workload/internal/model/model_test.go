package model

import "testing"

func TestQueryFulltextText(t *testing.T) {
	if got := (Query{Text: "original", LexicalQuery: "lexical"}).FulltextText(); got != "lexical" {
		t.Fatalf("got %q, want lexical", got)
	}
	if got := (Query{Text: "legacy"}).FulltextText(); got != "legacy" {
		t.Fatalf("legacy artifact fallback = %q", got)
	}
}

func TestQueryFulltextWorkloadText(t *testing.T) {
	query := Query{Text: "original", LexicalQuery: "lexical", FulltextQuery: "keywords"}
	if got := query.FulltextWorkloadText(); got != "keywords" {
		t.Fatalf("FulltextWorkloadText() = %q, want keywords", got)
	}
	if got := (Query{Text: "original", LexicalQuery: "lexical"}).FulltextWorkloadText(); got != "lexical" {
		t.Fatalf("legacy FulltextWorkloadText() = %q, want lexical", got)
	}
}
