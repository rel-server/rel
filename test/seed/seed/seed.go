// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package seed holds the actual hotel/booking fixture data generation logic
// (see test/README.md) — extracted out of test/seed/main.go (which stays a
// thin `package main` wrapper around Run below) so it's importable from
// other packages, notably the read-path benchmarks in query_bench, without
// duplicating ~500 lines of seeding code (AGENTS.md's DRY rule).
//
// A fixed seed value makes every run reproducible : the volume of data is
// random-looking, but not actually different from one run to the next. A
// handful of "named" rows (marked below) are hand-picked rather than
// generated, for scenarios worth referencing by name in future tests (a
// guest with no bookings, a property with no reviews, a multi-level staff
// manager chain, ...).
package seed

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/brianvoe/gofakeit/v7"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/samber/oops"
)

// Run seeds the test/hotel schema (already applied to pool's database)
// with fake data, deterministically derived from seedValue — the same
// seedValue always produces the same data. Returns an error rather than
// calling os.Exit, so callers (the main.go wrapper, or a benchmark's setup)
// can decide how to handle failure themselves.
func Run(ctx context.Context, pool *pgxpool.Pool, seedValue int64) error {
	if err := gofakeit.Seed(seedValue); err != nil {
		return oops.Wrapf(err, "gofakeit.Seed")
	}
	rng := rand.New(rand.NewSource(seedValue))

	s := &seeder{ctx: ctx, pool: pool, rng: rng}
	s.run()
	return nil
}

type seeder struct {
	ctx  context.Context
	pool *pgxpool.Pool
	rng  *rand.Rand

	loyaltyTierIDs []int64
	amenityIDs     []int64

	chainIDs    []int64
	propertyIDs []int64

	roomTypes []roomType
	rooms     []room

	guestIDs               []int64
	namedGuestNoBookingsID int64

	bookings []booking

	staffIDs []int64
}

type roomType struct {
	id, propertyID int64
	basePrice      float64
}

type room struct {
	id, propertyID, roomTypeID int64
	occupied                   []occupiedRange
}

type occupiedRange struct{ from, to time.Time }

type booking struct {
	id         string // uuid
	guestID    int64
	roomID     int64
	propertyID int64
	from, to   time.Time
	status     string
}

func (s *seeder) run() {
	s.seedLoyaltyTiers()
	s.seedAmenities()
	s.seedChains()
	s.seedProperties()
	s.seedRoomTypesAndRooms()
	s.seedPropertyAmenities()
	s.seedGuests()
	s.seedPaymentMethods()
	s.seedBookings()
	s.seedBookingGuests()
	s.seedPayments()
	s.seedReviews()
	s.seedRatePlans()
	s.seedStaff()
}

func (s *seeder) exec(sql string, args ...any) {
	_, err := s.pool.Exec(s.ctx, sql, args...)
	must(err)
}

func (s *seeder) queryInt64(sql string, args ...any) int64 {
	var id int64
	must(s.pool.QueryRow(s.ctx, sql, args...).Scan(&id))
	return id
}

func (s *seeder) queryString(sql string, args ...any) string {
	var id string
	must(s.pool.QueryRow(s.ctx, sql, args...).Scan(&id))
	return id
}

func must(err error) {
	if err != nil {
		panic(oops.Wrapf(err, "seed failed"))
	}
}

// ---- loyalty tiers, amenities : small fixed catalogs -----------------------

func (s *seeder) seedLoyaltyTiers() {
	tiers := []struct {
		name      string
		minPoints int
	}{
		{"Bronze", 0}, {"Silver", 1000}, {"Gold", 5000}, {"Platinum", 15000},
	}
	for _, t := range tiers {
		id := s.queryInt64(
			`insert into hotel.loyalty_tiers (name, min_points) values ($1, $2) returning id`,
			t.name, t.minPoints,
		)
		s.loyaltyTierIDs = append(s.loyaltyTierIDs, id)
	}
}

func (s *seeder) seedAmenities() {
	names := []string{
		"Free WiFi", "Swimming Pool", "Fitness Center", "Free Parking",
		"Breakfast Included", "Spa", "Pet Friendly", "Airport Shuttle",
		"Business Center", "Bar", "Restaurant", "Room Service",
	}
	for _, n := range names {
		id := s.queryInt64(`insert into hotel.amenities (name) values ($1) returning id`, n)
		s.amenityIDs = append(s.amenityIDs, id)
	}
}

