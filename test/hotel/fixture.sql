-- Small, deterministic data for assertions that need exact values (the TypeScript client tests,
-- typescript/tests, through test/tsclient). Applied after schema.sql on a fresh database, so every identity id
-- below is predictable : ids count up from 1 in insertion order. test/seed is the opposite, random volume data.

insert into hotel.chains (name) values ('Grand Chain');

insert into hotel.properties (chain_id, name, location, star_rating, description, created_at) values
  (1, 'Marina Bay Grand Hotel', point(103.86, 1.28), 5, 'Waterfront luxury hotel with a rooftop pool', '2024-01-15 10:00:00+00'),
  (1, 'Harbour Lights Inn', point(-122.40, 37.80), 3, 'Budget-friendly stay near the harbour', '2024-02-20 08:30:00+00'),
  (null, 'Lonely Cabin', null, null, null, '2024-03-01 00:00:00+00');

insert into hotel.room_types (property_id, name, base_price, capacity) values
  (1, 'Deluxe', 129.99, 2),
  (1, 'Suite', 349.50, 4),
  (2, 'Standard', 59.00, 2);

insert into hotel.rooms (property_id, room_type_id, room_number, floor, status, features) values
  (1, 1, '101', 1, 'clean', '{wifi,balcony}'),
  (1, 1, '102', 1, 'dirty', '{wifi}'),
  (1, 2, '201', 2, 'clean', '{wifi,balcony,jacuzzi}'),
  (2, 3, '1', null, 'maintenance', '{}');

insert into hotel.amenities (name) values ('Pool'), ('Gym'), ('Parking');

insert into hotel.property_amenities (property_id, amenity_id) values (1, 1), (1, 2), (2, 3);
