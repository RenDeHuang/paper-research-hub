package venueenrich

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/nlmcatalog"
)

func TestReconcileConflictingISSNsOnlyResolvesPartialOrContainingIntersections(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name      string
		rows      []RegistryRow
		wantCalls []string
	}{
		{
			name: "identical sets are allowed",
			rows: []RegistryRow{
				nlmRegistryRow(
					"medicine",
					1,
					"Same Identity A",
					"0028-0836",
					"",
					[]string{"0028-0836"},
				),
				nlmRegistryRow(
					"medicine",
					2,
					"Same Identity B",
					"0028-0836",
					"",
					[]string{"0028-0836"},
				),
			},
		},
		{
			name: "disjoint sets are allowed",
			rows: []RegistryRow{
				nlmRegistryRow(
					"medicine",
					1,
					"Disjoint A",
					"0028-0836",
					"",
					[]string{"0028-0836"},
				),
				nlmRegistryRow(
					"medicine",
					2,
					"Disjoint B",
					"1474-175X",
					"",
					[]string{"1474-175X"},
				),
			},
		},
		{
			name: "containing intersection resolves only unique titles",
			rows: []RegistryRow{
				nlmRegistryRow(
					"medicine",
					1,
					"Owner Journal",
					"1474-1776",
					"1474-1784",
					[]string{"1474-1776", "1474-1784"},
				),
				nlmRegistryRow(
					"biology",
					1,
					"Owner Journal",
					"1474-1776",
					"1474-1784",
					[]string{"1474-1776", "1474-1784"},
				),
				nlmRegistryRow(
					"medicine",
					2,
					"Polluted Journal",
					"1474-175X",
					"1474-1768",
					[]string{
						"1474-175X",
						"1474-1768",
						"1474-1776",
						"1474-1784",
					},
				),
				nlmRegistryRow(
					"medicine",
					3,
					"Unrelated Journal",
					"0007-1188",
					"1476-5381",
					[]string{"0007-1188", "1476-5381"},
				),
			},
			wantCalls: []string{"Owner Journal", "Polluted Journal"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolver := &nlmFixtureResolver{
				results: map[string]nlmcatalog.Result{
					"Owner Journal": nlmResult(
						"1001",
						"Owner Journal",
						"1474-1776",
						"1474-1784",
					),
					"Polluted Journal": nlmResult(
						"1002",
						"Polluted Journal",
						"1474-175X",
						"1474-1768",
					),
				},
			}
			factoryCalls := 0
			rows, receipts, err := ReconcileConflictingISSNsWithNLMCatalog(
				context.Background(),
				test.rows,
				func() (NLMCatalogResolver, error) {
					factoryCalls++
					return resolver, nil
				},
			)
			if err != nil {
				t.Fatalf(
					"ReconcileConflictingISSNsWithNLMCatalog() error = %v",
					err,
				)
			}
			if !slices.Equal(resolver.calls, test.wantCalls) {
				t.Fatalf(
					"resolver calls = %v, want %v",
					resolver.calls,
					test.wantCalls,
				)
			}
			wantFactoryCalls := 0
			if len(test.wantCalls) > 0 {
				wantFactoryCalls = 1
			}
			if factoryCalls != wantFactoryCalls {
				t.Fatalf(
					"factory calls = %d, want %d",
					factoryCalls,
					wantFactoryCalls,
				)
			}
			if len(receipts) != 3 && len(test.wantCalls) > 0 {
				t.Fatalf("receipts = %d, want one per conflicting source row", len(receipts))
			}
			if len(receipts) != 0 && len(test.wantCalls) == 0 {
				t.Fatalf("receipts = %#v, want none", receipts)
			}
			if len(rows) != len(test.rows) {
				t.Fatalf("rows = %d, want %d", len(rows), len(test.rows))
			}
		})
	}
}

