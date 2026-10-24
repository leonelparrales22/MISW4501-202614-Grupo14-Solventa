-- History of a holder for HU-M1.3-01 (CA-3): one expired consent and one
-- revoked consent with its receipt, as the revocation of HU-M1.3-02 will
-- write it. Only inserts: the audit tables are append-only. The consents go
-- first: the trigger consent_event_requires_consent refuses an event whose
-- consent does not exist (23503).
--
-- Usage (psql variables):
--   psql -v holder_document=1020304050 -v suffix=r123 -f seed-history.sql
-- holder_document is the document of the holder; suffix keeps the ids unique.

INSERT INTO consent_record (consent_id, customer_id, holder_document_id, holder_full_name, terms_version, scope, purpose,
                            channel, partner_id, application_id, user_agent, granted_at)
VALUES
    ('cns-expired-' || :'suffix', 'cust-history-' || :'suffix', :'holder_document', 'Titular de Prueba', 'v1',
     '[{"code":"open_finance","data_categories":["credit_products","balances","payment_behavior"],"validity":"12_months"},
       {"code":"open_data","data_categories":["public_records"],"validity":"12_months"},
       {"code":"kyc","data_categories":["identity_verification"],"validity":"single_use"}]'::jsonb,
     'risk_profiling_mortgage_life_offer', 'embedded', 'aliado-banco-1', 'app-expired-' || :'suffix', 'seed',
     now() - interval '13 months'),
    ('cns-revoked-' || :'suffix', 'cust-history-' || :'suffix', :'holder_document', 'Titular de Prueba', 'v1',
     '[{"code":"open_finance","data_categories":["credit_products","balances","payment_behavior"],"validity":"12_months"},
       {"code":"open_data","data_categories":["public_records"],"validity":"12_months"},
       {"code":"kyc","data_categories":["identity_verification"],"validity":"single_use"}]'::jsonb,
     'risk_profiling_mortgage_life_offer', 'embedded', 'aliado-banco-1', 'app-revoked-' || :'suffix', 'seed',
     now() - interval '4 months');

INSERT INTO consent_event (event_id, consent_id, status, occurred_at, actor_type, channel, receipt_id)
VALUES
    ('evt-revocation-requested-' || :'suffix', 'cns-revoked-' || :'suffix', 'revocation_requested',
     now() - interval '3 days' - interval '5 seconds', 'insured', 'mobile', NULL),
    ('evt-revoked-' || :'suffix', 'cns-revoked-' || :'suffix', 'revoked',
     now() - interval '3 days', 'system', 'mobile', 'REV-' || upper(:'suffix'));
