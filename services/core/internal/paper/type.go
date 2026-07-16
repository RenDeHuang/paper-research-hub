package paper

import "fmt"

type PaperType string

const (
	PaperTypeResearchArticle PaperType = "research_article"
	PaperTypeReview          PaperType = "review"
	PaperTypePreprint        PaperType = "preprint"
	PaperTypeDataset         PaperType = "dataset"
	PaperTypeBenchmark       PaperType = "benchmark"
)

func NewPaperType(raw string) (PaperType, error) {
	paperType := PaperType(raw)
	if !paperType.Valid() {
		return "", fmt.Errorf("invalid paper type %q", raw)
	}
	return paperType, nil
}

func (paperType PaperType) Valid() bool {
	switch paperType {
	case PaperTypeResearchArticle,
		PaperTypeReview,
		PaperTypePreprint,
		PaperTypeDataset,
		PaperTypeBenchmark:
		return true
	default:
		return false
	}
}

func (paperType PaperType) String() string {
	return string(paperType)
}