func TestReconcileConflictingISSNsCorrectsPollutedSupersetAndPreservesCrossrefEvidence(
	t *testing.T,
) {
	t.Parallel()

	owner := nlmRegistryRow(
		"medicine",
		1,
		"Nature Reviews Drug Discovery",
		"1474-1776",
		"1474-1784",
		[]string{"1474-1776", "1474-1784"},
	)
	polluted := nlmRegistryRow(
		"medicine",
		2,
		"Nature Reviews Cancer",
		"1474-1768",
		"1474-1768",
		[]string{"1474-175X", "1474-1768", "1474-1776", "1474-1784"},
	)
	polluted.CrossrefTitle = "Nature Reviews Cancer"
	polluted.CrossrefPublisher = "Springer Science and Business Media LLC"
	polluted.CrossrefTotalDOIs = 4321

	resolver := &nlmFixtureResolver{
		results: map[string]nlmcatalog.Result{
			owner.SourceJournalName: nlmResult(
				"101",
				owner.SourceJournalName,
				"1474-1776",
				"1474-1784",
			),
			polluted.SourceJournalName: nlmResult(
				"102",
				polluted.SourceJournalName,
				"1474-175X",
				"1474-1768",
			),
		},
	}
	rows, receipts, err := ReconcileConflictingISSNsWithNLMCatalog(
		context.Background(),
		[]RegistryRow{owner, polluted},
		func() (NLMCatalogResolver, error) { return resolver, nil },
	)
	if err != nil {
		t.Fatalf("ReconcileConflictingISSNsWithNLMCatalog() error = %v", err)
	}
	if len(rows) != 2 || len(receipts) != 2 {
		t.Fatalf("rows/receipts = %d/%d, want 2/2", len(rows), len(receipts))
	}
	got := rows[1]
	if got.MatchStatus != MatchStatusResolved ||
		got.PrintISSN != "1474-175X" ||
		got.EISSN != "1474-1768" ||
		!slices.Equal(got.AllISSNs, []string{"1474-175X", "1474-1768"}) {
		t.Fatalf("corrected polluted row = %#v", got)
	}
	if got.CrossrefTitle != polluted.CrossrefTitle ||
		got.CrossrefPublisher != polluted.CrossrefPublisher ||
		got.CrossrefTotalDOIs != polluted.CrossrefTotalDOIs ||
		got.CrossrefSupported != polluted.CrossrefSupported ||
		got.MatchMethod != polluted.MatchMethod {
		t.Fatalf("Crossref evidence changed: before=%#v after=%#v", polluted, got)
	}
	receipt := receipts[1]
	if receipt.Domain != polluted.Domain ||
		receipt.SourceOrder != polluted.SourceOrder ||
		receipt.SourceJournalName != polluted.SourceJournalName ||
		receipt.NLMUID != "102" ||
		receipt.NLMTitle != "Nature Reviews Cancer" ||
		receipt.DateRevised != "2026-05-08" ||
		!slices.Equal(
			receipt.OriginalISSNs,
			[]string{"1474-175X", "1474-1768", "1474-1776", "1474-1784"},
		) ||
		!slices.Equal(
			receipt.AuthorityISSNs,
			[]string{"1474-175X", "1474-1768"},
		) ||
		receipt.ESearchSHA256 != nlmTestSHAOne ||
		receipt.ESummarySHA256 != nlmTestSHATwo {
		t.Fatalf("polluted receipt = %#v", receipt)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("receipt.Validate() error = %v", err)
	}
}