// ---- chains, properties -----------------------------------------------------

func (s *seeder) seedChains() {
	// named, not generated : referenced by the property/room fixtures below
	names := []string{"Meridian Hotels", "Solstice Collection", "Harbor & Vine"}
	for _, n := range names {
		id := s.queryInt64(`insert into hotel.chains (name) values ($1) returning id`, n)
		s.chainIDs = append(s.chainIDs, id)
	}
}

func (s *seeder) seedProperties() {
	const propertiesPerChain = 4
	for _, chainID := range s.chainIDs {
		for i := 0; i < propertiesPerChain; i++ {
			city := gofakeit.City()
			name := city + " " + gofakeit.RandomString([]string{"Grand Hotel", "Plaza", "Inn", "Resort", "Suites"})
			lng, lat := gofakeit.Longitude(), gofakeit.Latitude()
			rating := s.rng.Intn(5) + 1
			desc := gofakeit.Paragraph() + " Located in " + city + "."

			id := s.queryInt64(`
				insert into hotel.properties (chain_id, name, location, star_rating, description)
				values ($1, $2, point($3, $4), $5, $6)
				returning id
			`, chainID, name, lng, lat, rating, desc)
			s.propertyIDs = append(s.propertyIDs, id)
		}
	}

	// named : a property with no reviews at all, for property_average_rating
	// to average over nothing (worth being able to reference by description)
	id := s.queryInt64(`
		insert into hotel.properties (chain_id, name, location, star_rating, description)
		values ($1, 'The Unreviewed Inn', point(0, 0), 3, 'Brand new, nobody has stayed here yet.')
		returning id
	`, s.chainIDs[0])
	s.propertyIDs = append(s.propertyIDs, id)
}

// ---- room types, rooms -------------------------------------------------------

func (s *seeder) seedRoomTypesAndRooms() {
	kinds := []struct {
		name     string
		price    float64
		capacity int
	}{
		{"Standard", 89.00, 2},
		{"Deluxe", 149.00, 2},
		{"Suite", 249.00, 4},
	}
	features := []string{"Sea View", "Balcony", "King Bed", "Bathtub", "Kitchenette", "City View"}

	for _, propertyID := range s.propertyIDs {
		var typesHere []roomType
		for _, k := range kinds {
			id := s.queryInt64(`
				insert into hotel.room_types (property_id, name, base_price, capacity)
				values ($1, $2, $3, $4) returning id
			`, propertyID, k.name, k.price, k.capacity)
			rt := roomType{id: id, propertyID: propertyID, basePrice: k.price}
			typesHere = append(typesHere, rt)
			s.roomTypes = append(s.roomTypes, rt)
		}

		floor := 1
		roomNum := 1
		for _, rt := range typesHere {
			for i := 0; i < 5; i++ {
				number := fmt.Sprintf("%d%02d", floor, roomNum)
				roomNum++
				if roomNum > 20 {
					roomNum = 1
					floor++
				}

				var feat []string
				n := s.rng.Intn(3)
				perm := s.rng.Perm(len(features))
				for j := 0; j < n; j++ {
					feat = append(feat, features[perm[j]])
				}
				if feat == nil {
					feat = []string{}
				}

				id := s.queryInt64(`
					insert into hotel.rooms (property_id, room_type_id, room_number, floor, features)
					values ($1, $2, $3, $4, $5) returning id
				`, propertyID, rt.id, number, floor, feat)
				s.rooms = append(s.rooms, room{id: id, propertyID: propertyID, roomTypeID: rt.id})
			}
		}
	}
}

func (s *seeder) seedPropertyAmenities() {
	for _, propertyID := range s.propertyIDs {
		perm := s.rng.Perm(len(s.amenityIDs))
		n := 3 + s.rng.Intn(len(s.amenityIDs)-3)
		for i := 0; i < n; i++ {
			s.exec(`insert into hotel.property_amenities (property_id, amenity_id) values ($1, $2)`,
				propertyID, s.amenityIDs[perm[i]])
		}
	}
}

