-- Visitor activity, where listings come from, and the log that limits count.

-- A visitor not seen for 30 days is deleted with everything they added.
ALTER TABLE visitors ADD COLUMN last_seen_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX visitors_last_seen_at_idx ON visitors (last_seen_at);

-- Where a listing came from, e.g. "manual" for the add-listing form.
ALTER TABLE listings ADD COLUMN source text NOT NULL DEFAULT 'manual';

-- One row per listing or place a visitor adds. Limits count these rather
-- than listings, so deleting a listing doesn't give its slot back. Rows older
-- than a day no longer count and are cleaned up.
CREATE TABLE submissions (
    visitor_id uuid NOT NULL REFERENCES visitors (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX submissions_visitor_id_created_at_idx ON submissions (visitor_id, created_at);
CREATE INDEX submissions_created_at_idx ON submissions (created_at);
