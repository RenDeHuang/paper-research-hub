package scope

import (
	"bytes"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestChannelIdentityIsClosedAndExact(t *testing.T) {
	t.Parallel()

	if got, want := ContentChannels(), []ContentChannel{
		ContentChannelJournalPublished,
		ContentChannelAcceptedEarly,
		ContentChannelPreprint,
		ContentChannelConferenceProceeding,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ContentChannels() = %#v, want %#v", got, want)
	}
	for _, channel := range ContentChannels() {
		parsed, err := ParseContentChannel(string(channel))
		if err != nil || parsed != channel {
			t.Fatalf("ParseContentChannel(%q) = %q, %v", channel, parsed, err)
		}
	}
	for _, value := range []string{
		"journal",
		"accepted",
		"conference",
		"Journal_Published",
		" preprint",
		"preprint ",
		"",
	} {
		if _, err := ParseContentChannel(value); !errors.Is(err, ErrInvalidContentChannel) {
			t.Fatalf("ParseContentChannel(%q) error = %v", value, err)
		}
	}
}

func TestChannelPreprintRegistryMatchesExactSourceDomainAndOfficialHost(t *testing.T) {
	t.Parallel()

	contents := []byte(
		"registry_name,registry_version,source_key,display_name,identifier_scheme,official_host,allowed_domain,lifecycle\n" +
			"medpaperhub-trusted-preprints,preprint-sources/v1,biorxiv,bioRxiv,biorxiv,www.biorxiv.org,biology,active\n" +
			"medpaperhub-trusted-preprints,preprint-sources/v1,medrxiv,medRxiv,medrxiv,www.medrxiv.org,medicine,active\n" +
			"medpaperhub-trusted-preprints,preprint-sources/v1,arxiv,arXiv,arxiv,arxiv.org,biology,active\n" +
			"medpaperhub-trusted-preprints,preprint-sources/v1,arxiv,arXiv,arxiv,arxiv.org,computer_science,active\n",
	)
	registry, err := ParsePreprintRegistry(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("ParsePreprintRegistry() error = %v", err)
	}
	if registry.Name() != "medpaperhub-trusted-preprints" ||
		registry.Version() != PreprintRegistryVersion ||
		registry.SourceCount() != 3 ||
		registry.RuleCount() != 4 ||
		registry.FileSHA256() == "" {
		t.Fatalf("preprint registry metadata = %#v", registry)
	}

	entry, found := registry.Match("arxiv", ResearchDomainComputerScience)
	if !found ||
		entry.Channel() != ContentChannelPreprint ||
		entry.IdentifierScheme() != "arxiv" ||
		entry.OfficialHost() != "arxiv.org" ||
		entry.Lifecycle() != RegistryLifecycleActive {
		t.Fatalf("arxiv match = %#v, %t", entry, found)
	}
	official, _ := url.Parse("https://arxiv.org/abs/2607.12345")
	if !entry.MatchesOfficialURL(official) {
		t.Fatal("arxiv official URL did not match exact registered host")
	}
	mirror, _ := url.Parse("https://export.arxiv.org/abs/2607.12345")
	if entry.MatchesOfficialURL(mirror) {
		t.Fatal("unregistered mirror host matched trusted preprint source")
	}
	for _, test := range []struct {
		source string
		domain ResearchDomain
	}{
		{source: "ArXiv", domain: ResearchDomainComputerScience},
		{source: " arxiv", domain: ResearchDomainComputerScience},
		{source: "arxiv", domain: ResearchDomainMedicine},
		{source: "conference", domain: ResearchDomainComputerScience},
	} {
		if _, found := registry.Match(test.source, test.domain); found {
			t.Fatalf("Match(%q, %q) found = true", test.source, test.domain)
		}
	}
}

func TestChannelConferenceRegistryRequiresExactSeriesEventAndReviewedSource(t *testing.T) {
	t.Parallel()

	contents := []byte(
		"registry_name,registry_version,provider,series_key,series_name,event_key,event_name,event_year,identifier_scheme,identifier_value,official_host,allowed_domain,lifecycle,reviewed\n" +
			"medpaperhub-conferences,conference-venues/v1,ieee,cvpr,IEEE/CVF Conference on Computer Vision and Pattern Recognition,cvpr-2026,CVPR 2026,2026,doi_prefix,10.1109,ieeexplore.ieee.org,computer_science,active,false\n" +
			"medpaperhub-conferences,conference-venues/v1,acm,sigkdd,ACM SIGKDD Conference on Knowledge Discovery and Data Mining,kdd-2026,KDD 2026,2026,doi_prefix,10.1145,dl.acm.org,computer_science,active,false\n" +
			"medpaperhub-conferences,conference-venues/v1,reviewed_allowlist,recomb,Research in Computational Molecular Biology,recomb-2026,RECOMB 2026,2026,series_issn,1528-4305,recomb.org,biology,active,true\n" +
			"medpaperhub-conferences,conference-venues/v1,reviewed_allowlist,recomb,Research in Computational Molecular Biology,recomb-2026,RECOMB 2026,2026,series_issn,1528-4305,recomb.org,computer_science,active,true\n",
	)
	registry, err := ParseConferenceRegistry(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("ParseConferenceRegistry() error = %v", err)
	}
	if registry.Version() != ConferenceRegistryVersion ||
		registry.SeriesCount() != 3 ||
		registry.EventCount() != 3 ||
		registry.RuleCount() != 4 {
		t.Fatalf("conference registry metadata = %#v", registry)
	}

	entry, found := registry.Match(
		"reviewed_allowlist",
		"recomb",
		"recomb-2026",
		ResearchDomainBiology,
	)
	if !found || !entry.Reviewed() ||
		entry.Channel() != ContentChannelConferenceProceeding ||
		entry.IdentifierScheme() != "series_issn" ||
		entry.IdentifierValue() != "1528-4305" {
		t.Fatalf("reviewed conference match = %#v, %t", entry, found)
	}
	official, _ := url.Parse("https://recomb.org/recomb2026/")
	if !entry.MatchesOfficialURL(official) {
		t.Fatal("reviewed conference official URL did not match")
	}
	for _, provider := range []string{"IEEE", "reviewed", " reviewed_allowlist"} {
		if _, found := registry.Match(
			provider,
			"recomb",
			"recomb-2026",
			ResearchDomainBiology,
		); found {
			t.Fatalf("normalized provider %q unexpectedly matched", provider)
		}
	}
	if _, found := registry.Match(
		"reviewed_allowlist",
		"RECOMB",
		"recomb-2026",
		ResearchDomainBiology,
	); found {
		t.Fatal("conference acronym normalization unexpectedly matched")
	}
	if _, found := registry.Match(
		"reviewed_allowlist",
		"recomb",
		"recomb-2025",
		ResearchDomainBiology,
	); found {
		t.Fatal("unknown conference event unexpectedly matched")
	}
}

func TestRegistryCommittedFilesParseWithFrozenVersions(t *testing.T) {
	t.Parallel()

	root := repositoryRootFromScopeTest(t)
	tests := []struct {
		path    string
		version string
		parse   func([]byte) (string, error)
	}{
		{
			path:    filepath.Join(root, "data", "subjects", "research-domains-jcr-subjects.v2.csv"),
			version: ResearchDomainRegistryVersion,
			parse: func(contents []byte) (string, error) {
				registry, err := ParseDomainRegistry(bytes.NewReader(contents))
				return registry.Version(), err
			},
		},
		{
			path:    filepath.Join(root, "data", "sources", "preprint-sources.v1.csv"),
			version: PreprintRegistryVersion,
			parse: func(contents []byte) (string, error) {
				registry, err := ParsePreprintRegistry(bytes.NewReader(contents))
				return registry.Version(), err
			},
		},
		{
			path:    filepath.Join(root, "data", "venues", "conference-venues.v1.csv"),
			version: ConferenceRegistryVersion,
			parse: func(contents []byte) (string, error) {
				registry, err := ParseConferenceRegistry(bytes.NewReader(contents))
				return registry.Version(), err
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(filepath.Base(test.path), func(t *testing.T) {
			t.Parallel()
			contents, err := os.ReadFile(test.path)
			if err != nil {
				t.Fatalf("read committed Registry %s: %v", test.path, err)
			}
			version, err := test.parse(contents)
			if err != nil {
				t.Fatalf("parse committed Registry %s: %v", test.path, err)
			}
			if version != test.version {
				t.Fatalf("Registry version = %q, want %q", version, test.version)
			}
		})
	}
}

func TestRegistryCSVContractsRejectRepairsAndUnreviewedProviders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		parse func(string) error
		csv   string
		want  string
	}{
		{
			name: "preprint source whitespace",
			parse: func(value string) error {
				_, err := ParsePreprintRegistry(strings.NewReader(value))
				return err
			},
			csv: "registry_name,registry_version,source_key,display_name,identifier_scheme,official_host,allowed_domain,lifecycle\n" +
				"medpaperhub-trusted-preprints,preprint-sources/v1, arxiv,arXiv,arxiv,arxiv.org,computer_science,active\n",
			want: "source_key",
		},
		{
			name: "preprint host scheme included",
			parse: func(value string) error {
				_, err := ParsePreprintRegistry(strings.NewReader(value))
				return err
			},
			csv: "registry_name,registry_version,source_key,display_name,identifier_scheme,official_host,allowed_domain,lifecycle\n" +
				"medpaperhub-trusted-preprints,preprint-sources/v1,arxiv,arXiv,arxiv,https://arxiv.org,computer_science,active\n",
			want: "official_host",
		},
		{
			name: "conference unknown provider",
			parse: func(value string) error {
				_, err := ParseConferenceRegistry(strings.NewReader(value))
				return err
			},
			csv: "registry_name,registry_version,provider,series_key,series_name,event_key,event_name,event_year,identifier_scheme,identifier_value,official_host,allowed_domain,lifecycle,reviewed\n" +
				"medpaperhub-conferences,conference-venues/v1,springer,recomb,RECOMB,recomb-2026,RECOMB 2026,2026,series_issn,1528-4305,recomb.org,biology,active,true\n",
			want: "provider",
		},
		{
			name: "reviewed provider without review",
			parse: func(value string) error {
				_, err := ParseConferenceRegistry(strings.NewReader(value))
				return err
			},
			csv: "registry_name,registry_version,provider,series_key,series_name,event_key,event_name,event_year,identifier_scheme,identifier_value,official_host,allowed_domain,lifecycle,reviewed\n" +
				"medpaperhub-conferences,conference-venues/v1,reviewed_allowlist,recomb,RECOMB,recomb-2026,RECOMB 2026,2026,series_issn,1528-4305,recomb.org,biology,active,false\n",
			want: "reviewed",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.parse(test.csv)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func repositoryRootFromScopeTest(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve scope test path")
	}
	return filepath.Clean(filepath.Join(
		filepath.Dir(currentFile),
		"..",
		"..",
		"..",
		"..",
	))
}
