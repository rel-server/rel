-- Foreign-key indexes the schema leaves out. rel refuses a join whose columns
-- are not indexed (JOIN_MISSING_INDEX), and a production database would have
-- them anyway ; every server benefits equally. Applied to the golden database
-- after seeding.
create index if not exists rooms_room_type_id_idx on hotel.rooms (room_type_id);
