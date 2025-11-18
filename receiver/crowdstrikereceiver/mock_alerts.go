// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package crowdstrikereceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/crowdstrikereceiver"
import (
	"math/rand"
	"time"

	"github.com/crowdstrike/gofalcon/falcon/client/alerts"
	"github.com/crowdstrike/gofalcon/falcon/models"
	"github.com/go-openapi/runtime"
	"github.com/go-openapi/strfmt"
)

var _ alerts.ClientService = &mockAlertsClient{}

type mockAlertsClient struct{}

func (m *mockAlertsClient) GetAggregateV2(params *alerts.GetAggregateV2Params, opts ...alerts.ClientOption) (*alerts.GetAggregateV2OK, error) {
	return &alerts.GetAggregateV2OK{}, nil
}

func (m *mockAlertsClient) GetQueriesAlertsV1(params *alerts.GetQueriesAlertsV1Params, opts ...alerts.ClientOption) (*alerts.GetQueriesAlertsV1OK, error) {
	return &alerts.GetQueriesAlertsV1OK{}, nil
}

func (m *mockAlertsClient) GetV2(params *alerts.GetV2Params, opts ...alerts.ClientOption) (*alerts.GetV2OK, error) {
	return generateGetV2OK(), nil
}

func (m *mockAlertsClient) PatchEntitiesAlertsV2(params *alerts.PatchEntitiesAlertsV2Params, opts ...alerts.ClientOption) (*alerts.PatchEntitiesAlertsV2OK, error) {
	return &alerts.PatchEntitiesAlertsV2OK{}, nil
}

func (m *mockAlertsClient) PostAggregatesAlertsV1(params *alerts.PostAggregatesAlertsV1Params, opts ...alerts.ClientOption) (*alerts.PostAggregatesAlertsV1OK, error) {
	return &alerts.PostAggregatesAlertsV1OK{}, nil
}

func (m *mockAlertsClient) PostCombinedAlertsV1(params *alerts.PostCombinedAlertsV1Params, opts ...alerts.ClientOption) (*alerts.PostCombinedAlertsV1OK, error) {
	return &alerts.PostCombinedAlertsV1OK{}, nil
}

func (m *mockAlertsClient) PostEntitiesAlertsV1(params *alerts.PostEntitiesAlertsV1Params, opts ...alerts.ClientOption) (*alerts.PostEntitiesAlertsV1OK, error) {
	return &alerts.PostEntitiesAlertsV1OK{}, nil
}

func (m *mockAlertsClient) QueryV2(params *alerts.QueryV2Params, opts ...alerts.ClientOption) (*alerts.QueryV2OK, error) {
	return generateQueryV2OKResponse(), nil
}

func (m *mockAlertsClient) UpdateV3(params *alerts.UpdateV3Params, opts ...alerts.ClientOption) (*alerts.UpdateV3OK, error) {
	return &alerts.UpdateV3OK{}, nil
}

func (m *mockAlertsClient) SetTransport(transport runtime.ClientTransport) {
	return
}

func generateGetV2OK() *alerts.GetV2OK {
	payload := generateDetectsapiPostEntitiesAlertsV2Response()
	return &alerts.GetV2OK{
		XCSTRACEID:          "x-cs-trace-id",
		XRateLimitLimit:     5,
		XRateLimitRemaining: 4,
		Payload:             payload,
	}
}

func generateQueryV2OKResponse() *alerts.QueryV2OK {
	payload := generateDetectsapiAlertQueryResponse()
	return &alerts.QueryV2OK{
		XCSTRACEID:          "x-cs-trace-id",
		XRateLimitLimit:     5,
		XRateLimitRemaining: 4,
		Payload:             payload,
	}
}