func TestReconcileConflictingISSNsFailsClosedOnInsufficientAuthority(
	t *testing.T,
) {
	t.Parallel()

	rows := []RegistryRow{
		nlmRegistryRow(
			"medicine",
			1,
			"Owner Journal",
			"1474-1776",
			"1474-1784",
			[]string{"1474-1776", "1474-1784"},
		),
		nlmRegistryRow(
			"medicine",
			2,
			"Polluted Journal",
			"1474-175X",
			"1474-1768",
			[]string{"1474-175X", "1474-1768", "1474-1776", "1474-1784"},
		),
	}
	tests := []struct {
		name    string
		results map[string]nlmcatalog.Result
		errs    map[string]error
		want    string
	}{
		{
			name: "titlemainsort mismatch",
			results: map[string]nlmcatalog.Result{
				"Owner Journal": nlmResult(
					"201",
					"Different Owner",
					"1474-1776",
					"1474-1784",
				),
			},
			want: "titlemainsort",
		},
		{
			name: "authority ISSN outside Crossref candidate",
			results: map[string]nlmcatalog.Result{
				"Owner Journal": nlmResult(
					"202",
					"Owner Journal",
					"0028-0836",
					"",
				),
			},
			want: "subset",
		},
		{
			name: "resolver network error",
			errs: map[string]error{
				"Owner Journal": errors.New("NLM network unavailable"),
			},
			want: "network unavailable",
		},
		{
			name: "invalid authority role identity",
			results: map[string]nlmcatalog.Result{
				"Owner Journal": func() nlmcatalog.Result {
					result := nlmResult(
						"203",
						"Owner Journal",
						"1474-1776",
						"",
					)
					result.ElectronicISSN = result.PrintISSN
					return result
				}(),
			},
			want: "role",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolver := &nlmFixtureResolver{
				results: test.results,
				errs:    test.errs,
			}
			got, receipts, err := ReconcileConflictingISSNsWithNLMCatalog(
				context.Background(),
				rows,
				func() (NLMCatalogResolver, error) { return resolver, nil },
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("reconcile error = %v, want %q", err, test.want)
			}
			if got != nil || receipts != nil {
				t.Fatalf(
					"failed reconciliation returned partial rows/receipts: %#v/%#v",
					got,
					receipts,
				)
			}
			if !slices.Equal(
				rows[0].AllISSNs,
				[]string{"1474-1776", "1474-1784"},
			) {
				t.Fatalf("input rows mutated after failure: %#v", rows[0])
			}
		})
	}
}