// ---- guests, payment methods -------------------------------------------------

func (s *seeder) seedGuests() {
	const count = 200
	for i := 0; i < count; i++ {
		first, last := gofakeit.FirstName(), gofakeit.LastName()
		email := gofakeit.Email()
		phone := gofakeit.Phone()
		dob := gofakeit.DateRange(time.Date(1950, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2005, 1, 1, 0, 0, 0, 0, time.UTC))

		var tierID *int64
		if s.rng.Intn(3) != 0 { // ~2/3 of guests have a loyalty tier
			t := s.loyaltyTierIDs[s.rng.Intn(len(s.loyaltyTierIDs))]
			tierID = &t
		}

		id := s.queryInt64(`
			insert into hotel.guests (email, first_name, last_name, phone, date_of_birth, billing_address, loyalty_tier_id)
			values ($1, $2, $3, $4, $5, ROW($6, $7, $8, $9, $10)::hotel.address, $11)
			returning id
		`, email, first, last, phone, dob.Format("2006-01-02"),
			gofakeit.StreetName(), gofakeit.City(), gofakeit.StateAbr(), gofakeit.Zip(), gofakeit.Country(),
			tierID)
		s.guestIDs = append(s.guestIDs, id)
	}

	// named : a guest who has never booked anything
	s.namedGuestNoBookingsID = s.queryInt64(`
		insert into hotel.guests (email, first_name, last_name, billing_address)
		values ('never.booked@example.test', 'Never', 'Booked', ROW('1 Nowhere St', 'Nullville', 'NA', '00000', 'Nowhere')::hotel.address)
		returning id
	`)
	s.guestIDs = append(s.guestIDs, s.namedGuestNoBookingsID)
}

func (s *seeder) seedPaymentMethods() {
	brands := []string{"Visa", "Mastercard", "American Express", "Discover"}
	for _, guestID := range s.guestIDs {
		n := s.rng.Intn(3) // 0, 1, or 2 payment methods
		for i := 0; i < n; i++ {
			brand := brands[s.rng.Intn(len(brands))]
			last4 := fmt.Sprintf("%04d", s.rng.Intn(10000))
			month := s.rng.Intn(12) + 1
			year := time.Now().Year() + 1 + s.rng.Intn(4)
			isDefault := i == 0

			s.exec(`
				insert into hotel.payment_methods (guest_id, card, is_default)
				values ($1, ROW($2, $3, $4, $5)::hotel.card_summary, $6)
			`, guestID, brand, last4, month, year, isDefault)
		}
	}
}

// ---- bookings, booking_guests, payments --------------------------------------

func (s *seeder) seedBookings() {
	statuses := []string{"confirmed", "confirmed", "confirmed", "checked_out", "checked_out", "cancelled"}
	horizon := time.Now().AddDate(0, -6, 0)

	// bookable guests exclude the one deliberately left with no bookings
	var bookableGuests []int64
	for _, g := range s.guestIDs {
		if g != s.namedGuestNoBookingsID {
			bookableGuests = append(bookableGuests, g)
		}
	}

	const count = 500
	for i := 0; i < count; i++ {
		r := &s.rooms[s.rng.Intn(len(s.rooms))]
		guestID := bookableGuests[s.rng.Intn(len(bookableGuests))]
		status := statuses[s.rng.Intn(len(statuses))]

		from, to, ok := s.pickNonOverlapping(r, horizon)
		if !ok {
			continue // this room is fully booked in our simulated window ; skip and try another next iteration
		}

		id := s.queryString(`
			insert into hotel.bookings (guest_id, room_id, stay, status)
			values ($1, $2, tstzrange($3, $4), $5)
			returning id
		`, guestID, r.id, from, to, status)

		s.bookings = append(s.bookings, booking{
			id: id, guestID: guestID, roomID: r.id, propertyID: r.propertyID,
			from: from, to: to, status: status,
		})
	}
}

