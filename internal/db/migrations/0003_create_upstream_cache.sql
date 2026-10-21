-- Responses from external services, so the same request isn't sent again
-- while its answer is fresh. "Nothing found" answers are cached too, for a
-- shorter time; errors never are.
CREATE TABLE upstream_cache (
    provider   text NOT NULL,
    -- sha256 of the request URL; the URL itself can be too long to index.
    key        text NOT NULL,
    url        text NOT NULL,
    body       bytea NOT NULL,
    miss       boolean NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (provider, key)
);
