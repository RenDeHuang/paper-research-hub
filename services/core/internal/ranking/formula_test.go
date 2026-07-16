package ranking

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestCitationVelocityRequiresBothBoundarySnapshots(t *testing.T) {
	t.Parallel()

	end := &MetricSnapshot{
		ObservedAt: time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC),
		Value:      decimal.NewFromInt(12),
	}

	_, err := CitationVelocity(CitationVelocityInput{End: end})
	var missing *MissingSignalError
	if !errors.As(err, &missing) {
		t.Fatalf("CitationVelocity() error = %v, want MissingSignalError", err)
	}
	if missing.Signal != SignalCitationVelocity {
		t.Fatalf("MissingSignalError.Signal = %q, want %q", missing.Signal, SignalCitationVelocity)
	}
	if !reflect.DeepEqual(missing.Fields, []string{"start_snapshot"}) {
		t.Fatalf("MissingSignalError.Fields = %v, want [start_snapshot]", missing.Fields)
	}

	_, err = CitationVelocity(CitationVelocityInput{})
	if !errors.As(err, &missing) {
		t.Fatalf("CitationVelocity() error = %v, want MissingSignalError", err)
	}
	if !reflect.DeepEqual(missing.Fields, []string{"start_snapshot", "end_snapshot"}) {
		t.Fatalf(
			"MissingSignalError.Fields = %v, want [start_snapshot end_snapshot]",
			missing.Fields,
		)
	}
}

func TestRankingFormulasReportMissingBoundaryTimesAsTypedSignals(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		call       func() error
		wantSignal Signal
		wantFields []string
	}{
		{
			name: "citation start time",
			call: func() error {
				_, err := CitationVelocity(CitationVelocityInput{
					Start: &MetricSnapshot{Value: decimal.NewFromInt(1)},
					End:   &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(2)},
				})
				return err
			},
			wantSignal: SignalCitationVelocity,
			wantFields: []string{"start_observed_at"},
		},
		{
			name: "code end time",
			call: func() error {
				_, err := CodeGrowth(CodeGrowthInput{
					Start: &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(1)},
					End:   &MetricSnapshot{Value: decimal.NewFromInt(2)},
				})
				return err
			},
			wantSignal: SignalCodeGrowth,
			wantFields: []string{"end_observed_at"},
		},
		{
			name: "topic baseline start",
			call: func() error {
				_, err := TopicGrowth(TopicGrowthInput{
					Baseline: &WindowMetric{
						Window: Window{End: at.Add(-24 * time.Hour)},
						Value:  decimal.NewFromInt(1),
					},
					Current: &WindowMetric{
						Window: Window{Start: at.Add(-24 * time.Hour), End: at},
						Value:  decimal.NewFromInt(2),
					},
				})
				return err
			},
			wantSignal: SignalTopicGrowth,
			wantFields: []string{"baseline_window_start"},
		},
		{
			name: "method current end",
			call: func() error {
				_, err := MethodAdoption(MethodAdoptionInput{
					Baseline: &AdoptionWindow{
						Window:  Window{Start: at.Add(-48 * time.Hour), End: at.Add(-24 * time.Hour)},
						Adopted: decimal.NewFromInt(1),
						Total:   decimal.NewFromInt(2),
					},
					Current: &AdoptionWindow{
						Window:  Window{Start: at.Add(-24 * time.Hour)},
						Adopted: decimal.NewFromInt(1),
						Total:   decimal.NewFromInt(2),
					},
				})
				return err
			},
			wantSignal: SignalMethodAdoption,
			wantFields: []string{"current_window_end"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var missing *MissingSignalError
			if err := tt.call(); !errors.As(err, &missing) {
				t.Fatalf("formula error = %v, want MissingSignalError", err)
			}
			if missing.Signal != tt.wantSignal ||
				!reflect.DeepEqual(missing.Fields, tt.wantFields) {
				t.Fatalf(
					"MissingSignalError = %#v, want signal %q fields %v",
					missing,
					tt.wantSignal,
					tt.wantFields,
				)
			}
		})
	}
}

