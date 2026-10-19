-- A charge whose retry window ends before the customer hands over a payment
-- method fails with payment_method_missing (HU-W1.3-02, ChargeStore.
-- ExpireWithoutPaymentMethod), and operations sees it among the issuance
-- exceptions. The CHECK of 0001 only listed the two reasons of HA-D02, so
-- PostgreSQL refused that update and the charge stayed pending for ever
-- (finding H-9 of the AWS phase of HA-D02, decision #61).
--
-- 0001 and 0002 are already applied in other databases, so they are not
-- edited: the constraint is replaced here with the same reasons plus the new
-- one. Every existing row already satisfies it, so nothing is rewritten.

ALTER TABLE charge_attempt DROP CONSTRAINT IF EXISTS charge_attempt_failure_reason_check;
ALTER TABLE charge_attempt ADD CONSTRAINT charge_attempt_failure_reason_check
    CHECK (failure_reason IN ('retry_window_expired', 'payment_rejected', 'payment_method_missing'));