func TestReconcileConflictingISSNsFailsAtomicallyWhenConnectedCorrectionsStillIntersect(
	t *testing.T,
) {
	t.Parallel()

	rows := []RegistryRow{
		nlmRegistryRow(
			"medicine",
			1,
			"Connected Journal A",
			"0028-0836",
			"2049-3630",
			[]string{"0028-0836", "2049-3630"},
		),
		nlmRegistryRow(
			"medicine",
			2,
			"Connected Journal B",
			"2049-3630",
			"3141-592X",
			[]string{"2049-3630", "3141-592X"},
		),
		nlmRegistryRow(
			"medicine",
			3,
			"Connected Journal C",
			"3141-592X",
			"0364-2313",
			[]string{"0364-2313", "3141-592X"},
		),
	}
	original := cloneRegistryRows(rows)
	resolver := &nlmFixtureResolver{
		results: map[string]nlmcatalog.Result{
			"Connected Journal A": nlmResult(
				"251",
				"Connected Journal A",
				"",
				"2049-3630",
			),
			"Connected Journal B": nlmResult(
				"252",
				"Connected Journal B",
				"2049-3630",
				"3141-592X",
			),
			"Connected Journal C": nlmResult(
				"253",
				"Connected Journal C",
				"3141-592X",
				"",
			),
		},
	}

	got, receipts, err := ReconcileConflictingISSNsWithNLMCatalog(
		context.Background(),
		rows,
		func() (NLMCatalogResolver, error) { return resolver, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "left a non-identical") {
		t.Fatalf("reconcile error = %v, want final intersection failure", err)
	}
	if got != nil || receipts != nil {
		t.Fatalf(
			"failed connected reconciliation returned partial rows/receipts: %#v/%#v",
			got,
			receipts,
		)
	}
	if !slices.Equal(
		resolver.calls,
		[]string{
			"Connected Journal A",
			"Connected Journal B",
			"Connected Journal C",
		},
	) {
		t.Fatalf("resolver calls = %v, want all connected participants", resolver.calls)
	}
	for index := range rows {
		if !slices.Equal(rows[index].AllISSNs, original[index].AllISSNs) ||
			rows[index].PrintISSN != original[index].PrintISSN ||
			rows[index].EISSN != original[index].EISSN {
			t.Fatalf(
				"input row %d mutated: before=%#v after=%#v",
				index,
				original[index],
				rows[index],
			)
		}
	}
}

func TestReconcileConflictingISSNsRealFourGroupRegression(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		ownerTitle      string
		ownerPrint      string
		ownerElectronic string
		pollutedTitle   string
		pollutedPrint   string
		pollutedEISSN   string
		pollutedAll     []string
	}{
		{
			ownerTitle:      "Nature Reviews Drug Discovery",
			ownerPrint:      "1474-1776",
			ownerElectronic: "1474-1784",
			pollutedTitle:   "Nature Reviews Cancer",
			pollutedPrint:   "1474-175X",
			pollutedEISSN:   "1474-1768",
			pollutedAll: []string{
				"1474-175X",
				"1474-1768",
				"1474-1776",
				"1474-1784",
			},
		},
		{
			ownerTitle:      "British Journal of Pharmacology",
			ownerPrint:      "0007-1188",
			ownerElectronic: "1476-5381",
			pollutedTitle:   "Bone Marrow Transplantation",
			pollutedPrint:   "0268-3369",
			pollutedEISSN:   "1476-5365",
			pollutedAll: []string{
				"0268-3369",
				"1476-5365",
				"1476-5381",
			},
		},
		{
			ownerTitle:      "Molecular Medicine",
			ownerPrint:      "1076-1551",
			ownerElectronic: "1528-3658",
			pollutedTitle:   "World Journal of Surgery",
			pollutedPrint:   "0364-2313",
			pollutedEISSN:   "1432-2323",
			pollutedAll: []string{
				"0364-2313",
				"1076-1551",
				"1431-0651",
				"1432-2323",
			},
		},
		{
			ownerTitle:      "Nature Methods",
			ownerPrint:      "1548-7091",
			ownerElectronic: "1548-7105",
			pollutedTitle:   "Nature Chemical Biology",
			pollutedPrint:   "1552-4450",
			pollutedEISSN:   "1552-4469",
			pollutedAll: []string{
				"1548-7091",
				"1548-7105",
				"1552-4450",
				"1552-4469",
			},
		},
	}

	rows := make([]RegistryRow, 0, len(fixtures)*2)
	results := make(map[string]nlmcatalog.Result, len(fixtures)*2)
	uid := 300
	for index, fixture := range fixtures {
		order := index*2 + 1
		ownerISSNs := []string{fixture.ownerPrint, fixture.ownerElectronic}
		slices.Sort(ownerISSNs)
		rows = append(rows,
			nlmRegistryRow(
				"medicine",
				order,
				fixture.ownerTitle,
				fixture.ownerPrint,
				fixture.ownerElectronic,
				ownerISSNs,
			),
			nlmRegistryRow(
				"medicine",
				order+1,
				fixture.pollutedTitle,
				fixture.pollutedPrint,
				fixture.pollutedEISSN,
				fixture.pollutedAll,
			),
		)
		uid++
		results[fixture.ownerTitle] = nlmResult(
			fmt.Sprint(uid),
			fixture.ownerTitle,
			fixture.ownerPrint,
			fixture.ownerElectronic,
		)
		uid++
		results[fixture.pollutedTitle] = nlmResult(
			fmt.Sprint(uid),
			fixture.pollutedTitle,
			fixture.pollutedPrint,
			fixture.pollutedEISSN,
		)
	}
	resolver := &nlmFixtureResolver{results: results}
	got, receipts, err := ReconcileConflictingISSNsWithNLMCatalog(
		context.Background(),
		rows,
		func() (NLMCatalogResolver, error) { return resolver, nil },
	)
	if err != nil {
		t.Fatalf("ReconcileConflictingISSNsWithNLMCatalog() error = %v", err)
	}
	if len(got) != 8 || len(receipts) != 8 || len(resolver.calls) != 8 {
		t.Fatalf(
			"rows/receipts/calls = %d/%d/%d, want 8/8/8",
			len(got),
			len(receipts),
			len(resolver.calls),
		)
	}
	for index, fixture := range fixtures {
		owner := got[index*2]
		polluted := got[index*2+1]
		if owner.MatchStatus != MatchStatusResolved ||
			polluted.MatchStatus != MatchStatusResolved {
			t.Fatalf("fixture %d lost resolved status", index)
		}
		if polluted.PrintISSN != fixture.pollutedPrint ||
			polluted.EISSN != fixture.pollutedEISSN ||
			!slices.Equal(
				polluted.AllISSNs,
				sortedStrings(fixture.pollutedPrint, fixture.pollutedEISSN),
			) {
			t.Fatalf("fixture %d polluted result = %#v", index, polluted)
		}
	}
	assertNoNonIdenticalResolvedISSNIntersections(t, got)
}