func generateDetectsapiAlertQueryResponse() *models.DetectsapiAlertQueryResponse {
	payloads := []models.DetectsapiAlertQueryResponse{
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				Pagination: &models.MsaPaging{
					Limit:  ptrTo(int32(100)),
					Offset: ptrTo(int32(0)),
					Total:  ptrTo(int64(5)),
				},
				PoweredBy: "crowdstrike-api",
				QueryTime: ptrTo(0.142),
				TraceID:   ptrTo("trace-001-abc123"),
			},
			Resources: []string{
				"alert-001-malware-detection",
				"alert-002-suspicious-activity",
				"alert-003-network-intrusion",
				"alert-004-ransomware-detected",
				"alert-005-data-exfiltration",
			},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				Pagination: &models.MsaPaging{
					Limit:  ptrTo(int32(50)),
					Offset: ptrTo(int32(0)),
					Total:  ptrTo(int64(3)),
				},
				PoweredBy: "crowdstrike-api",
				QueryTime: ptrTo(0.089),
				TraceID:   ptrTo("trace-002-def456"),
			},
			Resources: []string{
				"alert-006-phishing-campaign",
				"alert-007-brute-force-attack",
				"alert-008-privilege-escalation",
			},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				Pagination: &models.MsaPaging{
					Limit:  ptrTo(int32(100)),
					Offset: ptrTo(int32(0)),
					Total:  ptrTo(int64(1)),
				},
				PoweredBy: "crowdstrike-api",
				QueryTime: ptrTo(0.056),
				TraceID:   ptrTo("trace-003-ghi789"),
				Writes: &models.MsaResources{
					ResourcesAffected: ptrTo(int32(1)),
				},
			},
			Resources: []string{
				"alert-009-ddos-attack",
			},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				Pagination: &models.MsaPaging{
					Limit:  ptrTo(int32(100)),
					Offset: ptrTo(int32(100)),
					Total:  ptrTo(int64(7)),
				},
				PoweredBy: "crowdstrike-api",
				QueryTime: ptrTo(0.201),
				TraceID:   ptrTo("trace-004-jkl012"),
			},
			Resources: []string{
				"alert-010-sql-injection",
				"alert-011-zero-day-exploit",
			},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				Pagination: nil,
				PoweredBy:  "crowdstrike-api",
				QueryTime:  ptrTo(0.023),
				TraceID:    ptrTo("trace-005-mno345"),
			},
			Resources: []string{},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				Pagination: &models.MsaPaging{
					Limit:  ptrTo(int32(25)),
					Offset: ptrTo(int32(0)),
					Total:  ptrTo(int64(10)),
				},
				PoweredBy: "crowdstrike-api",
				QueryTime: ptrTo(0.178),
				TraceID:   ptrTo("trace-006-pqr678"),
			},
			Resources: []string{
				"alert-012-malicious-script",
				"alert-013-unauthorized-access",
				"alert-014-crypto-mining",
				"alert-015-backdoor-detected",
				"alert-016-command-injection",
				"alert-017-file-tampering",
				"alert-018-suspicious-download",
				"alert-019-policy-violation",
				"alert-020-anomaly-detected",
				"alert-021-lateral-movement",
			},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				Pagination: &models.MsaPaging{
					Limit:  ptrTo(int32(100)),
					Offset: ptrTo(int32(0)),
					Total:  ptrTo(int64(0)),
				},
				PoweredBy: "crowdstrike-api",
				QueryTime: ptrTo(0.034),
				TraceID:   ptrTo("trace-007-stu901"),
			},
			Resources: []string{},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				Pagination: &models.MsaPaging{
					Limit:  ptrTo(int32(100)),
					Offset: ptrTo(int32(0)),
					Total:  ptrTo(int64(2)),
				},
				PoweredBy: "crowdstrike-api",
				QueryTime: ptrTo(0.095),
				TraceID:   ptrTo("trace-008-vwx234"),
				Writes: &models.MsaResources{
					ResourcesAffected: ptrTo(int32(2)),
				},
			},
			Resources: []string{
				"alert-022-endpoint-compromise",
				"alert-023-credential-theft",
			},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				Pagination: nil,
				PoweredBy:  "crowdstrike-api",
				QueryTime:  ptrTo(0.012),
				TraceID:    ptrTo("trace-009-yza567"),
			},
			Resources: []string{},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				Pagination: &models.MsaPaging{
					Limit:  ptrTo(int32(50)),
					Offset: ptrTo(int32(50)),
					Total:  ptrTo(int64(6)),
				},
				PoweredBy: "crowdstrike-api",
				QueryTime: ptrTo(0.167),
				TraceID:   ptrTo("trace-010-bcd890"),
			},
			Resources: []string{
				"alert-024-trojan-detection",
				"alert-025-spyware-found",
				"alert-026-rootkit-activity",
				"alert-027-worm-propagation",
			},
		},
	}

	randInt := rand.Intn(len(payloads))
	return &payloads[randInt]
}

