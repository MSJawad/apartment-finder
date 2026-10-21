-- Everyday places listings are scored against: supermarkets, stops, parks
-- and so on, imported from the region's OpenStreetMap extract.
CREATE TABLE amenities (
    category text NOT NULL,
    osm_type text NOT NULL CHECK (osm_type IN ('node', 'way', 'relation')),
    osm_id   bigint NOT NULL,
    name     text,
    -- A point for a single place, a polygon for an area such as a park.
    geom     geography NOT NULL,
    PRIMARY KEY (category, osm_type, osm_id)
);

-- Serves "nearest places to this point" (ORDER BY geom <-> point) and
-- radius searches (ST_DWithin) without scanning the table.
CREATE INDEX amenities_geom_idx ON amenities USING gist (geom);