func TestCitationVelocityUsesElapsedDaysBetweenBoundaries(t *testing.T) {
	t.Parallel()

	startAt := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)
	got, err := CitationVelocity(CitationVelocityInput{
		Start: &MetricSnapshot{ObservedAt: startAt, Value: decimal.NewFromInt(6)},
		End: &MetricSnapshot{
			ObservedAt: startAt.Add(48 * time.Hour),
			Value:      decimal.NewFromInt(12),
		},
	})
	if err != nil {
		t.Fatalf("CitationVelocity() error = %v", err)
	}
	want := decimal.NewFromInt(3)
	if !got.Equal(want) {
		t.Fatalf("CitationVelocity() = %s, want %s citations/day", got, want)
	}
}

func TestCitationVelocityRejectsReversedOrZeroDurationBoundaries(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)
	_, err := CitationVelocity(CitationVelocityInput{
		Start: &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(1)},
		End:   &MetricSnapshot{ObservedAt: at.Add(-time.Hour), Value: decimal.NewFromInt(2)},
	})
	if !errors.Is(err, ErrTimeReversed) {
		t.Fatalf("CitationVelocity() error = %v, want ErrTimeReversed", err)
	}

	_, err = CitationVelocity(CitationVelocityInput{
		Start: &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(1)},
		End:   &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(2)},
	})
	var zero *ZeroDenominatorError
	if !errors.As(err, &zero) {
		t.Fatalf("CitationVelocity() error = %v, want ZeroDenominatorError", err)
	}
	if zero.Signal != SignalCitationVelocity || zero.Field != "elapsed_time" {
		t.Fatalf("ZeroDenominatorError = %#v, want citation_velocity elapsed_time", zero)
	}
}

func TestCodeGrowthRequiresTwoSnapshotsAndUsesRelativeChange(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.July, 14, 0, 0, 0, 0, time.UTC)
	end := &MetricSnapshot{
		ObservedAt: at.Add(48 * time.Hour),
		Value:      decimal.NewFromInt(15),
	}

	_, err := CodeGrowth(CodeGrowthInput{End: end})
	var missing *MissingSignalError
	if !errors.As(err, &missing) {
		t.Fatalf("CodeGrowth() error = %v, want MissingSignalError", err)
	}
	if missing.Signal != SignalCodeGrowth ||
		!reflect.DeepEqual(missing.Fields, []string{"start_snapshot"}) {
		t.Fatalf("MissingSignalError = %#v, want code_growth start_snapshot", missing)
	}

	got, err := CodeGrowth(CodeGrowthInput{
		Start: &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(10)},
		End:   end,
	})
	if err != nil {
		t.Fatalf("CodeGrowth() error = %v", err)
	}
	want := decimal.RequireFromString("0.5")
	if !got.Equal(want) {
		t.Fatalf("CodeGrowth() = %s, want %s", got, want)
	}
}

func TestCodeGrowthRejectsZeroBaselineAndInvalidBoundaryOrder(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)
	_, err := CodeGrowth(CodeGrowthInput{
		Start: &MetricSnapshot{ObservedAt: at.Add(-time.Hour), Value: decimal.Zero},
		End:   &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(1)},
	})
	var zero *ZeroDenominatorError
	if !errors.As(err, &zero) {
		t.Fatalf("CodeGrowth() error = %v, want ZeroDenominatorError", err)
	}
	if zero.Signal != SignalCodeGrowth || zero.Field != "start_value" {
		t.Fatalf("ZeroDenominatorError = %#v, want code_growth start_value", zero)
	}

	_, err = CodeGrowth(CodeGrowthInput{
		Start: &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(1)},
		End:   &MetricSnapshot{ObservedAt: at.Add(-time.Hour), Value: decimal.NewFromInt(2)},
	})
	if !errors.Is(err, ErrTimeReversed) {
		t.Fatalf("CodeGrowth() error = %v, want ErrTimeReversed", err)
	}

	_, err = CodeGrowth(CodeGrowthInput{
		Start: &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(1)},
		End:   &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(2)},
	})
	if !errors.Is(err, ErrWindowMismatch) {
		t.Fatalf("CodeGrowth() error = %v, want ErrWindowMismatch", err)
	}
}