// pickNonOverlapping finds a stay for room r that doesn't clash with
// what's already assigned to it this run, tracked in-memory.
func (s *seeder) pickNonOverlapping(r *room, horizon time.Time) (time.Time, time.Time, bool) {
	for attempt := 0; attempt < 20; attempt++ {
		offsetDays := s.rng.Intn(180)
		nights := 1 + s.rng.Intn(7)
		from := horizon.AddDate(0, 0, offsetDays)
		to := from.AddDate(0, 0, nights)

		clash := false
		for _, occ := range r.occupied {
			if from.Before(occ.to) && to.After(occ.from) {
				clash = true
				break
			}
		}
		if !clash {
			r.occupied = append(r.occupied, occupiedRange{from: from, to: to})
			return from, to, true
		}
	}
	return time.Time{}, time.Time{}, false
}

func (s *seeder) seedBookingGuests() {
	for _, b := range s.bookings {
		s.exec(`insert into hotel.booking_guests (booking_id, guest_id) values ($1, $2)`, b.id, b.guestID)
		if s.rng.Intn(3) == 0 { // roughly 1/3 of bookings have a second guest
			extra := s.guestIDs[s.rng.Intn(len(s.guestIDs))]
			if extra != b.guestID {
				s.exec(`insert into hotel.booking_guests (booking_id, guest_id) values ($1, $2) on conflict do nothing`, b.id, extra)
			}
		}
	}
}

func (s *seeder) seedPayments() {
	currencies := []string{"USD", "EUR", "GBP"}
	for _, b := range s.bookings {
		if b.status == "cancelled" {
			continue // no payment worth recording for a cancelled booking
		}
		amount := 89.0 + s.rng.Float64()*400.0
		currency := currencies[s.rng.Intn(len(currencies))]
		status := "completed"
		var paidAt *time.Time
		t := b.from.AddDate(0, 0, -s.rng.Intn(10))
		paidAt = &t

		s.exec(`
			insert into hotel.payments (booking_id, amount, currency, status, paid_at)
			values ($1, $2, $3, $4, $5)
		`, b.id, amount, currency, status, paidAt)
	}
}

// ---- reviews, rate plans, staff -----------------------------------------------

func (s *seeder) seedReviews() {
	for _, b := range s.bookings {
		if b.status != "checked_out" {
			continue // only completed stays get reviewed
		}
		if s.rng.Intn(2) != 0 { // about half of eligible stays leave a review
			continue
		}
		rating := 1 + s.rng.Intn(5)
		comment := gofakeit.Sentence()

		s.exec(`
			insert into hotel.reviews (guest_id, property_id, booking_id, rating, comment)
			values ($1, $2, $3, $4, $5)
		`, b.guestID, b.propertyID, b.id, rating, comment)
	}
}

func (s *seeder) seedRatePlans() {
	for _, propertyID := range s.propertyIDs {
		s.exec(`
			insert into hotel.rate_plans (property_id, name, refundable, cancellation_window)
			values ($1, 'Flexible', true, '24 hours')
		`, propertyID)
		s.exec(`
			insert into hotel.rate_plans (property_id, name, refundable, cancellation_window)
			values ($1, 'Non-Refundable', false, '0 hours')
		`, propertyID)
	}
}

func (s *seeder) seedStaff() {
	roles := []string{"Front Desk", "Housekeeping", "Concierge", "Maintenance"}
	for _, propertyID := range s.propertyIDs {
		managerID := s.queryInt64(`
			insert into hotel.staff (property_id, name, role) values ($1, $2, 'General Manager') returning id
		`, propertyID, gofakeit.Name())
		s.staffIDs = append(s.staffIDs, managerID)

		// A deputy reporting to the GM, who has reports — 3 levels deep,
		// so the self-join fixture has something non-trivial to walk.
		deputyID := s.queryInt64(`
			insert into hotel.staff (property_id, manager_id, name, role) values ($1, $2, $3, 'Assistant Manager') returning id
		`, propertyID, managerID, gofakeit.Name())
		s.staffIDs = append(s.staffIDs, deputyID)

		for i := 0; i < 3; i++ {
			role := roles[s.rng.Intn(len(roles))]
			id := s.queryInt64(`
				insert into hotel.staff (property_id, manager_id, name, role) values ($1, $2, $3, $4) returning id
			`, propertyID, deputyID, gofakeit.Name(), role)
			s.staffIDs = append(s.staffIDs, id)
		}
	}
}
