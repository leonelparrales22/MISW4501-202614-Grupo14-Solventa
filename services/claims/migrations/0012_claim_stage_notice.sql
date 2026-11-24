-- HU-M3.3-01: the notice of every change of stage of a claim the insured
-- follows (in evaluation, approved, paid).
--
-- Numbered 0012, after 0011_parametric_resolution of HU-M2.4-01. It reads
-- claim_status_change (0007) and writes to outbox_event (0011).

-- Every change of status the notifier already looked at, with the event it
-- wrote to the outbox (ClaimStageChanged), or NULL when the change notifies
-- nothing (another stage, or a parametric claim, which has the push of its
-- payment). A change is looked at once: of two tasks, the second finds it
-- here. There is no foreign key to claim_status_change on purpose: that table
-- keeps its own error when somebody tries to truncate it.
CREATE TABLE IF NOT EXISTS claim_stage_notice (
    change_id    BIGINT      PRIMARY KEY,
    event_id     TEXT,
    processed_at TIMESTAMPTZ NOT NULL
);

-- Where the notices start: the changes recorded before this migration are
-- history, and the insured is not told about them now.
CREATE TABLE IF NOT EXISTS claim_stage_notice_start (
    singleton       BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    first_change_id BIGINT  NOT NULL
);

INSERT INTO claim_stage_notice_start (first_change_id)
SELECT COALESCE(MAX(id), 0) + 1 FROM claim_status_change
ON CONFLICT (singleton) DO NOTHING;