func TestTopicGrowthComparesEqualCurrentAndBaselineWindows(t *testing.T) {
	t.Parallel()

	baselineStart := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	baseline := &WindowMetric{
		Window: Window{Start: baselineStart, End: baselineStart.Add(7 * 24 * time.Hour)},
		Value:  decimal.NewFromInt(20),
	}
	current := &WindowMetric{
		Window: Window{Start: baseline.Window.End, End: baseline.Window.End.Add(7 * 24 * time.Hour)},
		Value:  decimal.NewFromInt(25),
	}

	got, err := TopicGrowth(TopicGrowthInput{Baseline: baseline, Current: current})
	if err != nil {
		t.Fatalf("TopicGrowth() error = %v", err)
	}
	want := decimal.RequireFromString("0.25")
	if !got.Equal(want) {
		t.Fatalf("TopicGrowth() = %s, want %s", got, want)
	}
}

func TestTopicGrowthReportsMissingWindowsInsteadOfSubstitutingZero(t *testing.T) {
	t.Parallel()

	_, err := TopicGrowth(TopicGrowthInput{})
	var missing *MissingSignalError
	if !errors.As(err, &missing) {
		t.Fatalf("TopicGrowth() error = %v, want MissingSignalError", err)
	}
	if missing.Signal != SignalTopicGrowth ||
		!reflect.DeepEqual(missing.Fields, []string{"baseline_window", "current_window"}) {
		t.Fatalf(
			"MissingSignalError = %#v, want topic_growth baseline_window/current_window",
			missing,
		)
	}
}

func TestTopicGrowthRejectsZeroBaselineReversedTimeAndWindowMismatch(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	sevenDays := 7 * 24 * time.Hour
	baseline := &WindowMetric{
		Window: Window{Start: start, End: start.Add(sevenDays)},
		Value:  decimal.Zero,
	}
	current := &WindowMetric{
		Window: Window{Start: start.Add(sevenDays), End: start.Add(2 * sevenDays)},
		Value:  decimal.NewFromInt(1),
	}

	_, err := TopicGrowth(TopicGrowthInput{Baseline: baseline, Current: current})
	var zero *ZeroDenominatorError
	if !errors.As(err, &zero) {
		t.Fatalf("TopicGrowth() error = %v, want ZeroDenominatorError", err)
	}
	if zero.Signal != SignalTopicGrowth || zero.Field != "baseline_value" {
		t.Fatalf("ZeroDenominatorError = %#v, want topic_growth baseline_value", zero)
	}

	reversed := *baseline
	reversed.Window = Window{Start: start.Add(sevenDays), End: start}
	_, err = TopicGrowth(TopicGrowthInput{Baseline: &reversed, Current: current})
	if !errors.Is(err, ErrTimeReversed) {
		t.Fatalf("TopicGrowth() reversed window error = %v, want ErrTimeReversed", err)
	}

	mismatched := *current
	mismatched.Window.End = mismatched.Window.End.Add(24 * time.Hour)
	_, err = TopicGrowth(TopicGrowthInput{Baseline: baseline, Current: &mismatched})
	if !errors.Is(err, ErrWindowMismatch) {
		t.Fatalf("TopicGrowth() mismatched duration error = %v, want ErrWindowMismatch", err)
	}

	overlapping := *current
	overlapping.Window.Start = baseline.Window.End.Add(-time.Hour)
	overlapping.Window.End = overlapping.Window.Start.Add(sevenDays)
	_, err = TopicGrowth(TopicGrowthInput{Baseline: baseline, Current: &overlapping})
	if !errors.Is(err, ErrWindowMismatch) {
		t.Fatalf("TopicGrowth() overlapping window error = %v, want ErrWindowMismatch", err)
	}
}

