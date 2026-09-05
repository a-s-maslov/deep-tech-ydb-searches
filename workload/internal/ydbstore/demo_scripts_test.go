package ydbstore

import (
	"strings"
	"testing"

	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/config"
	"github.com/a-s-maslov/deep-tech-ydb-searches/workload/internal/model"
)

func TestDemoScriptsUseOnlineQueryBuilders(t *testing.T) {
	cfg := config.Config{
		TablePath:           "documents",
		FulltextIndex:       "ft_idx",
		VectorIndex:         "vec_idx",
		VectorDimension:     2,
		KMeansSearchTopSize: 24,
	}
	query := model.Query{
		QID:           "0",
		Text:          "Когда начался кризис?",
		LexicalQuery:  "начался кризис",
		FulltextQuery: "кризис 1962",
		Embedding:     []byte{1, 2, 3, 4},
	}
	scripts := DemoScripts(cfg, query)
	if len(scripts) != 5 {
		t.Fatalf("script count = %d, want 5", len(scripts))
	}
	byName := make(map[string]string, len(scripts))
	for _, script := range scripts {
		byName[script.Name] = script.Content
	}
	for name, wants := range map[string][]string{
		"01-fulltext-index.yql": {"fulltext_relevance", "ON (text)"},
		"02-fulltext-query.yql": {`$query = "кризис 1962"u`, `"100%" AS MinimumShouldMatch`, "LIMIT 10"},
		"03-vector-index.yql":   {"vector_kmeans_tree", "overlap_clusters = 3"},
		"04-vector-query.yql":   {"KMeansTreeSearchTopSize", "AQIDBA==", "VIEW `vec_idx`", "LIMIT 10"},
		"05-hybrid-query.yql":   {`$query = "начался кризис"u`, "AQIDBA==", "HybridRank", "(10, 10) AS Limits"},
	} {
		content, ok := byName[name]
		if !ok {
			t.Fatalf("missing %s", name)
		}
		for _, want := range wants {
			if !strings.Contains(content, want) {
				t.Errorf("%s does not contain %q", name, want)
			}
		}
	}
	if strings.Contains(byName["04-vector-query.yql"], "$target =") {
		t.Fatal("vector demo target must be inlined in the similarity expression so YDB can use the index")
	}
}
