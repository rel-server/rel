# PostGIS

Adds `geometry`/`geography` support to the generated TypeScript client, the same treatment `docs/content/typescript/index.md ## Typed wire values` gives every other Postgres type whose JSON wire representation the client currently either lies about or doesn't type at all.

This document starts from a preliminary exploration, confirmed against a live `postgis/postgis` instance, not from assumption.

## Wire format

A bare `geometry`/`geography` column, embedded via `jsonb_build_object` (the same mechanism `query/sql.go` already uses for every other column), is already a structured JSON object — not opaque WKB/hex, and not something that needs an explicit `ST_AsGeoJSON()` call server-side.

```
select jsonb_build_object('geom', 'SRID=4326;POINT(1.5 2.5)'::geometry);
-- {"geom": {"type": "Point", "coordinates": [1.5, 2.5], "crs": {"type": "name", "properties": {"name": "EPSG:4326"}}}}
```

> **Why:** PostGIS registers its own cast to `jsonb` for `geometry`/`geography`, which `jsonb_build_object` uses automatically when a geometry value is passed to it — no query-compiler change to how columns are selected is needed for this to already produce usable JSON.

This is GeoJSON-shaped, not standard GeoJSON : it carries an extra `crs` member RFC 7946 doesn't have.

## Type introspection

`pg.Type` (`pg/info_type.go`) already discovers `geometry`/`geography` today, in whichever schema they were installed into (typically `public`).

> **Why:** `INFO_QUERY_TYPES` (`pg/info_type.go:220`) has no schema restriction at all — it joins every `pg_type` row against `pg_namespace` unconditionally. No Go-side introspection change is needed to see these types ; the gap is purely `tsgen`'s `baseScalarTypes` map (`tsgen/types.go`) not having an entry for them, the same gap range types had.

`geometry`/`geography` are plain base types, not domain/enum/composite ; `tsTypeExpr` (`tsgen/types.go`) already falls through to the flat `baseScalarTypes` name lookup for them, matching every other Postgres type in this position.

> **Question:** matching is by bare type name only, regardless of schema — a user-defined type in some other schema also happening to be named `geometry` would collide. Narrow, pre-existing risk (the same is already true for every other `baseScalarTypes` entry), not new to this feature.

## Client-side type

A self-contained set of GeoJSON interfaces (`Point`, `LineString`, `Polygon`, `MultiPoint`, `MultiLineString`, `MultiPolygon`, `GeometryCollection`), defined once in the generated preamble, each with an optional `crs` member alongside its standard RFC 7946 shape.

> **Why:** `@types/geojson` exists and is the obvious off-the-shelf choice, but pulling it in breaks `database.ts`'s "copy the file in, no dependencies" design — and its types don't have PostGIS's own `crs` member anyway, so it would need extending regardless of where it came from.

`geometry` and `geography` map to the same `Geometry` union type ; nothing distinguishes them at the type level.

> **Question:** should it? They differ in a way the type currently can't express — see below.

## The geometry/geography safety distinction

`geography` coordinates are always WGS84 longitude/latitude degrees, unambiguous regardless of the column.

`geometry` coordinates are in whatever SRID the column was declared with — degrees, meters, or anything else a projected coordinate system uses ; `crs.properties.name` names it, but only at runtime, on the value, never in the static type.

> **Why:** this is the same shape of problem `money`'s locale-dependent formatting is — a fact that varies per-database-configuration, which a compile-time type generated once can't fully capture. Unlike `timestamp` vs. `timestamptz`, there's no branded-type fix available here : the ambiguity isn't about which of two Postgres types was used, it's about which SRID a single `geometry` column happens to be declared with, and that's `tsgen`-time information current already has (the column's declared SRID is knowable from Postgres's own `geometry_columns` view or the type modifier), just not yet surfaced.

> **Question:** `tsgen` could read a `geometry` column's declared SRID (typmod, or `geometry_columns`) and either (a) reject/warn on a non-4326 SRID reaching the client as `Geometry` without a caveat, or (b) encode the SRID into the branded type per column, the same spirit as this spec's other branded types. Left open — needs deciding whether it's worth the added introspection query.

## No accessor needed

Unlike every other Postgres type this client re-types client-side, no accessor/parsing helper is needed here at all.

> **Why:** the wire value is already a structured JSON object, not a string requiring a parse step. A client's own geometry library (Turf.js and similar) operates directly on the `Geometry` shape ; which library to use is left to the client, the same way `Date` vs. `Temporal` was for timestamps.

## Write side

Not yet explored.

> **Question:** does `jsonb_build_object`'s own reverse direction (accepting a GeoJSON-shaped `jsonb` value as `INSERT`/`UPDATE` input for a `geometry` column) work the same automatic way the read side does, or does writing need an explicit `ST_GeomFromGeoJSON()` in the generated SQL ? Needs the same kind of empirical check the read side just got, not assumed.

## Multi-geometry / other PostGIS types

Only `Point` was verified against a live instance. `LineString`/`Polygon`/`MultiPolygon`/`GeometryCollection`, and non-geometry PostGIS types (`box2d`, `box3d`, raster types), are assumed to follow the same `jsonb` cast mechanism but are not yet individually confirmed.