func TestMethodAdoptionIsShareDeltaRatherThanTopicCountGrowth(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	sevenDays := 7 * 24 * time.Hour
	baseline := &AdoptionWindow{
		Window:  Window{Start: start, End: start.Add(sevenDays)},
		Adopted: decimal.NewFromInt(20),
		Total:   decimal.NewFromInt(100),
	}
	current := &AdoptionWindow{
		Window:  Window{Start: baseline.Window.End, End: baseline.Window.End.Add(sevenDays)},
		Adopted: decimal.NewFromInt(45),
		Total:   decimal.NewFromInt(150),
	}

	got, err := MethodAdoption(MethodAdoptionInput{Baseline: baseline, Current: current})
	if err != nil {
		t.Fatalf("MethodAdoption() error = %v", err)
	}
	want := decimal.RequireFromString("0.1")
	if !got.Equal(want) {
		t.Fatalf("MethodAdoption() = %s, want share delta %s", got, want)
	}
}

func TestMethodAdoptionCrossMultipliesBeforeSingleRounding(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	window := func(offset int) Window {
		windowStart := start.Add(time.Duration(offset) * 24 * time.Hour)
		return Window{Start: windowStart, End: windowStart.Add(24 * time.Hour)}
	}

	tests := []struct {
		name            string
		baselineAdopted int64
		baselineTotal   int64
		currentAdopted  int64
		currentTotal    int64
		want            string
	}{
		{
			name:            "positive repeating delta",
			baselineAdopted: 1,
			baselineTotal:   6,
			currentAdopted:  1,
			currentTotal:    3,
			want:            "0.166666666666666667",
		},
		{
			name:            "negative repeating delta",
			baselineAdopted: 1,
			baselineTotal:   3,
			currentAdopted:  1,
			currentTotal:    6,
			want:            "-0.166666666666666667",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := MethodAdoption(MethodAdoptionInput{
				Baseline: &AdoptionWindow{
					Window:  window(0),
					Adopted: decimal.NewFromInt(tt.baselineAdopted),
					Total:   decimal.NewFromInt(tt.baselineTotal),
				},
				Current: &AdoptionWindow{
					Window:  window(1),
					Adopted: decimal.NewFromInt(tt.currentAdopted),
					Total:   decimal.NewFromInt(tt.currentTotal),
				},
			})
			if err != nil {
				t.Fatalf("MethodAdoption() error = %v", err)
			}
			threshold := decimal.RequireFromString(tt.want)
			if got.Cmp(threshold) != 0 {
				t.Fatalf(
					"MethodAdoption() = %s, want exact threshold boundary %s",
					got,
					threshold,
				)
			}
		})
	}
}

func TestMethodAdoptionReportsMissingWindows(t *testing.T) {
	t.Parallel()

	_, err := MethodAdoption(MethodAdoptionInput{})
	var missing *MissingSignalError
	if !errors.As(err, &missing) {
		t.Fatalf("MethodAdoption() error = %v, want MissingSignalError", err)
	}
	if missing.Signal != SignalMethodAdoption ||
		!reflect.DeepEqual(missing.Fields, []string{"baseline_window", "current_window"}) {
		t.Fatalf(
			"MissingSignalError = %#v, want method_adoption baseline_window/current_window",
			missing,
		)
	}
}

