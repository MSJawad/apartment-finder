# apartment-finder

Scores apartment listings by what matters day to day: walking and cycling time to the
places you go most, nearby amenities, rent and size. Each listing gets a 0-100 score with
a breakdown, shown on a map and in a ranked table with adjustable weights.

It started as a Google Sheets + Apps Script tool I used while apartment hunting in Erfurt.
This is the rebuild as a deployed service: Go, PostgreSQL + PostGIS, a self-hosted OSRM
router, and k3s on Oracle Cloud's free tier.

## Status

Work in progress. v1 is done when a stranger can open the URL, add a listing, and see it
scored on the map.

## Decisions

Each non-obvious choice, with the options considered and why this one won.
