CREATE EXTENSION IF NOT EXISTS postgis;

-- An anonymous visitor, identified by a random id kept in a cookie. There are
-- no accounts: each visitor sees the demo listings plus their own.
CREATE TABLE visitors (
    id         uuid PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE listings (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- NULL for demo listings, which every visitor sees.
    visitor_id      uuid REFERENCES visitors (id) ON DELETE CASCADE,
    address         text NOT NULL CHECK (length(address) BETWEEN 1 AND 300),
    -- Money as integer cents, so no rounding errors.
    warm_rent_cents integer NOT NULL CHECK (warm_rent_cents > 0),
    size_m2         numeric(6, 2) NOT NULL CHECK (size_m2 > 0),
    -- German listings count half rooms, e.g. 2.5 Zimmer.
    rooms           numeric(3, 1) NOT NULL CHECK (rooms > 0),
    source_url      text,
    -- NULL until geocoded.
    location        geography (Point, 4326),
    status          text NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'scored', 'failed', 'unsupported_area')),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX listings_visitor_id_idx ON listings (visitor_id);