func TestMethodAdoptionRejectsZeroTotalsMismatchedWindowsAndInvalidShares(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	sevenDays := 7 * 24 * time.Hour
	baseline := &AdoptionWindow{
		Window:  Window{Start: start, End: start.Add(sevenDays)},
		Adopted: decimal.Zero,
		Total:   decimal.Zero,
	}
	current := &AdoptionWindow{
		Window:  Window{Start: baseline.Window.End, End: baseline.Window.End.Add(sevenDays)},
		Adopted: decimal.NewFromInt(1),
		Total:   decimal.NewFromInt(2),
	}

	_, err := MethodAdoption(MethodAdoptionInput{Baseline: baseline, Current: current})
	var zero *ZeroDenominatorError
	if !errors.As(err, &zero) {
		t.Fatalf("MethodAdoption() error = %v, want ZeroDenominatorError", err)
	}
	if zero.Signal != SignalMethodAdoption || zero.Field != "baseline_total" {
		t.Fatalf("ZeroDenominatorError = %#v, want method_adoption baseline_total", zero)
	}

	baseline.Total = decimal.NewFromInt(1)
	current.Total = decimal.Zero
	_, err = MethodAdoption(MethodAdoptionInput{Baseline: baseline, Current: current})
	if !errors.As(err, &zero) || zero.Field != "current_total" {
		t.Fatalf("MethodAdoption() error = %v, want current_total ZeroDenominatorError", err)
	}

	current.Total = decimal.NewFromInt(2)
	current.Window.End = current.Window.End.Add(24 * time.Hour)
	_, err = MethodAdoption(MethodAdoptionInput{Baseline: baseline, Current: current})
	if !errors.Is(err, ErrWindowMismatch) {
		t.Fatalf("MethodAdoption() error = %v, want ErrWindowMismatch", err)
	}

	current.Window.End = current.Window.Start.Add(sevenDays)
	current.Adopted = decimal.NewFromInt(3)
	_, err = MethodAdoption(MethodAdoptionInput{Baseline: baseline, Current: current})
	if !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("MethodAdoption() error = %v, want ErrInvalidMetric", err)
	}
}

func TestCitationPercentileUsesTopicMonthAndPaperTypeCohort(t *testing.T) {
	t.Parallel()

	key, err := NewCitationCohortKey(
		"machine-learning",
		time.Date(2026, time.July, 16, 13, 45, 0, 0, time.FixedZone("CST", 8*60*60)),
		paper.PaperTypeResearchArticle,
	)
	if err != nil {
		t.Fatalf("NewCitationCohortKey() error = %v", err)
	}
	if key.Topic != "machine-learning" {
		t.Fatalf("CitationCohortKey.Topic = %q", key.Topic)
	}
	wantMonth := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	if !key.Month.Equal(wantMonth) {
		t.Fatalf("CitationCohortKey.Month = %v, want %v", key.Month, wantMonth)
	}
	if key.PaperType != paper.PaperTypeResearchArticle {
		t.Fatalf("CitationCohortKey.PaperType = %q", key.PaperType)
	}
	if got, want := key.String(), "t16:machine-learningm7:2026-07p16:research_article"; got != want {
		t.Fatalf("CitationCohortKey.String() = %q, want %q", got, want)
	}

	got, err := CitationPercentile(CitationPercentileInput{
		Cohort: key,
		Target: CohortObservation{
			PaperID: uuid.MustParse("00000000-0000-0000-0000-000000000100"),
			Value:   decimal.NewFromInt(20),
		},
		Observations: []CohortObservation{
			{
				PaperID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
				Value:   decimal.NewFromInt(30),
			},
			{
				PaperID: uuid.MustParse("00000000-0000-0000-0000-000000000002"),
				Value:   decimal.NewFromInt(20),
			},
			{
				PaperID: uuid.MustParse("00000000-0000-0000-0000-000000000003"),
				Value:   decimal.NewFromInt(10),
			},
			{
				PaperID: uuid.MustParse("00000000-0000-0000-0000-000000000004"),
				Value:   decimal.NewFromInt(20),
			},
		},
	})
	if err != nil {
		t.Fatalf("CitationPercentile() error = %v", err)
	}
	want := decimal.NewFromInt(75)
	if !got.Equal(want) {
		t.Fatalf("CitationPercentile() = %s, want empirical percentile %s", got, want)
	}
}

