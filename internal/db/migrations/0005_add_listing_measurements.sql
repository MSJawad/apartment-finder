-- Where a listing was geocoded to, and what was measured around it.

-- not_found replaces unsupported_area: either way, the address couldn't be
-- placed inside the region.
ALTER TABLE listings DROP CONSTRAINT listings_status_check;
ALTER TABLE listings ADD CONSTRAINT listings_status_check
    CHECK (status IN ('pending', 'scored', 'not_found', 'failed'));

ALTER TABLE listings
    -- The address as the geocoder matched it, shown so a wrong match is visible.
    ADD COLUMN geocoded_label    text,
    ADD COLUMN geocode_precision text CHECK (geocode_precision IN ('house', 'street')),
    -- Why a listing is not_found or failed, for the person who added it.
    ADD COLUMN status_detail     text,
    ADD COLUMN scored_at         timestamptz;

-- One row per scoring criterion, e.g. rent per m² or the walk to the nearest
-- supermarket.
CREATE TABLE listing_measurements (
    listing_id bigint NOT NULL REFERENCES listings (id) ON DELETE CASCADE,
    criterion  text NOT NULL,
    -- measured: value holds it. none_nearby: no place of the category within
    -- reach, which scores 0. unreachable: places exist but no route was
    -- found, so the criterion is left out of the score.
    outcome    text NOT NULL CHECK (outcome IN ('measured', 'none_nearby', 'unreachable')),
    value      double precision,
    -- For travel times: the place the time is to.
    place_name text,
    distance_m double precision,
    PRIMARY KEY (listing_id, criterion),
    CHECK ((outcome = 'measured') = (value IS NOT NULL))
);
