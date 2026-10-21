-- A visitor's personal places, such as work or a university, and the trip
-- from each listing to each place.

CREATE TABLE places (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    visitor_id        uuid NOT NULL REFERENCES visitors (id) ON DELETE CASCADE,
    name              text NOT NULL CHECK (length(name) BETWEEN 1 AND 40),
    address           text NOT NULL CHECK (length(address) BETWEEN 1 AND 300),
    -- How the visitor gets there.
    mode              text NOT NULL CHECK (mode IN ('walk', 'bike')),
    -- NULL until geocoded, like a listing's.
    location          geography (Point, 4326),
    geocoded_label    text,
    geocode_precision text CHECK (geocode_precision IN ('house', 'street')),
    status            text NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'located', 'not_found', 'failed')),
    status_detail     text,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX places_visitor_id_idx ON places (visitor_id);

-- The trip from a listing to a place, by the place's mode. Always measured
-- in that direction, whichever of the two was added last.
CREATE TABLE place_measurements (
    listing_id bigint NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    place_id   bigint NOT NULL REFERENCES places (id) ON DELETE CASCADE,
    -- NULL minutes: no route was found, so the place is left out of the
    -- listing's score.
    minutes    double precision,
    distance_m double precision,
    PRIMARY KEY (listing_id, place_id),
    CHECK ((minutes IS NULL) = (distance_m IS NULL))
);

-- Deleting a place cascades through this index.
CREATE INDEX place_measurements_place_id_idx ON place_measurements (place_id);