func TestCitationCohortMonthUsesCalendarMonthWithoutTimezoneDrift(t *testing.T) {
	t.Parallel()

	key, err := NewCitationCohortKey(
		"ml",
		time.Date(2026, time.July, 1, 0, 30, 0, 0, time.FixedZone("UTC+14", 14*60*60)),
		paper.PaperTypeReview,
	)
	if err != nil {
		t.Fatalf("NewCitationCohortKey() error = %v", err)
	}
	if got, want := key.Month.Format("2006-01"), "2026-07"; got != want {
		t.Fatalf("CitationCohortKey month = %s, want %s", got, want)
	}
}

func TestCitationCohortRequiresCanonicalTopicSlugOrID(t *testing.T) {
	t.Parallel()

	month := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	canonicalID := "123e4567-e89b-12d3-a456-426614174000"
	key, err := NewCitationCohortKey(canonicalID, month, paper.PaperTypeReview)
	if err != nil {
		t.Fatalf("NewCitationCohortKey(canonical UUID) error = %v", err)
	}
	if key.Topic != canonicalID {
		t.Fatalf("CitationCohortKey.Topic = %q, want canonical UUID", key.Topic)
	}

	for _, topic := range []string{
		"Machine-Learning",
		"machine_learning",
		"machine--learning",
		" machine-learning ",
		"alpha|2026-07",
		"123E4567-E89B-12D3-A456-426614174000",
	} {
		topic := topic
		t.Run(topic, func(t *testing.T) {
			t.Parallel()

			if key, err := NewCitationCohortKey(
				topic,
				month,
				paper.PaperTypeReview,
			); !errors.Is(err, ErrInvalidCohort) {
				t.Fatalf("NewCitationCohortKey(%q) = %#v, %v, want ErrInvalidCohort", topic, key, err)
			}
		})
	}
}

func TestCitationCohortEncodingSeparatesFormerPipeCollisionInputs(t *testing.T) {
	t.Parallel()

	left := []string{"alpha|2026-07", "review", "dataset"}
	right := []string{"alpha", "2026-07|review", "dataset"}
	if strings.Join(left, "|") != strings.Join(right, "|") {
		t.Fatal("test fixture does not collide under raw pipe joining")
	}

	leftEncoded := encodeCohortComponents(left...)
	rightEncoded := encodeCohortComponents(right...)
	if leftEncoded == rightEncoded {
		t.Fatalf("length-prefixed cohort encodings collide: %q", leftEncoded)
	}
}

func TestCitationPercentileRejectsIncompleteCohortAndMissingValues(t *testing.T) {
	t.Parallel()

	for _, input := range []struct {
		name      string
		topic     string
		paperType paper.PaperType
	}{
		{name: "missing topic", paperType: paper.PaperTypeReview},
		{name: "missing paper type", topic: "ml"},
		{name: "invalid paper type", topic: "ml", paperType: paper.PaperType("benchmark")},
	} {
		input := input
		t.Run(input.name, func(t *testing.T) {
			t.Parallel()

			if key, err := NewCitationCohortKey(
				input.topic,
				time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
				input.paperType,
			); !errors.Is(err, ErrInvalidCohort) {
				t.Fatalf("NewCitationCohortKey() = %#v, %v, want ErrInvalidCohort", key, err)
			}
		})
	}

	key, err := NewCitationCohortKey(
		"ml",
		time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		paper.PaperTypeReview,
	)
	if err != nil {
		t.Fatalf("NewCitationCohortKey() error = %v", err)
	}
	_, err = CitationPercentile(CitationPercentileInput{
		Cohort: key,
		Target: CohortObservation{
			PaperID: uuid.MustParse("00000000-0000-0000-0000-000000000100"),
			Value:   decimal.NewFromInt(20),
		},
	})
	var missing *MissingSignalError
	if !errors.As(err, &missing) {
		t.Fatalf("CitationPercentile() error = %v, want MissingSignalError", err)
	}
	if missing.Signal != SignalCitationPercentile ||
		!reflect.DeepEqual(missing.Fields, []string{"cohort_observations"}) {
		t.Fatalf("MissingSignalError = %#v, want citation_percentile cohort_observations", missing)
	}
}

