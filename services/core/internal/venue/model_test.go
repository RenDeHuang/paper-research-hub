package venue

import (
	"slices"
	"strings"
	"testing"
)

func TestParseISSNValidatesChecksumAndPreservesRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		role ISSNRole
		raw  string
		want string
	}{
		{name: "linking", role: ISSNRoleLinking, raw: " 1234-5679 ", want: "1234-5679"},
		{name: "print", role: ISSNRolePrint, raw: "2049-3630", want: "2049-3630"},
		{name: "electronic uppercase checksum", role: ISSNRoleElectronic, raw: "3141-592x", want: "3141-592X"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseISSN(tt.role, tt.raw)
			if err != nil {
				t.Fatalf("ParseISSN(%q, %q) error = %v", tt.role, tt.raw, err)
			}
			if got.Role() != tt.role {
				t.Fatalf("ISSN.Role() = %q, want %q", got.Role(), tt.role)
			}
			if got.String() != tt.want {
				t.Fatalf("ISSN.String() = %q, want %q", got.String(), tt.want)
			}
			if !got.Valid() {
				t.Fatal("parsed ISSN is not valid")
			}
		})
	}
}

func TestParseISSNRejectsMalformedOrChecksumInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		role ISSNRole
		raw  string
	}{
		{name: "invalid role", role: ISSNRole("primary"), raw: "1234-5679"},
		{name: "empty", role: ISSNRolePrint, raw: ""},
		{name: "missing hyphen", role: ISSNRolePrint, raw: "12345679"},
		{name: "wrong group width", role: ISSNRolePrint, raw: "123-45679"},
		{name: "non digit body", role: ISSNRolePrint, raw: "12A4-5679"},
		{name: "checksum x before final", role: ISSNRolePrint, raw: "123X-5679"},
		{name: "invalid checksum", role: ISSNRolePrint, raw: "1234-567X"},
		{name: "trailing text", role: ISSNRolePrint, raw: "1234-5679 print"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := ParseISSN(tt.role, tt.raw); err == nil {
				t.Fatalf("ParseISSN(%q, %q) = %#v, want error", tt.role, tt.raw, got)
			}
		})
	}
}

func TestISSNSetEnforcesOneValuePerRoleButAllowsSameISSNAcrossRoles(t *testing.T) {
	t.Parallel()

	linking := mustParseISSN(t, ISSNRoleLinking, "1234-5679")
	printISSN := mustParseISSN(t, ISSNRolePrint, "1234-5679")
	electronic := mustParseISSN(t, ISSNRoleElectronic, "2049-3630")

	set, err := NewISSNSet(linking, printISSN, electronic)
	if err != nil {
		t.Fatalf("NewISSNSet() error = %v", err)
	}
	if got, ok := set.Get(ISSNRoleLinking); !ok || got != linking {
		t.Fatalf("ISSNSet linking = %#v, %v; want %#v, true", got, ok, linking)
	}
	if got, ok := set.Get(ISSNRolePrint); !ok || got != printISSN {
		t.Fatalf("ISSNSet print = %#v, %v; want %#v, true", got, ok, printISSN)
	}
	if got := set.ExactValues(); !slices.Equal(got, []string{"1234-5679", "2049-3630"}) {
		t.Fatalf("ISSNSet.ExactValues() = %#v, want unique canonical values", got)
	}

	otherPrint := mustParseISSN(t, ISSNRolePrint, "9876-5434")
	if _, err := NewISSNSet(printISSN, otherPrint); err == nil ||
		!strings.Contains(err.Error(), "print") {
		t.Fatalf("duplicate print role error = %v, want explicit role conflict", err)
	}
}

func TestVenueIdentityMatchingUsesOnlyExactISSNsAndNeverTitles(t *testing.T) {
	t.Parallel()

	identifiers, err := NewISSNSet(
		mustParseISSN(t, ISSNRoleLinking, "1234-5679"),
		mustParseISSN(t, ISSNRolePrint, "1234-5679"),
		mustParseISSN(t, ISSNRoleElectronic, "2049-3630"),
	)
	if err != nil {
		t.Fatalf("NewISSNSet() error = %v", err)
	}
	alias, err := NewAlias("Synthetic Journal of Exact Identity", "jcr")
	if err != nil {
		t.Fatalf("NewAlias() error = %v", err)
	}
	item, err := NewVenue("venue-1", VenueTypeJournal, identifiers, []Alias{alias})
	if err != nil {
		t.Fatalf("NewVenue() error = %v", err)
	}

	exactPrint, err := NewISSNSet(mustParseISSN(t, ISSNRolePrint, "1234-5679"))
	if err != nil {
		t.Fatalf("NewISSNSet(exact print) error = %v", err)
	}
	if !item.MatchesISSNs(exactPrint) {
		t.Fatal("Venue did not match an exact controlled ISSN")
	}

	differentISSN, err := NewISSNSet(mustParseISSN(t, ISSNRolePrint, "9876-5434"))
	if err != nil {
		t.Fatalf("NewISSNSet(different print) error = %v", err)
	}
	if item.MatchesISSNs(differentISSN) {
		t.Fatal("Venue matched a different ISSN")
	}
	if item.MatchesIdentity("Synthetic Journal of Exact Identity") {
		t.Fatal("Venue used a title alias as identity")
	}
	if got := item.Aliases(); !slices.Equal(got, []Alias{alias}) {
		t.Fatalf("Venue.Aliases() = %#v, want alias evidence only", got)
	}
}

func TestVenueRejectsInvalidTypeIdentityAndAlias(t *testing.T) {
	t.Parallel()

	identifiers, err := NewISSNSet(mustParseISSN(t, ISSNRoleLinking, "1234-5679"))
	if err != nil {
		t.Fatalf("NewISSNSet() error = %v", err)
	}
	validAlias, err := NewAlias("Synthetic Venue", "fixture")
	if err != nil {
		t.Fatalf("NewAlias() error = %v", err)
	}

	tests := []struct {
		name        string
		id          string
		venueType   VenueType
		identifiers ISSNSet
		aliases     []Alias
	}{
		{name: "blank id", id: " ", venueType: VenueTypeJournal, identifiers: identifiers, aliases: []Alias{validAlias}},
		{name: "invalid type", id: "venue-1", venueType: VenueType("workshop"), identifiers: identifiers, aliases: []Alias{validAlias}},
		{name: "journal without issn", id: "venue-1", venueType: VenueTypeJournal, aliases: []Alias{validAlias}},
		{name: "without alias", id: "venue-1", venueType: VenueTypeJournal, identifiers: identifiers},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := NewVenue(tt.id, tt.venueType, tt.identifiers, tt.aliases); err == nil {
				t.Fatalf("NewVenue() = %#v, want error", got)
			}
		})
	}

	if _, err := NewAlias(" ", "fixture"); err == nil {
		t.Fatal("NewAlias() accepted blank alias")
	}
	if _, err := NewAlias("Synthetic Venue", " "); err == nil {
		t.Fatal("NewAlias() accepted blank source")
	}
}

func mustParseISSN(t *testing.T, role ISSNRole, raw string) ISSN {
	t.Helper()

	value, err := ParseISSN(role, raw)
	if err != nil {
		t.Fatalf("ParseISSN(%q, %q) error = %v", role, raw, err)
	}
	return value
}
