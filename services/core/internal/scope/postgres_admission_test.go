package scope

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPostgresAdmissionStorePersistsAndReadsMissingDecision(t *testing.T) {
	pool := openScopeTestPool(t)
	fixture := insertScopeProjectionFixture(t, pool, "admission-missing")
	decidedAt := time.Date(2026, time.July, 18, 13, 0, 0, 0, time.UTC)
	decision, err := EvaluateAdmission(
		AdmissionInput{
			WorkID: fixture.workID,
			ChannelDecision: ChannelDecision{
				WorkID:        fixture.workID,
				Status:        ChannelDecisionMissing,
				PolicyVersion: "channel-projection/v1",
				DecidedAt:     decidedAt,
			},
			AdmissionPolicyVersion: ChannelAdmissionPolicyVersion,
			DomainRegistryVersion:  ResearchDomainRegistryVersion,
			DecidedAt:              decidedAt,
		},
		PreprintRegistry{},
		ConferenceRegistry{},
	)
	if err != nil {
		t.Fatalf("EvaluateAdmission(missing) error = %v", err)
	}
	if decision.Decision != AdmissionMissing ||
		decision.Reason != AdmissionReasonChannelUnresolved ||
		decision.Channel != "" ||
		decision.JournalPolicyVersion != "" ||
		decision.ChannelRegistryVersion != "" {
		t.Fatalf("missing AdmissionDecision = %#v", decision)
	}

	store, err := NewPostgresAdmissionStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresAdmissionStore() error = %v", err)
	}
	record := AdmissionRecord{
		Decision:       decision,
		SourceRecordID: fixture.sourceRecordID,
		SourcePath:     "$.channel",
	}
	first, err := store.Persist(context.Background(), record)
	if err != nil {
		t.Fatalf("Persist(first missing admission) error = %v", err)
	}
	second, err := store.Persist(context.Background(), record)
	if err != nil {
		t.Fatalf("Persist(idempotent missing admission) error = %v", err)
	}
	if first.ID == "" || second.ID != first.ID {
		t.Fatalf("persisted missing admissions = %#v / %#v", first, second)
	}

	loaded, found, err := store.Find(
		scopeTestContext(t),
		first.ID,
	)
	if err != nil {
		t.Fatalf("Find(missing admission) error = %v", err)
	}
	if !found || !reflect.DeepEqual(loaded, first) {
		t.Fatalf("Find(missing admission) = %#v, %t, want %#v", loaded, found, first)
	}

	ctx := scopeTestContext(t)
	var count int
	var channelIsNull bool
	if err := pool.QueryRow(ctx, `
		SELECT count(*) OVER (), channel IS NULL
		FROM work_channel_admission_decisions
		WHERE work_id = $1
		  AND admission_policy_version = $2
		  AND source_record_id = $3
	`, fixture.workID, ChannelAdmissionPolicyVersion, fixture.sourceRecordID).Scan(
		&count,
		&channelIsNull,
	); err != nil {
		t.Fatalf("query persisted missing admission: %v", err)
	}
	if count != 1 || !channelIsNull {
		t.Fatalf(
			"persisted missing admission = count %d channel NULL %t",
			count,
			channelIsNull,
		)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO work_channel_admission_decisions (
			work_id, channel, decision, reason, source_record_id, source_path,
			admission_policy_version, domain_registry_version,
			journal_policy_version, channel_registry_version,
			evidence, decided_at
		) VALUES (
			$1, NULL, 'missing', 'channel_unresolved', $2, '$.duplicate',
			'channel-admission/v1', 'research-domains-jcr-subjects/v2',
			NULL, NULL, '{"source_paths":[]}', $3
		)
	`, fixture.workID, fixture.sourceRecordID, decidedAt); err == nil {
		t.Fatal("duplicate NULL-channel missing admission error = nil")
	} else {
		assertScopeConstraint(
			t,
			err,
			"work_channel_admission_decisions_identity_key",
		)
	}
}

func TestAdmissionSchemaRejectsInvalidMissingChannelShapes(t *testing.T) {
	pool := openScopeTestPool(t)
	fixture := insertScopeProjectionFixture(t, pool, "admission-shape")
	ctx := scopeTestContext(t)
	decidedAt := time.Date(2026, time.July, 18, 13, 15, 0, 0, time.UTC)
	tests := []struct {
		name                   string
		channel                any
		decision               string
		reason                 string
		channelRegistryVersion any
	}{
		{
			name:                   "missing with resolved channel",
			channel:                "preprint",
			decision:               "missing",
			reason:                 "channel_unresolved",
			channelRegistryVersion: PreprintRegistryVersion,
		},
		{
			name:     "accepted without channel",
			channel:  nil,
			decision: "accepted",
			reason:   "eligible",
		},
		{
			name:     "missing with resolved reason",
			channel:  nil,
			decision: "missing",
			reason:   "eligible",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, `
				INSERT INTO work_channel_admission_decisions (
					work_id, channel, decision, reason,
					source_record_id, source_path,
					admission_policy_version, domain_registry_version,
					journal_policy_version, channel_registry_version,
					evidence, decided_at
				) VALUES (
					$1, $2, $3, $4,
					$5, '$.channel',
					'channel-admission/v1',
					'research-domains-jcr-subjects/v2',
					NULL, $6,
					'{"source_paths":[]}', $7
				)
			`,
				fixture.workID,
				test.channel,
				test.decision,
				test.reason,
				fixture.sourceRecordID,
				test.channelRegistryVersion,
				decidedAt,
			)
			if err == nil {
				t.Fatalf("invalid admission shape %#v error = nil", test)
			}
			assertScopeConstraint(
				t,
				err,
				"work_channel_admission_decisions_channel_shape_check",
			)
		})
	}
}

func TestPostgresAdmissionStoreNormalizesDecisionTimestampsToMicroseconds(
	t *testing.T,
) {
	pool := openScopeTestPool(t)
	fixture := insertScopeProjectionFixture(t, pool, "admission-timestamp")
	originalTime := time.Date(
		2026,
		time.July,
		18,
		13,
		30,
		0,
		123456789,
		time.FixedZone("UTC+08", 8*60*60),
	)
	normalizedTime := originalTime.UTC().Truncate(time.Microsecond)
	decision, err := EvaluateAdmission(
		AdmissionInput{
			WorkID: fixture.workID,
			ChannelDecision: ChannelDecision{
				WorkID:        fixture.workID,
				Status:        ChannelDecisionMissing,
				PolicyVersion: "channel-projection/v1",
				DecidedAt:     originalTime,
			},
			AdmissionPolicyVersion: ChannelAdmissionPolicyVersion,
			DomainRegistryVersion:  ResearchDomainRegistryVersion,
			DecidedAt:              originalTime,
		},
		PreprintRegistry{},
		ConferenceRegistry{},
	)
	if err != nil {
		t.Fatalf("EvaluateAdmission(nanosecond timestamp) error = %v", err)
	}

	store, err := NewPostgresAdmissionStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresAdmissionStore() error = %v", err)
	}
	originalDecision := decision
	originalDecision.DecidedAt = originalTime
	record := AdmissionRecord{
		Decision:       originalDecision,
		SourceRecordID: fixture.sourceRecordID,
		SourcePath:     "$.channel",
	}
	first, err := store.Persist(context.Background(), record)
	if err != nil {
		t.Fatalf("Persist(first nanosecond timestamp) error = %v", err)
	}
	if first.Decision.DecidedAt != normalizedTime {
		t.Fatalf(
			"Persist(first) decided_at = %s, want %s",
			first.Decision.DecidedAt,
			normalizedTime,
		)
	}
	second, err := store.Persist(context.Background(), record)
	if err != nil {
		t.Fatalf("Persist(idempotent original timestamp) error = %v", err)
	}
	if second.ID != first.ID || second.Decision.DecidedAt != normalizedTime {
		t.Fatalf("idempotent timestamp records = %#v / %#v", first, second)
	}
	loaded, found, err := store.Find(
		scopeTestContext(t),
		first.ID,
	)
	if err != nil {
		t.Fatalf("Find(normalized timestamp) error = %v", err)
	}
	if !found || !reflect.DeepEqual(loaded, first) {
		t.Fatalf("Find(normalized timestamp) = %#v, %t, want %#v", loaded, found, first)
	}

	conflicting := record
	conflicting.Decision.DecidedAt = originalTime.Add(time.Microsecond)
	if _, err := store.Persist(
		context.Background(),
		conflicting,
	); !errors.Is(err, ErrConflictingAdmissionDecision) {
		t.Fatalf("Persist(different microsecond) error = %v", err)
	}
}