func TestCitationPercentileRejectsNilOrDuplicatePaperIDs(t *testing.T) {
	t.Parallel()

	key, err := NewCitationCohortKey(
		"ml",
		time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		paper.PaperTypeReview,
	)
	if err != nil {
		t.Fatalf("NewCitationCohortKey() error = %v", err)
	}
	targetID := uuid.MustParse("00000000-0000-0000-0000-000000000100")
	firstID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	tests := []struct {
		name  string
		input CitationPercentileInput
	}{
		{
			name: "nil target ID",
			input: CitationPercentileInput{
				Cohort: key,
				Target: CohortObservation{Value: decimal.NewFromInt(20)},
				Observations: []CohortObservation{
					{PaperID: firstID, Value: decimal.NewFromInt(10)},
				},
			},
		},
		{
			name: "nil cohort ID",
			input: CitationPercentileInput{
				Cohort: key,
				Target: CohortObservation{PaperID: targetID, Value: decimal.NewFromInt(20)},
				Observations: []CohortObservation{
					{Value: decimal.NewFromInt(10)},
				},
			},
		},
		{
			name: "duplicate cohort ID",
			input: CitationPercentileInput{
				Cohort: key,
				Target: CohortObservation{PaperID: targetID, Value: decimal.NewFromInt(20)},
				Observations: []CohortObservation{
					{PaperID: firstID, Value: decimal.NewFromInt(10)},
					{PaperID: firstID, Value: decimal.NewFromInt(30)},
				},
			},
		},
		{
			name: "target ID repeated in cohort",
			input: CitationPercentileInput{
				Cohort: key,
				Target: CohortObservation{PaperID: targetID, Value: decimal.NewFromInt(20)},
				Observations: []CohortObservation{
					{PaperID: targetID, Value: decimal.NewFromInt(10)},
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := CitationPercentile(tt.input); !errors.Is(err, ErrInvalidCohort) {
				t.Fatalf("CitationPercentile() error = %v, want ErrInvalidCohort", err)
			}
		})
	}
}

func TestCitationPercentileRejectsNegativeTargetOrCohortValues(t *testing.T) {
	t.Parallel()

	key, err := NewCitationCohortKey(
		"ml",
		time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		paper.PaperTypeReview,
	)
	if err != nil {
		t.Fatalf("NewCitationCohortKey() error = %v", err)
	}
	targetID := uuid.MustParse("00000000-0000-0000-0000-000000000100")
	firstID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	tests := []CitationPercentileInput{
		{
			Cohort: key,
			Target: CohortObservation{
				PaperID: targetID,
				Value:   decimal.NewFromInt(-1),
			},
			Observations: []CohortObservation{
				{PaperID: firstID, Value: decimal.Zero},
			},
		},
		{
			Cohort: key,
			Target: CohortObservation{
				PaperID: targetID,
				Value:   decimal.Zero,
			},
			Observations: []CohortObservation{
				{PaperID: firstID, Value: decimal.NewFromInt(-1)},
			},
		},
	}

	for _, input := range tests {
		if _, err := CitationPercentile(input); !errors.Is(err, ErrInvalidMetric) {
			t.Fatalf("CitationPercentile() error = %v, want ErrInvalidMetric", err)
		}
	}
}

func TestCitationPercentileDoesNotMutateObservationInput(t *testing.T) {
	t.Parallel()

	key, err := NewCitationCohortKey(
		"ml",
		time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		paper.PaperTypeReview,
	)
	if err != nil {
		t.Fatalf("NewCitationCohortKey() error = %v", err)
	}
	observations := []CohortObservation{
		{
			PaperID: uuid.MustParse("00000000-0000-0000-0000-000000000002"),
			Value:   decimal.NewFromInt(20),
		},
		{
			PaperID: uuid.MustParse("00000000-0000-0000-0000-000000000001"),
			Value:   decimal.NewFromInt(20),
		},
	}
	before := append([]CohortObservation(nil), observations...)

	_, err = CitationPercentile(CitationPercentileInput{
		Cohort: key,
		Target: CohortObservation{
			PaperID: uuid.MustParse("00000000-0000-0000-0000-000000000100"),
			Value:   decimal.NewFromInt(20),
		},
		Observations: observations,
	})
	if err != nil {
		t.Fatalf("CitationPercentile() error = %v", err)
	}
	if !reflect.DeepEqual(observations, before) {
		t.Fatalf("CitationPercentile() mutated observations: got %v want %v", observations, before)
	}
}

func TestRankingRejectsExcludedAndTerminalPaperStatuses(t *testing.T) {
	t.Parallel()

	excluded := []RankabilityInput{
		{Status: paper.WorkStatusRetracted},
		{Status: paper.WorkStatusWithdrawn},
		{Status: paper.WorkStatusRejected},
		{Status: paper.WorkStatusSuperseded},
		{Status: paper.WorkStatusActive, Excluded: true},
		{Status: paper.WorkStatus("unknown")},
	}
	for _, input := range excluded {
		input := input
		t.Run(input.Status.String(), func(t *testing.T) {
			t.Parallel()

			err := ValidateRankable(input)
			var unrankable *UnrankableWorkError
			if !errors.As(err, &unrankable) {
				t.Fatalf("ValidateRankable(%#v) error = %v, want UnrankableWorkError", input, err)
			}
			if unrankable.Status != input.Status || unrankable.Excluded != input.Excluded {
				t.Fatalf("UnrankableWorkError = %#v, want status/excluded from %#v", unrankable, input)
			}
		})
	}

	if err := ValidateRankable(RankabilityInput{Status: paper.WorkStatusActive}); err != nil {
		t.Fatalf("ValidateRankable(active) error = %v", err)
	}
}

func TestRankingFormulasRejectNegativeMetricInputs(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	_, err := CitationVelocity(CitationVelocityInput{
		Start: &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(-1)},
		End:   &MetricSnapshot{ObservedAt: at.Add(24 * time.Hour), Value: decimal.Zero},
	})
	if !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("CitationVelocity() error = %v, want ErrInvalidMetric", err)
	}

	_, err = CodeGrowth(CodeGrowthInput{
		Start: &MetricSnapshot{ObservedAt: at, Value: decimal.NewFromInt(1)},
		End:   &MetricSnapshot{ObservedAt: at.Add(24 * time.Hour), Value: decimal.NewFromInt(-1)},
	})
	if !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("CodeGrowth() error = %v, want ErrInvalidMetric", err)
	}

	_, err = TopicGrowth(TopicGrowthInput{
		Baseline: &WindowMetric{
			Window: Window{Start: at.Add(-48 * time.Hour), End: at.Add(-24 * time.Hour)},
			Value:  decimal.NewFromInt(1),
		},
		Current: &WindowMetric{
			Window: Window{Start: at.Add(-24 * time.Hour), End: at},
			Value:  decimal.NewFromInt(-1),
		},
	})
	if !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("TopicGrowth() error = %v, want ErrInvalidMetric", err)
	}
}

func TestRankingFormulaVersionIsExplicit(t *testing.T) {
	t.Parallel()

	if FormulaVersion != "ranking-formulas/v1" {
		t.Fatalf("FormulaVersion = %q, want ranking-formulas/v1", FormulaVersion)
	}
}
