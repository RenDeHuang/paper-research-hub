package urlverify

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

var officialHostnamePattern = regexp.MustCompile(
	`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`,
)

var ErrHostRegistryNotReady = errors.New(
	"official URL host Registry is missing or unsealed",
)

type HostPolicy struct {
	policyVersion string
	allowedHosts  []string
}

func NewHostPolicy(
	policyVersion string,
	allowedHosts []string,
) (HostPolicy, error) {
	if !exactNonEmpty(policyVersion) {
		return HostPolicy{}, errors.New(
			"official URL host policy requires an exact policy version",
		)
	}
	normalized := make([]string, 0, len(allowedHosts))
	for _, hostname := range allowedHosts {
		host, err := normalizeOfficialHostname(hostname)
		if err != nil {
			return HostPolicy{}, err
		}
		if !slices.Contains(normalized, host) {
			normalized = append(normalized, host)
		}
	}
	sort.Strings(normalized)
	return HostPolicy{
		policyVersion: policyVersion,
		allowedHosts:  normalized,
	}, nil
}

func (policy HostPolicy) PolicyVersion() string {
	return policy.policyVersion
}

func (policy HostPolicy) AllowedHosts() []string {
	return slices.Clone(policy.allowedHosts)
}

func (policy HostPolicy) Allows(hostname string) bool {
	if hostname == "" || hostname != strings.TrimSpace(hostname) {
		return false
	}
	normalized := strings.ToLower(hostname)
	_, found := slices.BinarySearch(policy.allowedHosts, normalized)
	return found
}

func normalizeOfficialHostname(hostname string) (string, error) {
	if hostname == "" ||
		hostname != strings.TrimSpace(hostname) ||
		hostname != strings.ToLower(hostname) ||
		len(hostname) > 253 ||
		!officialHostnamePattern.MatchString(hostname) {
		return "", fmt.Errorf(
			"invalid exact official URL registry hostname %q",
			hostname,
		)
	}
	if _, err := netip.ParseAddr(hostname); err == nil {
		return "", errors.New(
			"official URL registry hostnames must not be IP literals",
		)
	}
	return hostname, nil
}

func (store *PostgresStore) LoadHostPolicy(
	ctx context.Context,
	candidate Candidate,
	policyVersion string,
) (HostPolicy, error) {
	if err := validateStoreContext(ctx); err != nil {
		return HostPolicy{}, err
	}
	if err := store.validate(); err != nil {
		return HostPolicy{}, err
	}
	if err := candidate.Validate(); err != nil {
		return HostPolicy{}, err
	}
	if !exactNonEmpty(candidate.ID) {
		return HostPolicy{}, errors.New(
			"official URL host policy requires a persisted candidate",
		)
	}
	if !exactNonEmpty(policyVersion) {
		return HostPolicy{}, errors.New(
			"official URL host policy requires an exact policy version",
		)
	}
	stored, found, err := store.FindCandidate(ctx, candidate.ID)
	if err != nil {
		return HostPolicy{}, err
	}
	if !found {
		return HostPolicy{}, ErrCandidateNotFound
	}
	if stored != candidate {
		return HostPolicy{}, errors.New(
			"official URL host policy candidate conflicts with persisted state",
		)
	}

	var sealed bool
	err = store.pool.QueryRow(ctx, `
		SELECT sealed_at IS NOT NULL
		FROM official_url_registry_versions
		WHERE policy_version = $1
	`, policyVersion).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !sealed) {
		return HostPolicy{}, ErrHostRegistryNotReady
	}
	if err != nil {
		return HostPolicy{}, fmt.Errorf(
			"load official URL host Registry receipt: %w",
			err,
		)
	}

	rows, err := store.pool.Query(ctx, `
		SELECT registry.hostname
		FROM official_url_host_registry AS registry
		JOIN official_url_registry_versions AS version
		  ON version.id = registry.registry_version_id
		 AND version.policy_version = registry.policy_version
		WHERE version.policy_version = $1
		  AND version.sealed_at IS NOT NULL
		  AND registry.content_channel = $2
		  AND registry.link_role = $3
		ORDER BY registry.hostname
	`, policyVersion, candidate.Channel, candidate.LinkRole)
	if err != nil {
		return HostPolicy{}, fmt.Errorf(
			"load official URL host registry: %w",
			err,
		)
	}
	defer rows.Close()
	hosts := make([]string, 0)
	for rows.Next() {
		var hostname string
		if err := rows.Scan(&hostname); err != nil {
			return HostPolicy{}, fmt.Errorf(
				"scan official URL registry hostname: %w",
				err,
			)
		}
		hosts = append(hosts, hostname)
	}
	if err := rows.Err(); err != nil {
		return HostPolicy{}, fmt.Errorf(
			"iterate official URL registry hostnames: %w",
			err,
		)
	}
	return NewHostPolicy(policyVersion, hosts)
}