func generateDetectsapiPostEntitiesAlertsV2Response() *models.DetectsapiPostEntitiesAlertsV2Response {
	now := strfmt.DateTime(time.Now())
	hour1 := strfmt.DateTime(time.Now().Add(-1 * time.Hour))
	day1 := strfmt.DateTime(time.Now().Add(-24 * time.Hour))

	payloads := []models.DetectsapiPostEntitiesAlertsV2Response{
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				QueryTime: ptrTo(0.142),
				TraceID:   ptrTo("trace-001-abc123"),
			},
			Resources: []*models.DetectsAlert{
				{
					AgentID:           ptrTo("agent-001"),
					AggregateID:       ptrTo("agg-001"),
					AssignedToName:    ptrTo("John Doe"),
					AssignedToUID:     ptrTo("user-123"),
					AssignedToUUID:    ptrTo("uuid-123-456"),
					Cid:               ptrTo("cid-001"),
					CompositeID:       ptrTo("comp-001"),
					Confidence:        ptrTo(int64(85)),
					CrawledTimestamp:  &hour1,
					CreatedTimestamp:  &hour1,
					DataDomains:       []string{"endpoint"},
					Description:       ptrTo("Malware detected on endpoint"),
					DisplayName:       ptrTo("Malware Detection"),
					EmailSent:         ptrTo(true),
					External:          ptrTo(false),
					ID:                ptrTo("alert-001"),
					LinkedCaseIds:     []string{},
					MitreAttack:       []*models.DetectsMitreAttackMapping{},
					Name:              ptrTo("Malware.Generic"),
					Objective:         ptrTo("Malicious Activity"),
					PatternID:         ptrTo(int64(1001)),
					Platform:          ptrTo("Windows"),
					Product:           ptrTo("epp"),
					Resolution:        ptrTo(""),
					Scenario:          ptrTo("malware_detection"),
					SecondsToResolved: ptrTo(int64(0)),
					SecondsToTriaged:  ptrTo(int64(0)),
					Severity:          ptrTo(int64(70)),
					SeverityName:      ptrTo("High"),
					ShowInUI:          ptrTo(true),
					SourceProducts:    []string{"falcon"},
					SourceVendors:     []string{"crowdstrike"},
					Status:            ptrTo("new"),
					Tactic:            ptrTo("Execution"),
					TacticID:          ptrTo("TA0002"),
					Tags:              []string{"malware", "detected"},
					Technique:         ptrTo("User Execution"),
					TechniqueID:       ptrTo("T1204"),
					Timestamp:         &hour1,
					Type:              ptrTo("detection"),
					UpdatedTimestamp:  &hour1,
				},
			},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				QueryTime: ptrTo(0.089),
				TraceID:   ptrTo("trace-002-def456"),
			},
			Resources: []*models.DetectsAlert{
				{
					AgentID:           ptrTo("agent-002"),
					AggregateID:       ptrTo("agg-002"),
					AssignedToName:    ptrTo("Jane Smith"),
					AssignedToUID:     ptrTo("user-456"),
					AssignedToUUID:    ptrTo("uuid-456-789"),
					Cid:               ptrTo("cid-002"),
					CompositeID:       ptrTo("comp-002"),
					Confidence:        ptrTo(int64(92)),
					CrawledTimestamp:  &day1,
					CreatedTimestamp:  &day1,
					DataDomains:       []string{"network"},
					Description:       ptrTo("Suspicious network connection detected"),
					DisplayName:       ptrTo("Network Intrusion Attempt"),
					EmailSent:         ptrTo(true),
					External:          ptrTo(false),
					ID:                ptrTo("alert-002"),
					LinkedCaseIds:     []string{"case-001"},
					MitreAttack:       []*models.DetectsMitreAttackMapping{},
					Name:              ptrTo("Network.Intrusion"),
					Objective:         ptrTo("Command and Control"),
					PatternID:         ptrTo(int64(2002)),
					Platform:          ptrTo("Linux"),
					Product:           ptrTo("epp"),
					Resolution:        ptrTo("in_progress"),
					Scenario:          ptrTo("network_intrusion"),
					SecondsToResolved: ptrTo(int64(0)),
					SecondsToTriaged:  ptrTo(int64(3600)),
					Severity:          ptrTo(int64(85)),
					SeverityName:      ptrTo("Critical"),
					ShowInUI:          ptrTo(true),
					SourceProducts:    []string{"falcon"},
					SourceVendors:     []string{"crowdstrike"},
					Status:            ptrTo("in_progress"),
					Tactic:            ptrTo("Command and Control"),
					TacticID:          ptrTo("TA0011"),
					Tags:              []string{"network", "c2"},
					Technique:         ptrTo("Application Layer Protocol"),
					TechniqueID:       ptrTo("T1071"),
					Timestamp:         &day1,
					Type:              ptrTo("detection"),
					UpdatedTimestamp:  &now,
				},
			},
		},
		{
			Errors: nil,
			Meta: &models.MsaMetaInfo{
				QueryTime: ptrTo(0.056),
				TraceID:   ptrTo("trace-003-ghi789"),
			},
			Resources: []*models.DetectsAlert{
				{
					AgentID:           ptrTo("agent-003"),
					AggregateID:       ptrTo("agg-003"),
					AssignedToName:    ptrTo("Security Team"),
					AssignedToUID:     ptrTo("user-789"),
					AssignedToUUID:    ptrTo("uuid-789-012"),
					Cid:               ptrTo("cid-003"),
					CompositeID:       ptrTo("comp-003"),
					Confidence:        ptrTo(int64(88)),
					CrawledTimestamp:  &day1,
					CreatedTimestamp:  &day1,
					DataDomains:       []string{"endpoint"},
					Description:       ptrTo("Ransomware encryption activity detected"),
					DisplayName:       ptrTo("Ransomware Detected"),
					EmailSent:         ptrTo(true),
					External:          ptrTo(false),
					ID:                ptrTo("alert-003"),
					LinkedCaseIds:     []string{"case-002", "case-003"},
					MitreAttack:       []*models.DetectsMitreAttackMapping{},
					Name:              ptrTo("Ransomware.Encrypt"),
					Objective:         ptrTo("Impact"),
					PatternID:         ptrTo(int64(3003)),
					Platform:          ptrTo("Windows"),
					Product:           ptrTo("epp"),
					Resolution:        ptrTo("true_positive"),
					Scenario:          ptrTo("ransomware"),
					SecondsToResolved: ptrTo(int64(14400)),
					SecondsToTriaged:  ptrTo(int64(600)),
					Severity:          ptrTo(int64(95)),
					SeverityName:      ptrTo("Critical"),
					ShowInUI:          ptrTo(true),
					SourceProducts:    []string{"falcon"},
					SourceVendors:     []string{"crowdstrike"},
					Status:            ptrTo("closed"),
					Tactic:            ptrTo("Impact"),
					TacticID:          ptrTo("TA0040"),
					Tags:              []string{"ransomware", "critical", "true_positive"},
					Technique:         ptrTo("Data Encrypted for Impact"),
					TechniqueID:       ptrTo("T1486"),
					Timestamp:         &day1,
					Type:              ptrTo("detection"),
					UpdatedTimestamp:  &day1,
				},
			},
		},
	}

	randInt := rand.Intn(len(payloads))
	return &payloads[randInt]
}

func ptrTo[T any](v T) *T {
	return &v
}
