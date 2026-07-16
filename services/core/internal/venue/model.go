package venue

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var issnPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{3}[0-9X]$`)

type ISSNRole string

const (
	ISSNRoleLinking    ISSNRole = "issn_l"
	ISSNRolePrint      ISSNRole = "print"
	ISSNRoleElectronic ISSNRole = "electronic"
)

func (role ISSNRole) Valid() bool {
	switch role {
	case ISSNRoleLinking, ISSNRolePrint, ISSNRoleElectronic:
		return true
	default:
		return false
	}
}

type ISSN struct {
	role  ISSNRole
	value string
}

func ParseISSN(role ISSNRole, raw string) (ISSN, error) {
	if !role.Valid() {
		return ISSN{}, fmt.Errorf("invalid ISSN role %q", role)
	}

	value := strings.ToUpper(strings.TrimSpace(raw))
	if !issnPattern.MatchString(value) {
		return ISSN{}, fmt.Errorf("invalid %s ISSN format %q", role, raw)
	}
	if !validISSNChecksum(value) {
		return ISSN{}, fmt.Errorf("invalid %s ISSN checksum %q", role, raw)
	}

	return ISSN{role: role, value: value}, nil
}

func validISSNChecksum(value string) bool {
	sum := 0
	for index := range 7 {
		digit := int(value[index+index/4] - '0')
		sum += digit * (8 - index)
	}

	check := int(value[8] - '0')
	if value[8] == 'X' {
		check = 10
	}
	return (sum+check)%11 == 0
}

func (issn ISSN) Valid() bool {
	return issn.role.Valid() &&
		issnPattern.MatchString(issn.value) &&
		validISSNChecksum(issn.value)
}

func (issn ISSN) Role() ISSNRole {
	return issn.role
}

func (issn ISSN) String() string {
	if !issn.Valid() {
		return ""
	}
	return issn.value
}

type ISSNSet struct {
	linking    ISSN
	print      ISSN
	electronic ISSN
}

func NewISSNSet(values ...ISSN) (ISSNSet, error) {
	var result ISSNSet
	for _, value := range values {
		if !value.Valid() {
			return ISSNSet{}, errors.New("ISSN set contains an invalid value")
		}
		current, exists := result.Get(value.Role())
		if exists {
			if current == value {
				continue
			}
			return ISSNSet{}, fmt.Errorf(
				"ISSN role %s has conflicting values %q and %q",
				value.Role(),
				current.String(),
				value.String(),
			)
		}

		switch value.Role() {
		case ISSNRoleLinking:
			result.linking = value
		case ISSNRolePrint:
			result.print = value
		case ISSNRoleElectronic:
			result.electronic = value
		}
	}
	return result, nil
}

func (set ISSNSet) Empty() bool {
	return !set.linking.Valid() && !set.print.Valid() && !set.electronic.Valid()
}

func (set ISSNSet) Get(role ISSNRole) (ISSN, bool) {
	var value ISSN
	switch role {
	case ISSNRoleLinking:
		value = set.linking
	case ISSNRolePrint:
		value = set.print
	case ISSNRoleElectronic:
		value = set.electronic
	default:
		return ISSN{}, false
	}
	return value, value.Valid()
}

func (set ISSNSet) Values() []ISSN {
	result := make([]ISSN, 0, 3)
	for _, role := range []ISSNRole{
		ISSNRoleLinking,
		ISSNRolePrint,
		ISSNRoleElectronic,
	} {
		if value, ok := set.Get(role); ok {
			result = append(result, value)
		}
	}
	return result
}

func (set ISSNSet) ExactValues() []string {
	seen := make(map[string]struct{}, 3)
	result := make([]string, 0, 3)
	for _, value := range set.Values() {
		canonical := value.String()
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	return result
}

func (set ISSNSet) Intersects(other ISSNSet) bool {
	candidates := other.ExactValues()
	for _, controlled := range set.ExactValues() {
		if slices.Contains(candidates, controlled) {
			return true
		}
	}
	return false
}

type Alias struct {
	value  string
	source string
}

func NewAlias(value, source string) (Alias, error) {
	normalizedValue := strings.TrimSpace(value)
	if normalizedValue == "" {
		return Alias{}, errors.New("venue alias is required")
	}
	normalizedSource := strings.TrimSpace(source)
	if normalizedSource == "" {
		return Alias{}, errors.New("venue alias source is required")
	}
	return Alias{value: normalizedValue, source: normalizedSource}, nil
}

func (alias Alias) Value() string {
	return alias.value
}

func (alias Alias) Source() string {
	return alias.source
}

func (alias Alias) Valid() bool {
	return strings.TrimSpace(alias.value) != "" && strings.TrimSpace(alias.source) != ""
}

type VenueType string

const (
	VenueTypeJournal    VenueType = "journal"
	VenueTypeConference VenueType = "conference"
	VenueTypePreprint   VenueType = "preprint"
	VenueTypeRepository VenueType = "repository"
)

func (venueType VenueType) Valid() bool {
	switch venueType {
	case VenueTypeJournal, VenueTypeConference, VenueTypePreprint, VenueTypeRepository:
		return true
	default:
		return false
	}
}

type Venue struct {
	id          string
	venueType   VenueType
	identifiers ISSNSet
	aliases     []Alias
}

func NewVenue(
	id string,
	venueType VenueType,
	identifiers ISSNSet,
	aliases []Alias,
) (Venue, error) {
	normalizedID := strings.TrimSpace(id)
	if normalizedID == "" {
		return Venue{}, errors.New("venue ID is required")
	}
	if !venueType.Valid() {
		return Venue{}, fmt.Errorf("invalid venue type %q", venueType)
	}
	if venueType == VenueTypeJournal && identifiers.Empty() {
		return Venue{}, errors.New("journal venue requires at least one ISSN")
	}
	if len(aliases) == 0 {
		return Venue{}, errors.New("venue requires at least one title alias")
	}
	for _, alias := range aliases {
		if !alias.Valid() {
			return Venue{}, errors.New("venue contains an invalid title alias")
		}
	}

	return Venue{
		id:          normalizedID,
		venueType:   venueType,
		identifiers: identifiers,
		aliases:     slices.Clone(aliases),
	}, nil
}

func (venue Venue) ID() string {
	return venue.id
}

func (venue Venue) Type() VenueType {
	return venue.venueType
}

func (venue Venue) ISSNs() ISSNSet {
	return venue.identifiers
}

func (venue Venue) Aliases() []Alias {
	return slices.Clone(venue.aliases)
}

func (venue Venue) MatchesISSNs(candidates ISSNSet) bool {
	return venue.identifiers.Intersects(candidates)
}

func (venue Venue) MatchesIdentity(raw string) bool {
	candidate := strings.ToUpper(strings.TrimSpace(raw))
	return slices.Contains(venue.identifiers.ExactValues(), candidate)
}