type nlmFixtureResolver struct {
	results map[string]nlmcatalog.Result
	errs    map[string]error
	calls   []string
}

func (resolver *nlmFixtureResolver) Resolve(
	_ context.Context,
	query nlmcatalog.Query,
) (nlmcatalog.Result, error) {
	resolver.calls = append(resolver.calls, query.Title)
	if err := resolver.errs[query.Title]; err != nil {
		return nlmcatalog.Result{}, err
	}
	result, exists := resolver.results[query.Title]
	if !exists {
		return nlmcatalog.Result{}, fmt.Errorf(
			"missing fixture for %q",
			query.Title,
		)
	}
	return result, nil
}

const (
	nlmTestSHAOne = "1111111111111111111111111111111111111111111111111111111111111111"
	nlmTestSHATwo = "2222222222222222222222222222222222222222222222222222222222222222"
)

func nlmResult(
	uid string,
	title string,
	printISSN string,
	electronicISSN string,
) nlmcatalog.Result {
	normalized, err := NormalizeTitle(title)
	if err != nil {
		panic(err)
	}
	allISSNs := sortedStrings(printISSN, electronicISSN)
	return nlmcatalog.Result{
		UID:           uid,
		NLMUniqueID:   uid,
		TitleMainSort: normalized,
		TitleMainList: []nlmcatalog.TitleMain{
			{Title: title, SortTitle: normalized},
		},
		PrintISSN:             printISSN,
		ElectronicISSN:        electronicISSN,
		AllISSNs:              allISSNs,
		DateRevised:           "2026-05-08",
		EndYear:               "9999",
		CurrentIndexingStatus: "Y",
		ESearchSHA256:         nlmTestSHAOne,
		ESummarySHA256:        nlmTestSHATwo,
	}
}

func nlmRegistryRow(
	domain string,
	order int,
	title string,
	printISSN string,
	electronicISSN string,
	allISSNs []string,
) RegistryRow {
	return RegistryRow{
		SourceRow: SourceRow{
			Domain:            domain,
			SourceOrder:       order,
			SourceJournalName: title,
			SourceURL:         fmt.Sprintf("https://example.test/%s/%d", domain, order),
		},
		PrintISSN:         printISSN,
		EISSN:             electronicISSN,
		AllISSNs:          slices.Clone(allISSNs),
		CrossrefTitle:     title,
		CrossrefPublisher: "Fixture Publisher",
		CrossrefTotalDOIs: 100,
		CrossrefSupported: SupportStatusYes,
		OpenAlexSupported: SupportStatusUnknown,
		PubMedSupported:   SupportStatusUnknown,
		MatchMethod:       MatchMethodCrossrefExactNormalizedTitle,
		MatchStatus:       MatchStatusResolved,
	}
}

func sortedStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return result
}

func assertNoNonIdenticalResolvedISSNIntersections(
	t *testing.T,
	rows []RegistryRow,
) {
	t.Helper()
	for left := range rows {
		if rows[left].MatchStatus != MatchStatusResolved {
			continue
		}
		for right := left + 1; right < len(rows); right++ {
			if rows[right].MatchStatus != MatchStatusResolved ||
				slices.Equal(rows[left].AllISSNs, rows[right].AllISSNs) {
				continue
			}
			for _, issn := range rows[left].AllISSNs {
				if slices.Contains(rows[right].AllISSNs, issn) {
					t.Fatalf(
						"rows %d and %d retain non-identical intersection on %s",
						left,
						right,
						issn,
					)
				}
			}
		}
	}
}
