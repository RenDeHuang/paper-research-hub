package urlverify

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

func TestHostPolicyAllowsOnlyExactExplicitHosts(t *testing.T) {
	t.Parallel()

	policy, err := NewHostPolicy(
		"official-url/v1",
		[]string{"doi.org", "preprints.example.test"},
	)
	if err != nil {
		t.Fatalf("NewHostPolicy() error = %v", err)
	}
	for _, hostname := range []string{
		"doi.org",
		"DOI.ORG",
		"preprints.example.test",
	} {
		if !policy.Allows(hostname) {
			t.Fatalf("HostPolicy.Allows(%q) = false", hostname)
		}
	}
	for _, hostname := range []string{
		"",
		"evil.doi.org",
		"doi.org.evil.test",
		"127.0.0.1",
	} {
		if policy.Allows(hostname) {
			t.Fatalf("HostPolicy.Allows(%q) = true", hostname)
		}
	}

	hosts := policy.AllowedHosts()
	hosts[0] = "mutated.example.test"
	if policy.Allows("mutated.example.test") {
		t.Fatal("HostPolicy exposed mutable allowed-host state")
	}
}

type officialURLHostRegistryFixture struct {
	channel  scope.ContentChannel
	role     LinkRole
	hostname string
}

func importOfficialURLHostRegistry(
	t *testing.T,
	pool *pgxpool.Pool,
	policyVersion string,
	hosts []officialURLHostRegistryFixture,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin official URL host Registry import: %v", err)
	}
	defer tx.Rollback(context.Background())
	fileSHA256 := fmt.Sprintf(
		"%x",
		sha256.Sum256([]byte(fmt.Sprintf("%s:%#v", policyVersion, hosts))),
	)
	var registryVersionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO official_url_registry_versions (
			registry_name,
			policy_version,
			file_sha256,
			host_count,
			imported_at
		) VALUES (
			'official-url-hosts',
			$1,
			$2,
			$3,
			TIMESTAMPTZ '2026-07-19 00:00:00+00'
		)
		RETURNING id::text
	`, policyVersion, fileSHA256, len(hosts)).Scan(
		&registryVersionID,
	); err != nil {
		t.Fatalf("insert official URL host Registry receipt: %v", err)
	}
	for _, host := range hosts {
		if _, err := tx.Exec(ctx, `
		INSERT INTO official_url_host_registry (
			registry_version_id,
			policy_version,
			content_channel,
			link_role,
			hostname,
			registry_source,
			source_reference,
			registered_at
		) VALUES (
			$1, $2, $3, $4, $5,
			'test_fixture',
			'urlverify-host-policy-test/v1',
			TIMESTAMPTZ '2026-07-19 00:00:00+00'
		)
	`, registryVersionID, policyVersion, host.channel, host.role, host.hostname); err != nil {
			t.Fatalf("register official URL test host %q: %v", host.hostname, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE official_url_registry_versions
		SET sealed_at = TIMESTAMPTZ '2026-07-19 00:00:00+00'
		WHERE id = $1
	`, registryVersionID); err != nil {
		t.Fatalf("seal official URL host Registry receipt: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit official URL host Registry import: %v", err)
	}
}

func TestPostgresStoreLoadsOnlyPersistedCandidateHostPolicy(t *testing.T) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	fixture := insertCurrentURLProjectionFixture(
		t,
		pool,
		"host-policy-explicit",
		"normalized-record/v4",
		normalizedURLPayload(
			"official/normalized-v4",
			[]map[string]any{{
				"url": "https://policy-unregistered.example.test/" +
					"article",
				"source_path":       "$.official_url",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "official/normalized-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/host-policy-explicit",
			}},
		),
	)
	ctx := context.Background()
	candidates, err := store.ImportCurrentProjectionCandidates(
		ctx,
		CurrentProjectionRef{
			WorkID:                fixture.WorkID,
			NormalizedAssertionID: fixture.NormalizedAssertionID,
		},
	)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("import host policy candidate = %#v, %v", candidates, err)
	}
	candidate := candidates[0]

	policy, err := store.LoadHostPolicy(
		ctx,
		candidate,
		"official-url/v1",
	)
	if err != nil {
		t.Fatalf("LoadHostPolicy(unregistered) error = %v", err)
	}
	if policy.Allows("policy-unregistered.example.test") {
		t.Fatal("LoadHostPolicy guessed candidate source hostname")
	}

	importOfficialURLHostRegistry(
		t,
		pool,
		"official-url/test-explicit-v1",
		[]officialURLHostRegistryFixture{{
			channel:  candidate.Channel,
			role:     candidate.LinkRole,
			hostname: "policy-unregistered.example.test",
		}},
	)
	policy, err = store.LoadHostPolicy(
		ctx,
		candidate,
		"official-url/test-explicit-v1",
	)
	if err != nil {
		t.Fatalf("LoadHostPolicy(registered) error = %v", err)
	}
	if !policy.Allows("policy-unregistered.example.test") {
		t.Fatal("LoadHostPolicy omitted explicit registry hostname")
	}

	doiFixture := insertCurrentURLProjectionFixture(
		t,
		pool,
		"host-policy-doi",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               "https://doi.org/10.1000/host-policy-doi",
				"source_path":       "$.URL",
				"content_channel":   "journal_published",
				"link_role":         "doi_url",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/host-policy-doi",
			}},
		),
	)
	doiCandidates, err := store.ImportCurrentProjectionCandidates(
		ctx,
		CurrentProjectionRef{
			WorkID:                doiFixture.WorkID,
			NormalizedAssertionID: doiFixture.NormalizedAssertionID,
		},
	)
	if err != nil || len(doiCandidates) != 1 {
		t.Fatalf("import DOI candidate = %#v, %v", doiCandidates, err)
	}
	doiPolicy, err := store.LoadHostPolicy(
		ctx,
		doiCandidates[0],
		"official-url/v1",
	)
	if err != nil {
		t.Fatalf("LoadHostPolicy(DOI) error = %v", err)
	}
	if !doiPolicy.Allows("doi.org") ||
		doiPolicy.Allows("policy-unregistered.example.test") {
		t.Fatalf(
			"DOI HostPolicy = %#v",
			doiPolicy.AllowedHosts(),
		)
	}
}
