-- The background job queue; internal/queue explains how it is used.
CREATE TABLE jobs (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind         text NOT NULL,
    payload      jsonb NOT NULL DEFAULT '{}',
    status       text NOT NULL DEFAULT 'pending'
                 CHECK (status IN ('pending', 'running', 'done', 'dead')),
    -- Counts claims, so a job that keeps crashing its worker still runs out.
    attempts     integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL DEFAULT 8 CHECK (max_attempts > 0),
    -- The earliest time the job may run; a retry moves it into the future.
    run_at       timestamptz NOT NULL DEFAULT now(),
    -- While running: when the worker's lease ends. After that the job counts
    -- as abandoned and another worker may claim it.
    locked_until timestamptz,
    -- A new random token on every claim. Completing or failing a job requires
    -- the current token, so a worker whose lease expired can't overwrite the
    -- result of the worker that took the job over.
    lease_token  uuid,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Workers only ever look for unfinished jobs, so the index leaves out the
-- done and dead ones, which would otherwise make up most of the table.
CREATE INDEX jobs_unfinished_run_at_idx ON jobs (run_at) WHERE status IN ('pending', 'running');
