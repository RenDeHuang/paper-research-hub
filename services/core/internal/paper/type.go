package paper

import "fmt"

type PaperType string

const (
	PaperTypeResearchArticle    PaperType = "research_article"
	PaperTypeReview             PaperType = "review"
	PaperTypePreprint           PaperType = "preprint"
	PaperTypeProceedingsArticle PaperType = "proceedings_article"
	PaperTypeDataset            PaperType = "dataset"
	PaperTypeOther              PaperType = "other"
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
		PaperTypeProceedingsArticle,
		PaperTypeDataset,
		PaperTypeOther:
		return true
	default:
		return false
	}
}

func (paperType PaperType) String() string {
	return string(paperType)
}
