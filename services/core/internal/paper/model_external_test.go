package paper_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
)

type identifierReader interface {
	Scheme() paper.Scheme
	Value() string
	CanonicalKey() string
}

type workReader interface {
	Identity() paper.Identifier
	CanonicalKey() string
	Status() paper.WorkStatus
	Title() string
	Abstract() string
	PublishedAt() *time.Time
	VenueID() string
}

var (
	_ identifierReader = paper.Identifier{}
	_ workReader       = paper.Work{}
)

func TestValueObjectsExposeGettersWithoutMutableInvariantFields(t *testing.T) {
	t.Parallel()

	for _, value := range []any{paper.Identifier{}, paper.Work{}} {
		valueType := reflect.TypeOf(value)
		for index := 0; index < valueType.NumField(); index++ {
			field := valueType.Field(index)
			if field.IsExported() {
				t.Errorf("%s exposes mutable field %s", valueType.Name(), field.Name)
			}
		}
	}
}

func TestNewWorkPublicContractCannotLoadHistoricalStatus(t *testing.T) {
	t.Parallel()

	newWorkType := reflect.TypeOf(paper.NewWork)
	if newWorkType.IsVariadic() || newWorkType.NumIn() != 2 {
		t.Fatalf(
			"NewWork accepts status or optional arguments: variadic=%v inputs=%d",
			newWorkType.IsVariadic(),
			newWorkType.NumIn(),
		)
	}

	restoreWorkType := reflect.TypeOf(paper.RestoreWork)
	if restoreWorkType.IsVariadic() || restoreWorkType.NumIn() != 1 {
		t.Fatalf(
			"RestoreWork contract is not an explicit persisted-state boundary: variadic=%v inputs=%d",
			restoreWorkType.IsVariadic(),
			restoreWorkType.NumIn(),
		)
	}
}
